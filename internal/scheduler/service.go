package scheduler

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Service holds the business logic shared by REST, gRPC and the admin UI.
type Service struct {
	repo       *Repository
	dispatcher *Dispatcher
	now        func() time.Time
}

func NewService(repo *Repository, dispatcher *Dispatcher) *Service {
	return &Service{repo: repo, dispatcher: dispatcher, now: time.Now}
}

func (s *Service) Repo() *Repository       { return s.repo }
func (s *Service) Dispatcher() *Dispatcher { return s.dispatcher }

// Create validates and stores a new job. j.Project, j.Location and j.Name must be set.
func (s *Service) Create(j *Job) (*Job, error) {
	j.ApplyDefaults()
	if err := j.Validate(); err != nil {
		return nil, err
	}
	if j.State == StateEnabled {
		next, err := NextRun(j.Schedule, j.TimeZone, s.now())
		if err != nil {
			return nil, err
		}
		j.NextRunAt = next
	} else {
		j.NextRunAt = time.Time{}
	}
	if err := s.repo.Create(j); err != nil {
		return nil, err
	}
	return s.repo.Get(j.ID)
}

func (s *Service) Get(id string) (*Job, error) {
	j, err := s.repo.Get(id)
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, ErrNotFound
	}
	return j, nil
}

func (s *Service) List(project, location string) ([]*Job, error) {
	return s.repo.List(project, location)
}

func (s *Service) Delete(id string) error { return s.repo.Delete(id) }

func (s *Service) Pause(id string) (*Job, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	if err := s.repo.SetState(id, StatePaused, time.Time{}); err != nil {
		return nil, err
	}
	return s.Get(id)
}

func (s *Service) Resume(id string) (*Job, error) {
	j, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	next, err := NextRun(j.Schedule, j.TimeZone, s.now())
	if err != nil {
		return nil, err
	}
	if err := s.repo.SetState(id, StateEnabled, next); err != nil {
		return nil, err
	}
	return s.Get(id)
}

// Run triggers the job immediately in the background, regardless of state.
// The regular schedule is unaffected.
func (s *Service) Run(id string) (*Job, error) {
	j, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	go s.dispatcher.Fire(context.Background(), j, s.now())
	return j, nil
}

// Update applies patch to the job named id. paths is an update mask using
// proto (snake_case) or JSON (camelCase) names, e.g. "schedule",
// "httpTarget.uri", "retry_config". An empty mask applies every non-zero
// field of patch.
func (s *Service) Update(id string, patch *Job, paths []string) (*Job, error) {
	j, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if err := applyPatch(j, patch, paths); err != nil {
		return nil, err
	}
	j.ApplyDefaults()
	if err := j.Validate(); err != nil {
		return nil, err
	}
	if j.State == StateEnabled {
		next, err := NextRun(j.Schedule, j.TimeZone, s.now())
		if err != nil {
			return nil, err
		}
		j.NextRunAt = next
	}
	if err := s.repo.Update(j); err != nil {
		return nil, err
	}
	return s.Get(id)
}

func snake(p string) string {
	var b strings.Builder
	for i, r := range p {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

func applyPatch(j, p *Job, paths []string) error {
	if len(paths) == 0 {
		if p.Description != "" {
			j.Description = p.Description
		}
		if p.Schedule != "" {
			j.Schedule = p.Schedule
		}
		if p.TimeZone != "" {
			j.TimeZone = p.TimeZone
		}
		if p.URL != "" {
			j.URL = p.URL
		}
		if p.HTTPMethod != "" {
			j.HTTPMethod = p.HTTPMethod
		}
		if p.Headers != nil {
			j.Headers = p.Headers
		}
		if p.Body != nil {
			j.Body = p.Body
		}
		if p.AttemptDeadline != 0 {
			j.AttemptDeadline = p.AttemptDeadline
		}
		r := p.Retry
		if r.RetryCount != 0 {
			j.Retry.RetryCount = r.RetryCount
		}
		if r.MaxRetryDuration != 0 {
			j.Retry.MaxRetryDuration = r.MaxRetryDuration
		}
		if r.MinBackoff != 0 {
			j.Retry.MinBackoff = r.MinBackoff
		}
		if r.MaxBackoff != 0 {
			j.Retry.MaxBackoff = r.MaxBackoff
		}
		if r.MaxDoublings != 0 {
			j.Retry.MaxDoublings = r.MaxDoublings
		}
		return nil
	}
	for _, raw := range paths {
		path := snake(strings.TrimSpace(raw))
		switch path {
		case "description":
			j.Description = p.Description
		case "schedule":
			j.Schedule = p.Schedule
		case "time_zone":
			j.TimeZone = p.TimeZone
		case "attempt_deadline":
			j.AttemptDeadline = p.AttemptDeadline
		case "http_target":
			j.URL, j.HTTPMethod, j.Headers, j.Body = p.URL, p.HTTPMethod, p.Headers, p.Body
		case "http_target.uri":
			j.URL = p.URL
		case "http_target.http_method":
			j.HTTPMethod = p.HTTPMethod
		case "http_target.headers":
			j.Headers = p.Headers
		case "http_target.body":
			j.Body = p.Body
		case "retry_config":
			j.Retry = p.Retry
		case "retry_config.retry_count":
			j.Retry.RetryCount = p.Retry.RetryCount
		case "retry_config.max_retry_duration":
			j.Retry.MaxRetryDuration = p.Retry.MaxRetryDuration
		case "retry_config.min_backoff_duration":
			j.Retry.MinBackoff = p.Retry.MinBackoff
		case "retry_config.max_backoff_duration":
			j.Retry.MaxBackoff = p.Retry.MaxBackoff
		case "retry_config.max_doublings":
			j.Retry.MaxDoublings = p.Retry.MaxDoublings
		default:
			return invalidf("unsupported update mask path %q", raw)
		}
	}
	return nil
}

var services sync.Map // *sql.DB -> *Service

// ForDB returns the process-wide Service for a database connection, so the
// REST API, gRPC server, UI and Runner share one Dispatcher (and therefore
// one in-flight guard) without changing existing constructor signatures.
func ForDB(conn *sql.DB) *Service {
	if v, ok := services.Load(conn); ok {
		return v.(*Service)
	}
	repo := NewRepository(conn)
	v, _ := services.LoadOrStore(conn, NewService(repo, NewDispatcher(repo)))
	return v.(*Service)
}
