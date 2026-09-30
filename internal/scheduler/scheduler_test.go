package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cloud-tasks-emulator/internal/db"
)

func testConn(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.New(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d.Conn()
}

func newJob(name, url string) *Job {
	return &Job{Project: "p", Location: "l", Name: name, Schedule: "* * * * *", URL: url}
}

func TestNextRunTimeZoneAndDST(t *testing.T) {
	from := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	next, err := NextRun("0 9 * * *", "America/New_York", from)
	if err != nil {
		t.Fatal(err)
	}
	// 09:00 EST == 14:00 UTC
	if want := time.Date(2024, 1, 15, 14, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("winter: got %s want %s", next, want)
	}
	// After spring-forward (Mar 10 2024) 09:00 EDT == 13:00 UTC.
	// Crossing spring-forward (Mar 10 2024): local 09:00 stays, UTC shifts 14:00 -> 13:00.
	from = time.Date(2024, 3, 9, 15, 0, 0, 0, time.UTC)
	next, _ = NextRun("0 9 * * *", "America/New_York", from)
	if want := time.Date(2024, 3, 10, 13, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("dst: got %s want %s", next, want)
	}
	next2, _ := NextRun("0 9 * * *", "America/New_York", next)
	if want := time.Date(2024, 3, 11, 13, 0, 0, 0, time.UTC); !next2.Equal(want) {
		t.Errorf("dst+1: got %s want %s", next2, want)
	}
	// Default tz is UTC.
	next, _ = NextRun("30 * * * *", "", time.Date(2024, 1, 1, 0, 45, 0, 0, time.UTC))
	if want := time.Date(2024, 1, 1, 1, 30, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("utc: got %s want %s", next, want)
	}
}

func TestNextRunDescriptors(t *testing.T) {
	from := time.Date(2024, 1, 1, 10, 30, 0, 0, time.UTC)
	if n, err := NextRun("@every 5s", "", from); err != nil || n.Sub(from) != 5*time.Second {
		t.Errorf("@every: %v %v", n, err)
	}
	if n, err := NextRun("@daily", "", from); err != nil || !n.Equal(time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("@daily: %v %v", n, err)
	}
}

func TestParseScheduleErrors(t *testing.T) {
	for _, c := range [][2]string{
		{"", "UTC"}, {"not a cron", "UTC"}, {"* * * * *", "Mars/Olympus"}, {"CRON_TZ=UTC * * * * *", "UTC"}, {"61 * * * *", "UTC"},
	} {
		_, err := ParseSchedule(c[0], c[1])
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%v: expected ValidationError, got %v", c, err)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := newJob("a", "http://x.test/y")
	ok.ApplyDefaults()
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(j *Job){
		"bad name":     func(j *Job) { j.Name = "bad name!" },
		"bad url":      func(j *Job) { j.URL = "ftp://x" },
		"no url":       func(j *Job) { j.URL = "" },
		"bad method":   func(j *Job) { j.HTTPMethod = "FETCH" },
		"deadline low": func(j *Job) { j.AttemptDeadline = time.Second },
		"backoff":      func(j *Job) { j.Retry.MinBackoff = time.Hour * 2 },
		"neg retry":    func(j *Job) { j.Retry.RetryCount = -1 },
	}
	for name, mut := range cases {
		j := newJob("a", "http://x.test/y")
		j.ApplyDefaults()
		mut(j)
		var ve *ValidationError
		if err := j.Validate(); !errors.As(err, &ve) {
			t.Errorf("%s: expected ValidationError, got %v", name, err)
		}
	}
}

func TestBackoff(t *testing.T) {
	r := RetryConfig{MinBackoff: time.Second, MaxBackoff: 10 * time.Second, MaxDoublings: 2}
	want := []time.Duration{1, 2, 4, 8, 10, 10}
	for i, w := range want {
		if got := r.Backoff(i + 1); got != w*time.Second {
			t.Errorf("retry %d: got %s want %s", i+1, got, w*time.Second)
		}
	}
}

func TestRepositoryCRUD(t *testing.T) {
	repo := NewRepository(testConn(t))
	j := newJob("job1", "http://x.test/y")
	j.Headers = map[string]string{"X-A": "b"}
	j.Body = []byte("hello")
	j.Retry.RetryCount = 3
	j.ApplyDefaults()
	j.NextRunAt = time.Now().Add(time.Minute)
	if err := repo.Create(j); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(j); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("duplicate: %v", err)
	}
	got, err := repo.Get(j.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.ID != "projects/p/locations/l/jobs/job1" || got.Headers["X-A"] != "b" || string(got.Body) != "hello" ||
		got.Retry.RetryCount != 3 || got.AttemptDeadline != DefaultAttemptDeadline || got.State != StateEnabled {
		t.Errorf("unexpected job %+v", got)
	}
	if !got.NextRunAt.Equal(j.NextRunAt.Truncate(time.Millisecond)) {
		t.Errorf("next run %s vs %s", got.NextRunAt, j.NextRunAt)
	}
	if missing, _ := repo.Get("projects/p/locations/l/jobs/none"); missing != nil {
		t.Error("expected nil for missing")
	}

	other := newJob("job2", "http://x.test/y")
	other.Location = "other"
	other.ApplyDefaults()
	_ = repo.Create(other)
	if l, _ := repo.List("p", "l"); len(l) != 1 {
		t.Errorf("list: %d", len(l))
	}
	if l, _ := repo.ListAll(); len(l) != 2 {
		t.Errorf("listall: %d", len(l))
	}

	got.Description = "new"
	if err := repo.Update(got); err != nil {
		t.Fatal(err)
	}
	if g, _ := repo.Get(j.ID); g.Description != "new" {
		t.Error("update not persisted")
	}
	if err := repo.RecordAttempt(j.ID, time.Now(), "FAILED", 13, "boom"); err != nil {
		t.Fatal(err)
	}
	if g, _ := repo.Get(j.ID); g.LastStatus != "FAILED" || g.LastError != "boom" || g.LastAttemptAt.IsZero() {
		t.Errorf("attempt: %+v", g)
	}
	if err := repo.Delete(j.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(j.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing: %v", err)
	}
}

func TestListDueAndClaim(t *testing.T) {
	repo := NewRepository(testConn(t))
	now := time.Now().UTC().Truncate(time.Millisecond)
	mk := func(name, state string, next time.Time) *Job {
		j := newJob(name, "http://x.test")
		j.ApplyDefaults()
		j.State, j.NextRunAt = state, next
		if err := repo.Create(j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	due := mk("due", StateEnabled, now.Add(-time.Second))
	mk("future", StateEnabled, now.Add(time.Hour))
	mk("paused", StatePaused, time.Time{})
	list, _ := repo.ListDue(now)
	if len(list) != 1 || list[0].Name != "due" {
		t.Fatalf("due: %+v", list)
	}
	next := now.Add(time.Minute)
	if ok, _ := repo.Claim(due.ID, due.NextRunAt, next); !ok {
		t.Fatal("first claim should win")
	}
	if ok, _ := repo.Claim(due.ID, due.NextRunAt, next); ok {
		t.Fatal("second claim must lose")
	}
	if l, _ := repo.ListDue(now); len(l) != 0 {
		t.Error("claimed job still due")
	}
}

func newTestService(t *testing.T) *Service {
	repo := NewRepository(testConn(t))
	return NewService(repo, NewDispatcher(repo))
}

func TestServiceLifecycle(t *testing.T) {
	svc := newTestService(t)
	j, err := svc.Create(newJob("a", "http://x.test"))
	if err != nil || j.NextRunAt.IsZero() {
		t.Fatalf("create: %v %+v", err, j)
	}
	if _, err := svc.Create(newJob("a", "http://x.test")); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("dup: %v", err)
	}
	p, err := svc.Pause(j.ID)
	if err != nil || p.State != StatePaused || !p.NextRunAt.IsZero() {
		t.Fatalf("pause: %v %+v", err, p)
	}
	r, err := svc.Resume(j.ID)
	if err != nil || r.State != StateEnabled || r.NextRunAt.IsZero() {
		t.Fatalf("resume: %v %+v", err, r)
	}
	u, err := svc.Update(j.ID, &Job{Schedule: "@every 10s", Description: "d"}, nil)
	if err != nil || u.Schedule != "@every 10s" || u.Description != "d" {
		t.Fatalf("update: %v %+v", err, u)
	}
	u, err = svc.Update(j.ID, &Job{Schedule: "0 * * * *", Description: "ignored"}, []string{"schedule"})
	if err != nil || u.Description != "d" || u.Schedule != "0 * * * *" {
		t.Fatalf("masked update: %v %+v", err, u)
	}
	var ve *ValidationError
	if _, err := svc.Update(j.ID, &Job{Schedule: "garbage"}, nil); !errors.As(err, &ve) {
		t.Errorf("bad schedule update: %v", err)
	}
	if _, err := svc.Update(j.ID, &Job{}, []string{"bogus"}); !errors.As(err, &ve) {
		t.Errorf("bad mask: %v", err)
	}
	if err := svc.Delete(j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(j.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted: %v", err)
	}
}

func TestRunnerFiresOnceAndAdvances(t *testing.T) {
	var hits atomic.Int32
	got := make(chan *http.Request, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		got <- r
	}))
	defer srv.Close()

	svc := newTestService(t)
	j := newJob("fire", srv.URL+"/hook")
	j.Headers = map[string]string{"X-Custom": "1"}
	j.Body = []byte("payload")
	created, err := svc.Create(j)
	if err != nil {
		t.Fatal(err)
	}
	// Pretend the job was due an hour ago (simulated downtime).
	past := time.Now().Add(-time.Hour)
	if ok, _ := svc.repo.Claim(created.ID, created.NextRunAt, past); !ok {
		t.Fatal("setup claim")
	}

	runner := NewRunner(svc, 10*time.Millisecond)
	runner.Tick(context.Background())
	runner.Tick(context.Background()) // must not fire again
	runner.wg.Wait()

	select {
	case r := <-got:
		if r.Method != "POST" || r.URL.Path != "/hook" || r.Header.Get("X-Custom") != "1" ||
			r.Header.Get("X-CloudScheduler-JobName") != "fire" {
			t.Errorf("bad request: %+v", r.Header)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job did not fire")
	}
	if hits.Load() != 1 {
		t.Errorf("hits=%d, want 1 (no catch-up storm / double fire)", hits.Load())
	}
	after, _ := svc.repo.Get(created.ID)
	if after.LastStatus != "OK" || after.LastAttemptAt.IsZero() {
		t.Errorf("attempt not recorded: %+v", after)
	}
	if !after.NextRunAt.After(time.Now()) {
		t.Errorf("next run not advanced past now: %s", after.NextRunAt)
	}
}

func TestDispatcherRetriesAndRecordsFailure(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	svc := newTestService(t)
	j := newJob("retry", srv.URL)
	j.Retry = RetryConfig{RetryCount: 3, MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	created, err := svc.Create(j)
	if err != nil {
		t.Fatal(err)
	}
	svc.dispatcher.Fire(context.Background(), created, time.Now())
	if hits.Load() != 3 {
		t.Errorf("hits=%d want 3", hits.Load())
	}
	if g, _ := svc.repo.Get(created.ID); g.LastStatus != "OK" {
		t.Errorf("status %q", g.LastStatus)
	}

	// Permanent failure with no retries records the error.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer bad.Close()
	j2, _ := svc.Create(newJob("fail", bad.URL))
	svc.dispatcher.Fire(context.Background(), j2, time.Now())
	g, _ := svc.repo.Get(j2.ID)
	if g.LastStatus != "FAILED" || g.LastStatusCode != codeNotFound || g.LastError == "" {
		t.Errorf("failure not recorded: %+v", g)
	}
}
