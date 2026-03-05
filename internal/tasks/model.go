package tasks

import (
	"encoding/json"
	"time"
)

const (
	StatusPending   = "PENDING"
	StatusRunning   = "RUNNING"
	StatusCompleted = "COMPLETED"
	StatusFailed    = "FAILED"
)

type Task struct {
	ID               string            `json:"-"`
	Name             string            `json:"name"`
	QueueID          string            `json:"-"`
	HTTPMethod       string            `json:"-"`
	URL              string            `json:"-"`
	Headers          map[string]string `json:"-"`
	Body             []byte            `json:"-"`
	ScheduleTime     time.Time         `json:"-"`
	DispatchDeadline int               `json:"-"`
	Status           string            `json:"-"`
	RetryCount       int               `json:"-"`
	MaxRetries       int               `json:"-"`
	NextAttemptAt    time.Time         `json:"-"`
	LastError        string            `json:"-"`
	CreatedAt        time.Time         `json:"-"`
}

func (t *Task) ResourceName() string {
	return t.Name
}

func (t *Task) HeadersJSON() string {
	if t.Headers == nil {
		return "{}"
	}
	b, _ := json.Marshal(t.Headers)
	return string(b)
}
