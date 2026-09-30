package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Runner polls for due jobs and fires them.
type Runner struct {
	svc      *Service
	interval time.Duration

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewRunner(svc *Service, pollInterval time.Duration) *Runner {
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	return &Runner{svc: svc, interval: pollInterval, stopCh: make(chan struct{})}
}

// Run blocks until ctx is cancelled or Stop is called.
func (r *Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	defer r.wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.Tick(ctx)
		}
	}
}

func (r *Runner) Stop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
}

// Tick fires every job that is due. For each one next_run_at is advanced to
// the first schedule time after now (so downtime never causes a catch-up
// storm) using a compare-and-swap, so a job cannot fire twice.
func (r *Runner) Tick(ctx context.Context) {
	now := r.svc.now()
	due, err := r.svc.repo.ListDue(now)
	if err != nil {
		slog.Error("scheduler: list due jobs failed", "error", err)
		return
	}
	for _, j := range due {
		next, err := NextRun(j.Schedule, j.TimeZone, now)
		if err != nil {
			slog.Error("scheduler: cannot compute next run, pausing job", "job", j.ID, "error", err)
			_ = r.svc.repo.SetState(j.ID, StatePaused, time.Time{})
			continue
		}
		ok, err := r.svc.repo.Claim(j.ID, j.NextRunAt, next)
		if err != nil {
			slog.Error("scheduler: claim failed", "job", j.ID, "error", err)
			continue
		}
		if !ok {
			continue
		}
		slog.Info("scheduler: firing job", "job", j.ID, "next_run", next)
		r.wg.Add(1)
		go func(j *Job) {
			defer r.wg.Done()
			r.svc.dispatcher.Fire(ctx, j, j.NextRunAt)
		}(j)
	}
}
