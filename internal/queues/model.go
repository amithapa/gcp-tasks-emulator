package queues

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

const (
	StateRunning = "RUNNING"
	StatePaused  = "PAUSED"
)

// Defaults match Cloud Tasks (500 dispatches/s, 1000 concurrent).
const (
	DefaultMaxDispatchesPerSecond  = 500
	DefaultMaxConcurrentDispatches = 1000
)

var (
	ErrNotFound      = errors.New("queue not found")
	ErrAlreadyExists = errors.New("queue already exists")
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// ValidateName checks a short queue name (letters, digits, hyphens, underscores).
func ValidateName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid queue name %q: use 1-100 letters, numbers, hyphens or underscores", name)
	}
	return nil
}

type Queue struct {
	ID         string      `json:"-"`
	Name       string      `json:"name"`
	Project    string      `json:"-"`
	Location   string      `json:"-"`
	State      string      `json:"-"`
	RateLimits *RateLimits `json:"rateLimits,omitempty"`
	CreatedAt  time.Time   `json:"-"`
}

type RateLimits struct {
	MaxDispatchesPerSecond  int `json:"maxDispatchesPerSecond,omitempty"`
	MaxConcurrentDispatches int `json:"maxConcurrentDispatches,omitempty"`
}

func (q *Queue) Paused() bool { return q.State == StatePaused }

func (q *Queue) ResourceName(project, location string) string {
	if project == "" {
		project = q.Project
	}
	if location == "" {
		location = q.Location
	}
	return "projects/" + project + "/locations/" + location + "/queues/" + q.Name
}
