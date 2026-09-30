package scheduler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"cloud-tasks-emulator/internal/auth"
)

// google.rpc.Code values used for last-attempt status.
const (
	codeOK               = 0
	codeUnknown          = 2
	codeInvalidArgument  = 3
	codeDeadlineExceeded = 4
	codeNotFound         = 5
	codePermissionDenied = 7
	codeResourceExhaust  = 8
	codeUnimplemented    = 12
	codeInternal         = 13
	codeUnavailable      = 14
	codeUnauthenticated  = 16
)

func httpStatusToCode(status int) int {
	switch {
	case status >= 200 && status < 300:
		return codeOK
	case status == 400:
		return codeInvalidArgument
	case status == 401:
		return codeUnauthenticated
	case status == 403:
		return codePermissionDenied
	case status == 404:
		return codeNotFound
	case status == 429:
		return codeResourceExhaust
	case status == 501:
		return codeUnimplemented
	case status == 503:
		return codeUnavailable
	case status == 504:
		return codeDeadlineExceeded
	case status >= 500:
		return codeInternal
	}
	return codeUnknown
}

// Dispatcher performs the HTTP requests for jobs, applying the retry config
// and recording the outcome. At most one execution per job runs at a time.
type Dispatcher struct {
	repo   *Repository
	client *http.Client

	mu      sync.Mutex
	running map[string]bool
}

func NewDispatcher(repo *Repository) *Dispatcher {
	return &Dispatcher{
		repo:    repo,
		client:  &http.Client{},
		running: make(map[string]bool),
	}
}

func (d *Dispatcher) acquire(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running[id] {
		return false
	}
	d.running[id] = true
	return true
}

func (d *Dispatcher) release(id string) {
	d.mu.Lock()
	delete(d.running, id)
	d.mu.Unlock()
}

// Fire executes the job (with retries) and blocks until it finishes. It
// returns false if the job was skipped because a run is already in flight.
// scheduled is the time the run was due, sent as X-CloudScheduler-ScheduleTime.
func (d *Dispatcher) Fire(ctx context.Context, j *Job, scheduled time.Time) bool {
	if !d.acquire(j.ID) {
		slog.Info("scheduler: skipping job, previous run still in flight", "job", j.ID)
		return false
	}
	defer d.release(j.ID)

	start := time.Now()
	attempt := 0
	for {
		attempt++
		at := time.Now()
		code, msg := d.attempt(ctx, j, scheduled, attempt)
		if code == codeOK {
			_ = d.repo.RecordAttempt(j.ID, at, "OK", codeOK, "")
			slog.Info("scheduler: job succeeded", "job", j.ID, "attempt", attempt)
			return true
		}
		_ = d.repo.RecordAttempt(j.ID, at, "FAILED", code, msg)
		slog.Warn("scheduler: job attempt failed", "job", j.ID, "attempt", attempt, "error", msg)

		if attempt > j.Retry.RetryCount || ctx.Err() != nil {
			return true
		}
		wait := j.Retry.Backoff(attempt)
		if j.Retry.MaxRetryDuration > 0 && time.Since(start)+wait > j.Retry.MaxRetryDuration {
			return true
		}
		select {
		case <-ctx.Done():
			return true
		case <-time.After(wait):
		}
	}
}

func (d *Dispatcher) attempt(ctx context.Context, j *Job, scheduled time.Time, n int) (int, string) {
	actx, cancel := context.WithTimeout(ctx, j.AttemptDeadline)
	defer cancel()

	var body io.Reader
	if len(j.Body) > 0 {
		body = bytes.NewReader(j.Body)
	}
	req, err := http.NewRequestWithContext(actx, j.HTTPMethod, j.URL, body)
	if err != nil {
		return codeInvalidArgument, err.Error()
	}
	for k, v := range j.Headers {
		req.Header.Set(k, v)
	}
	auth.Apply(req)
	if len(j.Body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Google-Cloud-Scheduler")
	}
	req.Header.Set("X-CloudScheduler", "true")
	req.Header.Set("X-CloudScheduler-JobName", j.Name)
	req.Header.Set("X-CloudScheduler-ScheduleTime", scheduled.UTC().Format(time.RFC3339))
	req.Header.Set("X-CloudScheduler-AttemptNumber", fmt.Sprint(n))

	resp, err := d.client.Do(req)
	if err != nil {
		if actx.Err() == context.DeadlineExceeded {
			return codeDeadlineExceeded, "attempt deadline exceeded"
		}
		if ue, ok := err.(*url.Error); ok {
			err = ue.Err
		}
		return codeUnavailable, err.Error()
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if code := httpStatusToCode(resp.StatusCode); code != codeOK {
		return code, fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return codeOK, ""
}
