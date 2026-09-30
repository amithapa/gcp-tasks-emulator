package worker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/queues"
	"cloud-tasks-emulator/internal/tasks"
)

func TestBackoff(t *testing.T) {
	tests := []struct {
		initial, max, retryCount int
		want                     int
	}{
		{1, 60, 1, 1},
		{1, 60, 2, 2},
		{1, 60, 3, 4},
		{1, 60, 5, 16},
		{1, 60, 10, 60},
		{2, 30, 2, 4},
	}
	for _, tt := range tests {
		got := backoff(tt.initial, tt.max, tt.retryCount)
		if got != tt.want {
			t.Errorf("backoff(%d, %d, %d) = %d, want %d", tt.initial, tt.max, tt.retryCount, got, tt.want)
		}
	}
}

func newEnv(t *testing.T) (*db.DB, *config.Config, *queues.Repository, *tasks.Repository) {
	t.Helper()
	tmp, err := os.CreateTemp("", "worker_test_*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := tmp.Name()
	tmp.Close()
	t.Cleanup(func() { os.Remove(path) })
	database, err := db.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	cfg := &config.Config{
		WorkerConcurrency:       10,
		WorkerPollIntervalMs:    10,
		InitialBackoffSeconds:   1,
		MaxBackoffSeconds:       1,
		DefaultMaxRetries:       5,
		TaskDispatchTimeoutSecs: 5,
	}
	return database, cfg, queues.NewRepository(database.Conn()), tasks.NewRepository(database.Conn())
}

func startWorker(t *testing.T, database *db.DB, cfg *config.Config) {
	t.Helper()
	w := New(database, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	t.Cleanup(func() { cancel(); w.Stop() })
}

func addTask(t *testing.T, repo *tasks.Repository, queueID, id, url string, headers map[string]string) *tasks.Task {
	t.Helper()
	now := time.Now().Add(-time.Second)
	tk := &tasks.Task{
		Name: queueID + "/tasks/" + id, QueueID: queueID, HTTPMethod: "POST", URL: url,
		Headers: headers, Body: []byte(`{}`), ScheduleTime: now, NextAttemptAt: now,
		Status: tasks.StatusPending, MaxRetries: 5, DispatchDeadline: 5,
	}
	if err := repo.Create(tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRunRecoversTasksStuckRunning(t *testing.T) {
	database, cfg, qr, tr := newEnv(t)
	q := &queues.Queue{Project: "p", Location: "l", Name: "q"}
	qr.Create(q)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	tk := addTask(t, tr, q.ID, "stuck", srv.URL, nil)
	// Simulate a crash mid-dispatch: claimed but never finished.
	if ok, _ := tr.Claim(tk.Name); !ok {
		t.Fatal("claim")
	}

	startWorker(t, database, cfg)
	waitFor(t, "stuck task to complete", func() bool {
		got, _ := tr.Get(tk.Name)
		return got.Status == tasks.StatusCompleted
	})
}

func TestPausedQueueIsNotDispatchedUntilResumed(t *testing.T) {
	database, cfg, qr, tr := newEnv(t)
	q := &queues.Queue{Project: "p", Location: "l", Name: "q"}
	qr.Create(q)
	qr.SetState(q.ID, queues.StatePaused)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	tk := addTask(t, tr, q.ID, "a", srv.URL, nil)

	startWorker(t, database, cfg)
	time.Sleep(300 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatal("paused queue was dispatched")
	}
	qr.SetState(q.ID, queues.StateRunning)
	waitFor(t, "dispatch after resume", func() bool {
		got, _ := tr.Get(tk.Name)
		return got.Status == tasks.StatusCompleted
	})
}

func TestMaxConcurrentDispatchesEnforced(t *testing.T) {
	database, cfg, qr, tr := newEnv(t)
	q := &queues.Queue{Project: "p", Location: "l", Name: "q",
		RateLimits: &queues.RateLimits{MaxConcurrentDispatches: 2, MaxDispatchesPerSecond: 1000}}
	qr.Create(q)

	var cur, peak, done atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(80 * time.Millisecond)
		cur.Add(-1)
		done.Add(1)
	}))
	defer srv.Close()
	for i := 0; i < 6; i++ {
		addTask(t, tr, q.ID, fmt.Sprintf("t%d", i), srv.URL, nil)
	}

	startWorker(t, database, cfg)
	waitFor(t, "all tasks", func() bool { return done.Load() == 6 })
	if peak.Load() > 2 {
		t.Fatalf("peak concurrency %d exceeds maxConcurrentDispatches 2", peak.Load())
	}
}

func TestMaxDispatchesPerSecondEnforced(t *testing.T) {
	database, cfg, qr, tr := newEnv(t)
	q := &queues.Queue{Project: "p", Location: "l", Name: "q",
		RateLimits: &queues.RateLimits{MaxDispatchesPerSecond: 2, MaxConcurrentDispatches: 100}}
	qr.Create(q)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	for i := 0; i < 20; i++ {
		addTask(t, tr, q.ID, fmt.Sprintf("t%d", i), srv.URL, nil)
	}

	startWorker(t, database, cfg)
	time.Sleep(1100 * time.Millisecond)
	// Burst of 2 plus ~2 refilled in the first second; unlimited would be 20.
	if n := hits.Load(); n > 5 {
		t.Fatalf("%d dispatches in ~1s with maxDispatchesPerSecond=2", n)
	}
	if hits.Load() == 0 {
		t.Fatal("nothing dispatched")
	}
}

func TestDispatchKeepsCallerContentTypeAndSendsCloudTasksHeaders(t *testing.T) {
	database, cfg, qr, tr := newEnv(t)
	q := &queues.Queue{Project: "p", Location: "l", Name: "q"}
	qr.Create(q)
	got := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got <- r.Header }))
	defer srv.Close()
	addTask(t, tr, q.ID, "abc", srv.URL, map[string]string{"content-type": "text/plain", "X-Custom": "1"})

	startWorker(t, database, cfg)
	select {
	case h := <-got:
		if h.Get("Content-Type") != "text/plain" {
			t.Errorf("Content-Type = %q, want text/plain", h.Get("Content-Type"))
		}
		if h.Get("X-Custom") != "1" {
			t.Error("custom header lost")
		}
		if h.Get("X-CloudTasks-QueueName") != "q" || h.Get("X-CloudTasks-TaskName") != "abc" || h.Get("X-CloudTasks-TaskRetryCount") != "0" {
			t.Errorf("cloud tasks headers = %v", h)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("not dispatched")
	}
}

func TestRetriesThenFailsAndRecordsError(t *testing.T) {
	database, cfg, qr, tr := newEnv(t)
	q := &queues.Queue{Project: "p", Location: "l", Name: "q"}
	qr.Create(q)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("kaboom"))
	}))
	defer srv.Close()
	tk := addTask(t, tr, q.ID, "a", srv.URL, nil)
	database.Conn().Exec("UPDATE tasks SET max_retries = 1 WHERE id = ?", tk.Name)

	startWorker(t, database, cfg)
	waitFor(t, "task to fail", func() bool {
		got, _ := tr.Get(tk.Name)
		return got.Status == tasks.StatusFailed
	})
	// max_retries=1: the first attempt plus one retry.
	if hits.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", hits.Load())
	}
	got, _ := tr.Get(tk.Name)
	if got.Attempts() != 2 || !strings.Contains(got.LastError, "500") || !strings.Contains(got.LastError, "kaboom") {
		t.Fatalf("task = %+v", got)
	}
}
