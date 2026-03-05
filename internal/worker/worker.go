package worker

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"time"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/tasks"
)

type Worker struct {
	db     *db.DB
	cfg    *config.Config
	stopCh chan struct{}
}

func New(database *db.DB, cfg *config.Config) *Worker {
	return &Worker{
		db:     database,
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
}

func (w *Worker) Run(ctx context.Context) {
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
	close(w.stopCh)
}

func (w *Worker) pollAndDispatch(sem chan struct{}) {
	repo := tasks.NewRepository(w.db.Conn())
	pending, err := repo.ListPending(w.cfg.WorkerConcurrency)
	if err != nil {
		slog.Error("failed to list pending tasks", "error", err)
		return
	}

	for _, t := range pending {
		select {
		case sem <- struct{}{}:
			go func(task *tasks.Task) {
				defer func() { <-sem }()
				w.dispatch(task)
			}(t)
		default:
			return
		}
	}
}

func (w *Worker) dispatch(t *tasks.Task) {
	repo := tasks.NewRepository(w.db.Conn())

	claimed, err := repo.Claim(t.ID)
	if err != nil {
		slog.Error("failed to claim task", "task", t.ID, "error", err)
		return
	}
	if !claimed {
		return
	}

	timeout := time.Duration(t.DispatchDeadline) * time.Second
	if timeout == 0 {
		timeout = time.Duration(w.cfg.TaskDispatchTimeoutSecs) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, t.HTTPMethod, t.URL, bytes.NewReader(t.Body))
	if err != nil {
		w.failTask(repo, t, err.Error())
		return
	}

	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")

	slog.Info("dispatching task", "task", t.ID, "method", t.HTTPMethod, "url", t.URL)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("task dispatch failed", "task", t.ID, "error", err)
		w.failTask(repo, t, err.Error())
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := repo.UpdateStatus(t.ID, tasks.StatusCompleted, ""); err != nil {
			slog.Error("failed to mark task completed", "task", t.ID, "error", err)
		}
		slog.Info("task completed", "task", t.ID, "status", resp.StatusCode)
	} else {
		errMsg := "HTTP " + resp.Status
		slog.Warn("task returned non-2xx", "task", t.ID, "status", resp.StatusCode, "status_line", resp.Status)
		w.retryOrFail(repo, t, errMsg)
	}
}

func (w *Worker) failTask(repo *tasks.Repository, t *tasks.Task, errMsg string) {
	if t.RetryCount >= t.MaxRetries {
		repo.UpdateStatus(t.ID, tasks.StatusFailed, errMsg)
		slog.Info("task failed permanently", "task", t.ID, "error", errMsg)
	} else {
		w.retryOrFail(repo, t, errMsg)
	}
}

func (w *Worker) retryOrFail(repo *tasks.Repository, t *tasks.Task, errMsg string) {
	nextRetry := t.RetryCount + 1
	if nextRetry > t.MaxRetries {
		repo.UpdateStatus(t.ID, tasks.StatusFailed, errMsg)
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
