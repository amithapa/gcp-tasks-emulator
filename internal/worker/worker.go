package worker

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloud-tasks-emulator/internal/auth"
	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/queues"
	"cloud-tasks-emulator/internal/tasks"
)

type Worker struct {
	db       *db.DB
	cfg      *config.Config
	stopCh   chan struct{}
	stopOnce sync.Once

	mu       sync.Mutex
	inflight map[string]int     // queue id -> dispatches in flight
	buckets  map[string]*bucket // queue id -> dispatch rate limiter
}

func New(database *db.DB, cfg *config.Config) *Worker {
	return &Worker{
		db:       database,
		cfg:      cfg,
		stopCh:   make(chan struct{}),
		inflight: map[string]int{},
		buckets:  map[string]*bucket{},
	}
}

func (w *Worker) Run(ctx context.Context) {
	// Tasks left RUNNING by a previous process (crash, kill, restart) would
	// otherwise never be dispatched again.
	if n, err := tasks.NewRepository(w.db.Conn()).RecoverRunning(); err != nil {
		slog.Error("failed to recover running tasks", "error", err)
	} else if n > 0 {
		slog.Info("recovered tasks left running by a previous process", "count", n)
	}

	ticker := time.NewTicker(time.Duration(w.cfg.WorkerPollIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	sem := make(chan struct{}, w.cfg.WorkerConcurrency)

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.pollAndDispatch(sem)
		}
	}
}

func (w *Worker) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
}

// bucket is a token bucket enforcing a queue's max dispatches per second.
type bucket struct {
	tokens float64
	last   time.Time
}

func (b *bucket) refill(now time.Time, rate float64) {
	capacity := math.Max(1, rate)
	b.tokens = math.Min(capacity, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
}

func (w *Worker) pollAndDispatch(sem chan struct{}) {
	queueRepo := queues.NewRepository(w.db.Conn())
	repo := tasks.NewRepository(w.db.Conn())
	qs, err := queueRepo.ListAll()
	if err != nil {
		slog.Error("failed to list queues", "error", err)
		return
	}
	now := time.Now()

	for _, q := range qs {
		if q.Paused() {
			continue
		}
		room := cap(sem) - len(sem)
		if room <= 0 {
			return
		}

		// Per-queue limits: max concurrent dispatches and dispatches/second.
		var b *bucket
		limit := room
		w.mu.Lock()
		if mc := q.RateLimits.MaxConcurrentDispatches; mc > 0 {
			limit = min(limit, mc-w.inflight[q.ID])
		}
		if rate := q.RateLimits.MaxDispatchesPerSecond; rate > 0 {
			b = w.buckets[q.ID]
			if b == nil {
				b = &bucket{tokens: math.Max(1, float64(rate)), last: now}
				w.buckets[q.ID] = b
			}
			b.refill(now, float64(rate))
			limit = min(limit, int(b.tokens))
		}
		w.mu.Unlock()
		if limit <= 0 {
			continue
		}

		pending, err := repo.ListPendingForQueue(q.ID, limit)
		if err != nil {
			slog.Error("failed to list pending tasks", "queue", q.ID, "error", err)
			continue
		}
		for _, t := range pending {
			select {
			case sem <- struct{}{}:
			default:
				return
			}
			// Claim here (not in the goroutine) so the next poll cannot
			// re-list the task and so in-flight counts are exact.
			claimed, err := repo.Claim(t.ID)
			if err != nil {
				slog.Error("failed to claim task", "task", t.ID, "error", err)
			}
			if err != nil || !claimed {
				<-sem
				continue
			}
			w.mu.Lock()
			w.inflight[q.ID]++
			if b != nil {
				b.tokens--
			}
			w.mu.Unlock()
			go func(task *tasks.Task) {
				defer func() {
					w.mu.Lock()
					w.inflight[task.QueueID]--
					w.mu.Unlock()
					<-sem
				}()
				w.dispatch(task)
			}(t)
		}
	}
}

// dispatch performs the HTTP request for a task that has already been claimed
// (moved to RUNNING) and records the outcome.
func (w *Worker) dispatch(t *tasks.Task) {
	repo := tasks.NewRepository(w.db.Conn())

	timeout := time.Duration(t.DispatchDeadline) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(w.cfg.TaskDispatchTimeoutSecs) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, t.HTTPMethod, t.URL, bytes.NewReader(t.Body))
	if err != nil {
		w.retryOrFail(repo, t, err.Error())
		return
	}

	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	auth.Apply(req)
	// Headers Cloud Tasks adds to every dispatch.
	req.Header.Set("User-Agent", "Google-Cloud-Tasks")
	req.Header.Set("X-CloudTasks-QueueName", path.Base(t.QueueID))
	req.Header.Set("X-CloudTasks-TaskName", path.Base(t.ID))
	req.Header.Set("X-CloudTasks-TaskRetryCount", strconv.Itoa(t.RetryCount))
	req.Header.Set("X-CloudTasks-TaskExecutionCount", strconv.Itoa(t.RetryCount))
	req.Header.Set("X-CloudTasks-TaskETA", strconv.FormatFloat(float64(t.ScheduleTime.UnixMilli())/1000, 'f', 3, 64))

	slog.Info("dispatching task", "task", t.ID, "method", t.HTTPMethod, "url", t.URL)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("task dispatch failed", "task", t.ID, "error", err)
		w.retryOrFail(repo, t, err.Error())
		return
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := repo.UpdateStatus(t.ID, tasks.StatusCompleted, ""); err != nil {
			slog.Error("failed to mark task completed", "task", t.ID, "error", err)
		}
		slog.Info("task completed", "task", t.ID, "status", resp.StatusCode)
	} else {
		errMsg := "HTTP " + resp.Status
		if msg := strings.TrimSpace(strings.ToValidUTF8(string(snippet), "")); msg != "" {
			errMsg += ": " + msg
		}
		slog.Warn("task returned non-2xx", "task", t.ID, "status", resp.StatusCode, "status_line", resp.Status)
		w.retryOrFail(repo, t, errMsg)
	}
}

func (w *Worker) retryOrFail(repo *tasks.Repository, t *tasks.Task, errMsg string) {
	nextRetry := t.RetryCount + 1
	if nextRetry > t.MaxRetries {
		_ = repo.UpdateStatus(t.ID, tasks.StatusFailed, errMsg)
		slog.Info("task failed permanently", "task", t.ID, "error", errMsg)
		return
	}

	delay := backoff(w.cfg.InitialBackoffSeconds, w.cfg.MaxBackoffSeconds, nextRetry)
	nextAttempt := time.Now().Add(time.Duration(delay) * time.Second)

	if err := repo.UpdateRetry(t.ID, nextRetry, nextAttempt, errMsg); err != nil {
		slog.Error("failed to update retry", "task", t.ID, "error", err)
		return
	}
	slog.Info("task scheduled for retry", "task", t.ID, "retry", nextRetry, "next_attempt", nextAttempt, "error", errMsg)
}

func backoff(initial, max, retryCount int) int {
	delay := initial
	for i := 0; i < retryCount-1; i++ {
		delay *= 2
		if delay > max {
			return max
		}
	}
	return delay
}
