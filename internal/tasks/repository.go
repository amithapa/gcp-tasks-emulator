package tasks

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// sqliteDatetimeFormat matches SQLite datetime('now') for correct comparison.
const sqliteDatetimeFormat = "2006-01-02 15:04:05"

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

func (r *Repository) Create(t *Task) error {
	t.ID = t.Name
	headersJSON := t.HeadersJSON()
	_, err := r.db.Exec(
		`INSERT INTO tasks (id, queue_id, http_method, url, headers, body, schedule_time,
			dispatch_deadline, status, retry_count, max_retries, next_attempt_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.QueueID, t.HTTPMethod, t.URL, headersJSON, t.Body,
		t.ScheduleTime.UTC().Format(sqliteDatetimeFormat),
		t.DispatchDeadline, t.Status, t.RetryCount, t.MaxRetries,
		t.NextAttemptAt.UTC().Format(sqliteDatetimeFormat),
	)
	return err
}

func (r *Repository) Get(id string) (*Task, error) {
	var t Task
	var headersJSON []byte
	var scheduleTime, nextAttemptAt, createdAt string
	var lastError sql.NullString
	err := r.db.QueryRow(
		`SELECT id, queue_id, http_method, url, headers, body, schedule_time, dispatch_deadline,
			status, retry_count, max_retries, next_attempt_at, last_error, created_at
		 FROM tasks WHERE id = ?`,
		id,
	).Scan(&t.ID, &t.QueueID, &t.HTTPMethod, &t.URL, &headersJSON, &t.Body,
		&scheduleTime, &t.DispatchDeadline, &t.Status, &t.RetryCount, &t.MaxRetries,
		&nextAttemptAt, &lastError, &createdAt)
	t.LastError = lastError.String
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Name = t.ID
	t.ScheduleTime, _ = parseSQLiteTime(scheduleTime)
	t.NextAttemptAt, _ = parseSQLiteTime(nextAttemptAt)
	t.CreatedAt, _ = parseSQLiteTime(createdAt)
	if len(headersJSON) > 0 {
		_ = json.Unmarshal(headersJSON, &t.Headers)
	}
	return &t, nil
}

func (r *Repository) List(queueID string, statusFilter string) ([]*Task, error) {
	query := `SELECT id, queue_id, http_method, url, headers, body, schedule_time, dispatch_deadline,
		status, retry_count, max_retries, next_attempt_at, last_error, created_at
		FROM tasks WHERE queue_id = ?`
	args := []interface{}{queueID}
	if statusFilter != "" {
		query += " AND status = ?"
		args = append(args, statusFilter)
	}
	query += " ORDER BY created_at DESC"

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return r.scanTasks(rows)
}

func (r *Repository) ListPending(limit int) ([]*Task, error) {
	rows, err := r.db.Query(
		`SELECT id, queue_id, http_method, url, headers, body, schedule_time, dispatch_deadline,
			status, retry_count, max_retries, next_attempt_at, last_error, created_at
		 FROM tasks WHERE status = ? AND next_attempt_at <= datetime('now')
		 ORDER BY next_attempt_at ASC LIMIT ?`,
		StatusPending, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return r.scanTasks(rows)
}

func (r *Repository) scanTasks(rows *sql.Rows) ([]*Task, error) {
	var result []*Task
	for rows.Next() {
		var t Task
		var headersJSON []byte
		var scheduleTime, nextAttemptAt, createdAt string
		var lastError sql.NullString
		if err := rows.Scan(&t.ID, &t.QueueID, &t.HTTPMethod, &t.URL, &headersJSON, &t.Body,
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
		result = append(result, &t)
	}
	return result, rows.Err()
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
		StatusPending, retryCount, nextAttemptAt.UTC().Format(sqliteDatetimeFormat), lastError, id,
	)
	return err
}

func (r *Repository) Delete(id string) error {
	res, err := r.db.Exec("DELETE FROM tasks WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task not found: %s", id)
	}
	return nil
}

func (r *Repository) SetNextAttemptNow(id string) error {
	_, err := r.db.Exec(
		"UPDATE tasks SET next_attempt_at = datetime('now') WHERE id = ?",
		id,
	)
	return err
}

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
