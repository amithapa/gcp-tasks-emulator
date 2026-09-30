package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"
)

var (
	ErrNotFound      = errors.New("task not found")
	ErrAlreadyExists = errors.New("task already exists")
)

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,500}$`)

// ValidateID checks a short task id (letters, digits, hyphens, underscores).
func ValidateID(id string) error {
	if !idRe.MatchString(id) {
		return fmt.Errorf("invalid task id %q: use 1-500 letters, numbers, hyphens or underscores", id)
	}
	return nil
}

// ValidateURL checks that raw is an absolute http(s) URL.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid url %q: must be an absolute http or https URL", raw)
	}
	return nil
}

// Attempts returns how many dispatch attempts have been made so far.
func (t *Task) Attempts() int {
	if t.Status == StatusPending {
		return t.RetryCount
	}
	return t.RetryCount + 1
}

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
