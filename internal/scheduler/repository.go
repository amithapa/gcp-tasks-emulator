package scheduler

import (
	"database/sql"
	"encoding/json"
	"time"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

const jobColumns = `id, project, location, name, description, schedule, time_zone, http_method, url,
	headers, body, state, attempt_deadline_ms, retry_count, min_backoff_ms, max_backoff_ms,
	max_doublings, max_retry_duration_ms, next_run_at, last_attempt_at, last_status,
	last_status_code, last_error, created_at, updated_at`

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMs(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

type scanner interface{ Scan(dest ...any) error }

func scanJob(s scanner) (*Job, error) {
	var j Job
	var headers sql.NullString
	var body []byte
	var attemptMs, minMs, maxMs, maxRetryMs, nextMs, lastMs, createdMs, updatedMs int64
	var lastStatus, lastError sql.NullString
	err := s.Scan(&j.ID, &j.Project, &j.Location, &j.Name, &j.Description, &j.Schedule, &j.TimeZone,
		&j.HTTPMethod, &j.URL, &headers, &body, &j.State, &attemptMs, &j.Retry.RetryCount, &minMs, &maxMs,
		&j.Retry.MaxDoublings, &maxRetryMs, &nextMs, &lastMs, &lastStatus, &j.LastStatusCode, &lastError,
		&createdMs, &updatedMs)
	if err != nil {
		return nil, err
	}
	if headers.Valid && headers.String != "" {
		_ = json.Unmarshal([]byte(headers.String), &j.Headers)
	}
	j.Body = body
	j.AttemptDeadline = time.Duration(attemptMs) * time.Millisecond
	j.Retry.MinBackoff = time.Duration(minMs) * time.Millisecond
	j.Retry.MaxBackoff = time.Duration(maxMs) * time.Millisecond
	j.Retry.MaxRetryDuration = time.Duration(maxRetryMs) * time.Millisecond
	j.NextRunAt = fromMs(nextMs)
	j.LastAttemptAt = fromMs(lastMs)
	j.LastStatus = lastStatus.String
	j.LastError = lastError.String
	j.CreatedAt = fromMs(createdMs)
	j.UpdatedAt = fromMs(updatedMs)
	return &j, nil
}

func headersJSON(h map[string]string) string {
	if len(h) == 0 {
		return ""
	}
	b, _ := json.Marshal(h)
	return string(b)
}

// Create inserts a new job. The caller is expected to have applied defaults
// and validated it. Returns ErrAlreadyExists if the resource name is taken.
func (r *Repository) Create(j *Job) error {
	j.ID = ResourceName(j.Project, j.Location, j.Name)
	existing, err := r.Get(j.ID)
	if err != nil {
		return err
	}
	if existing != nil {
		return ErrAlreadyExists
	}
	now := time.Now().UTC()
	j.CreatedAt, j.UpdatedAt = now, now
	_, err = r.db.Exec(
		`INSERT INTO jobs (`+jobColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.Project, j.Location, j.Name, j.Description, j.Schedule, j.TimeZone, j.HTTPMethod, j.URL,
		headersJSON(j.Headers), j.Body, j.State, j.AttemptDeadline.Milliseconds(), j.Retry.RetryCount,
		j.Retry.MinBackoff.Milliseconds(), j.Retry.MaxBackoff.Milliseconds(), j.Retry.MaxDoublings,
		j.Retry.MaxRetryDuration.Milliseconds(), ms(j.NextRunAt), ms(j.LastAttemptAt), j.LastStatus,
		j.LastStatusCode, j.LastError, ms(j.CreatedAt), ms(j.UpdatedAt),
	)
	return err
}

// Get returns nil, nil when the job does not exist.
func (r *Repository) Get(id string) (*Job, error) {
	j, err := scanJob(r.db.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

func (r *Repository) query(q string, args ...any) ([]*Job, error) {
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (r *Repository) List(project, location string) ([]*Job, error) {
	return r.query(`SELECT `+jobColumns+` FROM jobs WHERE project = ? AND location = ? ORDER BY name`, project, location)
}

func (r *Repository) ListAll() ([]*Job, error) {
	return r.query(`SELECT ` + jobColumns + ` FROM jobs ORDER BY project, location, name`)
}

// ListDue returns ENABLED jobs whose next_run_at is at or before now.
func (r *Repository) ListDue(now time.Time) ([]*Job, error) {
	return r.query(`SELECT `+jobColumns+` FROM jobs WHERE state = ? AND next_run_at > 0 AND next_run_at <= ? ORDER BY next_run_at`,
		StateEnabled, now.UnixMilli())
}

// Update rewrites the mutable configuration of a job (not attempt bookkeeping).
func (r *Repository) Update(j *Job) error {
	j.UpdatedAt = time.Now().UTC()
	res, err := r.db.Exec(
		`UPDATE jobs SET description=?, schedule=?, time_zone=?, http_method=?, url=?, headers=?, body=?,
		 state=?, attempt_deadline_ms=?, retry_count=?, min_backoff_ms=?, max_backoff_ms=?, max_doublings=?,
		 max_retry_duration_ms=?, next_run_at=?, updated_at=? WHERE id=?`,
		j.Description, j.Schedule, j.TimeZone, j.HTTPMethod, j.URL, headersJSON(j.Headers), j.Body,
		j.State, j.AttemptDeadline.Milliseconds(), j.Retry.RetryCount, j.Retry.MinBackoff.Milliseconds(),
		j.Retry.MaxBackoff.Milliseconds(), j.Retry.MaxDoublings, j.Retry.MaxRetryDuration.Milliseconds(),
		ms(j.NextRunAt), ms(j.UpdatedAt), j.ID,
	)
	return affected(res, err)
}

// SetState updates state and next_run_at (zero for paused).
func (r *Repository) SetState(id, state string, nextRun time.Time) error {
	res, err := r.db.Exec(`UPDATE jobs SET state=?, next_run_at=?, updated_at=? WHERE id=?`,
		state, ms(nextRun), time.Now().UnixMilli(), id)
	return affected(res, err)
}

// Claim atomically advances next_run_at from oldNext to newNext for an
// ENABLED job. It returns true only for the caller that won the race.
func (r *Repository) Claim(id string, oldNext, newNext time.Time) (bool, error) {
	res, err := r.db.Exec(`UPDATE jobs SET next_run_at=? WHERE id=? AND state=? AND next_run_at=?`,
		ms(newNext), id, StateEnabled, ms(oldNext))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RecordAttempt stores the outcome of the latest attempt. Missing jobs are ignored.
func (r *Repository) RecordAttempt(id string, at time.Time, status string, code int, errMsg string) error {
	_, err := r.db.Exec(`UPDATE jobs SET last_attempt_at=?, last_status=?, last_status_code=?, last_error=? WHERE id=?`,
		ms(at), status, code, errMsg, id)
	return err
}

func (r *Repository) Delete(id string) error {
	res, err := r.db.Exec(`DELETE FROM jobs WHERE id = ?`, id)
	return affected(res, err)
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
