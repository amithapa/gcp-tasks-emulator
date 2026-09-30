package tasks

import (
	"database/sql"
	"encoding/json"
	"time"

	"cloud-tasks-emulator/internal/db"
)

// sqliteDatetimeFormat is a UTC, millisecond-precision layout that sorts
// lexicographically and is compatible with rows written by older versions
// (which used whole seconds).
const sqliteDatetimeFormat = "2006-01-02 15:04:05.000"

func formatTime(t time.Time) string { return t.UTC().Format(sqliteDatetimeFormat) }

func parseSQLiteTime(s string) (time.Time, error) {
	// SQLite "YYYY-MM-DD HH:MM:SS" or "YYYY-MM-DD HH:MM:SS.SSS" (UTC)
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Parse(time.RFC3339, s)
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// taskCols must match the Scan order in scanTask; the table alias is "t".
const taskCols = `t.id, t.queue_id, t.http_method, t.url, t.headers, t.body, t.schedule_time,
	t.dispatch_deadline, t.status, t.retry_count, t.max_retries, t.next_attempt_at,
	t.last_error, t.created_at`

// Create inserts the task. It returns ErrAlreadyExists if the name is taken.
func (r *Repository) Create(t *Task) error {
	t.ID = t.Name
	_, err := r.db.Exec(
		`INSERT INTO tasks (id, queue_id, http_method, url, headers, body, schedule_time,
			dispatch_deadline, status, retry_count, max_retries, next_attempt_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.QueueID, t.HTTPMethod, t.URL, t.HeadersJSON(), t.Body,
		formatTime(t.ScheduleTime),
		t.DispatchDeadline, t.Status, t.RetryCount, t.MaxRetries,
		formatTime(t.NextAttemptAt),
	)
	if db.IsUniqueViolation(err) {
		return ErrAlreadyExists
	}
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTask(row rowScanner) (*Task, error) {
	var t Task
	var headersJSON []byte
	var scheduleTime, nextAttemptAt, createdAt string
	var lastError sql.NullString
	if err := row.Scan(&t.ID, &t.QueueID, &t.HTTPMethod, &t.URL, &headersJSON, &t.Body,
		&scheduleTime, &t.DispatchDeadline, &t.Status, &t.RetryCount, &t.MaxRetries,
		&nextAttemptAt, &lastError, &createdAt); err != nil {
		return nil, err
	}
	t.LastError = lastError.String
	t.Name = t.ID
	t.ScheduleTime, _ = parseSQLiteTime(scheduleTime)
	t.NextAttemptAt, _ = parseSQLiteTime(nextAttemptAt)
	t.CreatedAt, _ = parseSQLiteTime(createdAt)
	if len(headersJSON) > 0 {
		_ = json.Unmarshal(headersJSON, &t.Headers)
	}
	return &t, nil
}

func (r *Repository) Get(id string) (*Task, error) {
	t, err := scanTask(r.db.QueryRow(`SELECT `+taskCols+` FROM tasks t WHERE t.id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func (r *Repository) query(q string, args ...any) ([]*Task, error) {
	rows, err := r.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

// List returns every task in the queue, newest first.
func (r *Repository) List(queueID string, statusFilter string) ([]*Task, error) {
	return r.ListPage(queueID, statusFilter, -1, 0)
}

// ListPage returns tasks newest first. limit < 0 means no limit.
func (r *Repository) ListPage(queueID, statusFilter string, limit, offset int) ([]*Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks t WHERE t.queue_id = ?`
	args := []any{queueID}
	if statusFilter != "" {
		q += " AND t.status = ?"
		args = append(args, statusFilter)
	}
	q += " ORDER BY t.created_at DESC, t.rowid DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	return r.query(q, args...)
}

// Count returns the number of tasks in the queue, optionally filtered by status.
func (r *Repository) Count(queueID, statusFilter string) (int, error) {
	q := "SELECT COUNT(*) FROM tasks WHERE queue_id = ?"
	args := []any{queueID}
	if statusFilter != "" {
		q += " AND status = ?"
		args = append(args, statusFilter)
	}
	var n int
	err := r.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// CountsByQueue returns task counts per queue id and status.
func (r *Repository) CountsByQueue() (map[string]map[string]int, error) {
	rows, err := r.db.Query("SELECT queue_id, status, COUNT(*) FROM tasks GROUP BY queue_id, status")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var qid, st string
		var n int
		if err := rows.Scan(&qid, &st, &n); err != nil {
			return nil, err
		}
		if out[qid] == nil {
			out[qid] = map[string]int{}
		}
		out[qid][st] = n
	}
	return out, rows.Err()
}

// ListPending returns due PENDING tasks of queues that are not paused.
func (r *Repository) ListPending(limit int) ([]*Task, error) {
	return r.query(
		`SELECT `+taskCols+` FROM tasks t JOIN queues q ON q.id = t.queue_id
		 WHERE t.status = ? AND t.next_attempt_at <= ? AND q.state = 'RUNNING'
		 ORDER BY t.next_attempt_at ASC, t.rowid ASC LIMIT ?`,
		StatusPending, formatTime(time.Now()), limit,
	)
}

// ListPendingForQueue returns up to limit due PENDING tasks of one queue.
func (r *Repository) ListPendingForQueue(queueID string, limit int) ([]*Task, error) {
	return r.query(
		`SELECT `+taskCols+` FROM tasks t
		 WHERE t.queue_id = ? AND t.status = ? AND t.next_attempt_at <= ?
		 ORDER BY t.next_attempt_at ASC, t.rowid ASC LIMIT ?`,
		queueID, StatusPending, formatTime(time.Now()), limit,
	)
}

func (r *Repository) UpdateStatus(id, status, lastError string) error {
	_, err := r.db.Exec(
		"UPDATE tasks SET status = ?, last_error = ? WHERE id = ?",
		status, lastError, id,
	)
	return err
}

func (r *Repository) UpdateRetry(id string, retryCount int, nextAttemptAt time.Time, lastError string) error {
	_, err := r.db.Exec(
		"UPDATE tasks SET status = ?, retry_count = ?, next_attempt_at = ?, last_error = ? WHERE id = ?",
		StatusPending, retryCount, formatTime(nextAttemptAt), lastError, id,
	)
	return err
}

func (r *Repository) Delete(id string) error {
	res, err := r.db.Exec("DELETE FROM tasks WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetNextAttemptNow makes the task due immediately. FAILED and COMPLETED tasks
// are re-queued from scratch (status PENDING, retry count reset); RUNNING
// tasks are left alone. It returns ErrNotFound for unknown tasks.
func (r *Repository) SetNextAttemptNow(id string) error {
	res, err := r.db.Exec(
		`UPDATE tasks SET
			next_attempt_at = CASE WHEN status = 'RUNNING' THEN next_attempt_at ELSE ? END,
			retry_count = CASE WHEN status IN ('FAILED', 'COMPLETED') THEN 0 ELSE retry_count END,
			last_error = CASE WHEN status IN ('FAILED', 'COMPLETED') THEN NULL ELSE last_error END,
			status = CASE WHEN status IN ('FAILED', 'COMPLETED') THEN 'PENDING' ELSE status END
		 WHERE id = ?`,
		formatTime(time.Now()), id,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Claim atomically moves a PENDING task to RUNNING.
func (r *Repository) Claim(id string) (bool, error) {
	res, err := r.db.Exec(
		"UPDATE tasks SET status = ? WHERE id = ? AND status = ?",
		StatusRunning, id, StatusPending,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RecoverRunning returns tasks left RUNNING by a crashed or stopped process to
// PENDING so they are dispatched again. It returns the number recovered.
func (r *Repository) RecoverRunning() (int64, error) {
	res, err := r.db.Exec("UPDATE tasks SET status = ? WHERE status = ?", StatusPending, StatusRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
