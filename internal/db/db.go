package db

import (
	"database/sql"
	"log/slog"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	conn *sql.DB
}

func New(path string) (*DB, error) {
	conn, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		return nil, err
	}

	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, err
	}

	return db, nil
}

func (db *DB) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS queues (
			id TEXT PRIMARY KEY,
			project TEXT NOT NULL,
			location TEXT NOT NULL,
			name TEXT NOT NULL,
			rate_limit INTEGER DEFAULT 10,
			max_concurrent_dispatches INTEGER DEFAULT 5,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_queues_project_location ON queues(project, location)`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			queue_id TEXT NOT NULL,
			http_method TEXT DEFAULT 'POST',
			url TEXT NOT NULL,
			headers TEXT,
			body BLOB,
			schedule_time DATETIME NOT NULL,
			dispatch_deadline INTEGER DEFAULT 30,
			status TEXT DEFAULT 'PENDING',
			retry_count INTEGER DEFAULT 0,
			max_retries INTEGER DEFAULT 5,
			next_attempt_at DATETIME NOT NULL,
			last_error TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (queue_id) REFERENCES queues(id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_queue_status ON tasks(queue_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_next_attempt ON tasks(next_attempt_at) WHERE status = 'PENDING'`,
		`CREATE INDEX IF NOT EXISTS idx_tasks_status_next ON tasks(status, next_attempt_at)`,
	}

	for _, m := range migrations {
		if _, err := db.conn.Exec(m); err != nil {
			slog.Error("migration failed", "error", err, "sql", m)
			return err
		}
	}
	return nil
}

func (db *DB) Conn() *sql.DB {
	return db.conn
}

func (db *DB) Close() error {
	return db.conn.Close()
}
