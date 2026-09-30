package queues

import (
	"database/sql"

	"cloud-tasks-emulator/internal/db"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

const selectCols = `id, project, location, name, rate_limit, max_concurrent_dispatches, state, created_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanQueue(s scanner) (*Queue, error) {
	var q Queue
	var rateLimit, maxConcurrent int
	if err := s.Scan(&q.ID, &q.Project, &q.Location, &q.Name, &rateLimit, &maxConcurrent, &q.State, &q.CreatedAt); err != nil {
		return nil, err
	}
	q.RateLimits = &RateLimits{
		MaxDispatchesPerSecond:  rateLimit,
		MaxConcurrentDispatches: maxConcurrent,
	}
	return &q, nil
}

// Create inserts the queue. It returns ErrAlreadyExists if the ID is taken.
func (r *Repository) Create(q *Queue) error {
	q.ID = q.ResourceName(q.Project, q.Location)
	if q.State == "" {
		q.State = StateRunning
	}
	rateLimit := DefaultMaxDispatchesPerSecond
	maxConcurrent := DefaultMaxConcurrentDispatches
	if q.RateLimits != nil {
		if q.RateLimits.MaxDispatchesPerSecond > 0 {
			rateLimit = q.RateLimits.MaxDispatchesPerSecond
		}
		if q.RateLimits.MaxConcurrentDispatches > 0 {
			maxConcurrent = q.RateLimits.MaxConcurrentDispatches
		}
	}
	_, err := r.db.Exec(
		`INSERT INTO queues (id, project, location, name, rate_limit, max_concurrent_dispatches, state)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		q.ID, q.Project, q.Location, q.Name, rateLimit, maxConcurrent, q.State,
	)
	if db.IsUniqueViolation(err) {
		return ErrAlreadyExists
	}
	if err == nil {
		q.RateLimits = &RateLimits{MaxDispatchesPerSecond: rateLimit, MaxConcurrentDispatches: maxConcurrent}
	}
	return err
}

func (r *Repository) Get(id string) (*Queue, error) {
	q, err := scanQueue(r.db.QueryRow(`SELECT `+selectCols+` FROM queues WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return q, err
}

func (r *Repository) query(where string, args ...any) ([]*Queue, error) {
	rows, err := r.db.Query(`SELECT `+selectCols+` FROM queues `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*Queue
	for rows.Next() {
		q, err := scanQueue(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, q)
	}
	return result, rows.Err()
}

func (r *Repository) ListAll() ([]*Queue, error) {
	return r.query(`ORDER BY project, location, name`)
}

func (r *Repository) List(project, location string) ([]*Queue, error) {
	return r.query(`WHERE project = ? AND location = ? ORDER BY name`, project, location)
}

// Delete removes the queue and all of its tasks.
func (r *Repository) Delete(id string) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec("DELETE FROM queues WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec("DELETE FROM tasks WHERE queue_id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetState pauses or resumes a queue.
func (r *Repository) SetState(id, state string) error {
	res, err := r.db.Exec("UPDATE queues SET state = ? WHERE id = ?", state, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Purge deletes every task in the queue and returns how many were removed.
func (r *Repository) Purge(id string) (int64, error) {
	q, err := r.Get(id)
	if err != nil {
		return 0, err
	}
	if q == nil {
		return 0, ErrNotFound
	}
	res, err := r.db.Exec("DELETE FROM tasks WHERE queue_id = ?", id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UpdateRateLimits sets the given (non-zero) limits.
func (r *Repository) UpdateRateLimits(id string, rl RateLimits) error {
	res, err := r.db.Exec(
		`UPDATE queues SET
			rate_limit = CASE WHEN ? > 0 THEN ? ELSE rate_limit END,
			max_concurrent_dispatches = CASE WHEN ? > 0 THEN ? ELSE max_concurrent_dispatches END
		 WHERE id = ?`,
		rl.MaxDispatchesPerSecond, rl.MaxDispatchesPerSecond,
		rl.MaxConcurrentDispatches, rl.MaxConcurrentDispatches, id,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
