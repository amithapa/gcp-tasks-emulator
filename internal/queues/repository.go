package queues

import (
	"database/sql"
	"fmt"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(q *Queue) error {
	q.ID = q.ResourceName(q.Project, q.Location)
	rateLimit := 10
	maxConcurrent := 5
	if q.RateLimits != nil {
		if q.RateLimits.MaxDispatchesPerSecond > 0 {
			rateLimit = q.RateLimits.MaxDispatchesPerSecond
		}
		if q.RateLimits.MaxConcurrentDispatches > 0 {
			maxConcurrent = q.RateLimits.MaxConcurrentDispatches
		}
	}
	_, err := r.db.Exec(
		`INSERT INTO queues (id, project, location, name, rate_limit, max_concurrent_dispatches)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		q.ID, q.Project, q.Location, q.Name, rateLimit, maxConcurrent,
	)
	return err
}

func (r *Repository) Get(id string) (*Queue, error) {
	var q Queue
	var rateLimit, maxConcurrent int
	err := r.db.QueryRow(
		`SELECT id, project, location, name, rate_limit, max_concurrent_dispatches, created_at
		 FROM queues WHERE id = ?`,
		id,
	).Scan(&q.ID, &q.Project, &q.Location, &q.Name, &rateLimit, &maxConcurrent, &q.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	q.RateLimits = &RateLimits{
		MaxDispatchesPerSecond:  rateLimit,
		MaxConcurrentDispatches: maxConcurrent,
	}
	return &q, nil
}

func (r *Repository) ListAll() ([]*Queue, error) {
	rows, err := r.db.Query(
		`SELECT id, project, location, name, rate_limit, max_concurrent_dispatches, created_at
		 FROM queues ORDER BY project, location, name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*Queue
	for rows.Next() {
		var q Queue
		var rateLimit, maxConcurrent int
		if err := rows.Scan(&q.ID, &q.Project, &q.Location, &q.Name, &rateLimit, &maxConcurrent, &q.CreatedAt); err != nil {
			return nil, err
		}
		q.RateLimits = &RateLimits{
			MaxDispatchesPerSecond:  rateLimit,
			MaxConcurrentDispatches: maxConcurrent,
		}
		result = append(result, &q)
	}
	return result, rows.Err()
}

func (r *Repository) List(project, location string) ([]*Queue, error) {
	rows, err := r.db.Query(
		`SELECT id, project, location, name, rate_limit, max_concurrent_dispatches, created_at
		 FROM queues WHERE project = ? AND location = ? ORDER BY name`,
		project, location,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*Queue
	for rows.Next() {
		var q Queue
		var rateLimit, maxConcurrent int
		if err := rows.Scan(&q.ID, &q.Project, &q.Location, &q.Name, &rateLimit, &maxConcurrent, &q.CreatedAt); err != nil {
			return nil, err
		}
		q.RateLimits = &RateLimits{
			MaxDispatchesPerSecond:  rateLimit,
			MaxConcurrentDispatches: maxConcurrent,
		}
		result = append(result, &q)
	}
	return result, rows.Err()
}

func (r *Repository) Delete(id string) error {
	res, err := r.db.Exec("DELETE FROM queues WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("queue not found: %s", id)
	}
	return nil
}
