package queues

import "time"

type Queue struct {
	ID         string      `json:"-"`
	Name       string      `json:"name"`
	Project    string      `json:"-"`
	Location   string      `json:"-"`
	RateLimits *RateLimits `json:"rateLimits,omitempty"`
	CreatedAt  time.Time   `json:"-"`
}

type RateLimits struct {
	MaxDispatchesPerSecond  int `json:"maxDispatchesPerSecond,omitempty"`
	MaxConcurrentDispatches int `json:"maxConcurrentDispatches,omitempty"`
}

func (q *Queue) ResourceName(project, location string) string {
	if project == "" {
		project = q.Project
	}
	if location == "" {
		location = q.Location
	}
	return "projects/" + project + "/locations/" + location + "/queues/" + q.Name
}
