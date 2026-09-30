package scheduler

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	StateEnabled = "ENABLED"
	StatePaused  = "PAUSED"

	DefaultTimeZone        = "UTC"
	DefaultAttemptDeadline = 180 * time.Second
	MinAttemptDeadline     = 15 * time.Second
	MaxAttemptDeadline     = 30 * time.Minute
	DefaultMinBackoff      = 5 * time.Second
	DefaultMaxBackoff      = 3600 * time.Second
	DefaultMaxDoublings    = 5
)

var (
	ErrNotFound      = errors.New("job not found")
	ErrAlreadyExists = errors.New("job already exists")

	jobNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,500}$`)

	cronParser = cron.NewParser(
		cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)

	validMethods = map[string]bool{
		"POST": true, "GET": true, "HEAD": true, "PUT": true,
		"DELETE": true, "PATCH": true, "OPTIONS": true,
	}

	// Timezone aliases for environments with incomplete timezone databases.
	// Maps common timezone names to fallbacks that are more likely to exist.
	timezoneAliases = map[string]string{
		"Asia/Kolkata":    "Asia/Calcutta",  // India
		"Asia/Rangoon":    "Asia/Yangon",    // Myanmar
		"Asia/Saigon":     "Asia/Ho_Chi_Minh", // Vietnam
		"Europe/Belfast":  "Europe/London",  // UK
		"Europe/Mariehamn": "Europe/Helsinki", // Finland
		"Etc/GMT+0":       "UTC",            // UTC alias
	}
)

// ValidationError marks an error caused by invalid user input (INVALID_ARGUMENT).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalidf(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// RetryConfig mirrors google.cloud.scheduler.v1.RetryConfig.
type RetryConfig struct {
	RetryCount       int
	MaxRetryDuration time.Duration // 0 = unlimited
	MinBackoff       time.Duration
	MaxBackoff       time.Duration
	MaxDoublings     int
}

// Job is a scheduled HTTP job.
type Job struct {
	ID          string // full resource name
	Project     string
	Location    string
	Name        string // short job id
	Description string
	Schedule    string
	TimeZone    string
	HTTPMethod  string
	URL         string
	Headers     map[string]string
	Body        []byte
	State       string

	AttemptDeadline time.Duration
	Retry           RetryConfig

	NextRunAt      time.Time // zero when paused
	LastAttemptAt  time.Time // zero when never attempted
	LastStatus     string    // "", "OK" or "FAILED"
	LastStatusCode int       // google.rpc.Code of the last attempt
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func ResourceName(project, location, name string) string {
	return "projects/" + project + "/locations/" + location + "/jobs/" + name
}

// ParseName splits projects/{p}/locations/{l}/jobs/{j}.
func ParseName(name string) (project, location, job string, err error) {
	parts := strings.Split(name, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "jobs" ||
		parts[1] == "" || parts[3] == "" || parts[5] == "" {
		return "", "", "", invalidf("invalid job name %q, expected projects/{project}/locations/{location}/jobs/{job}", name)
	}
	return parts[1], parts[3], parts[5], nil
}

// ParseSchedule parses a cron expression (5 fields, or descriptors such as
// @daily, @hourly, @every 5s) evaluated in the given time zone.
func ParseSchedule(spec, tz string) (cron.Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, invalidf("schedule is required")
	}
	up := strings.ToUpper(spec)
	if strings.HasPrefix(up, "TZ=") || strings.HasPrefix(up, "CRON_TZ=") {
		return nil, invalidf("schedule must not contain a TZ= prefix; use timeZone")
	}
	if tz == "" {
		tz = DefaultTimeZone
	}

	// Try to load the timezone, and fall back to an alias if it doesn't exist.
	if _, err := time.LoadLocation(tz); err != nil {
		if alias, ok := timezoneAliases[tz]; ok {
			tz = alias
			if _, err := time.LoadLocation(tz); err != nil {
				return nil, invalidf("invalid timeZone %q (and alias %q): %v", timezoneAliases[tz], tz, err)
			}
		} else {
			return nil, invalidf("invalid timeZone %q: %v", tz, err)
		}
	}

	sched, err := cronParser.Parse("CRON_TZ=" + tz + " " + spec)
	if err != nil {
		return nil, invalidf("invalid schedule %q: %v", spec, err)
	}
	return sched, nil
}

// NextRun computes the first run time strictly after `from`.
func NextRun(spec, tz string, from time.Time) (time.Time, error) {
	sched, err := ParseSchedule(spec, tz)
	if err != nil {
		return time.Time{}, err
	}
	next := sched.Next(from)
	if next.IsZero() {
		return time.Time{}, invalidf("schedule %q never fires", spec)
	}
	return next.UTC(), nil
}

// ApplyDefaults fills zero-valued optional fields the way Cloud Scheduler does.
func (j *Job) ApplyDefaults() {
	if j.TimeZone == "" {
		j.TimeZone = DefaultTimeZone
	}
	if j.HTTPMethod == "" {
		j.HTTPMethod = "POST"
	}
	j.HTTPMethod = strings.ToUpper(j.HTTPMethod)
	if j.State == "" {
		j.State = StateEnabled
	}
	if j.AttemptDeadline == 0 {
		j.AttemptDeadline = DefaultAttemptDeadline
	}
	if j.Retry.MinBackoff == 0 {
		j.Retry.MinBackoff = DefaultMinBackoff
	}
	if j.Retry.MaxBackoff == 0 {
		j.Retry.MaxBackoff = DefaultMaxBackoff
	}
	if j.Retry.MaxDoublings == 0 {
		j.Retry.MaxDoublings = DefaultMaxDoublings
	}
}

// Validate checks the job and returns a *ValidationError on failure.
func (j *Job) Validate() error {
	if !jobNameRe.MatchString(j.Name) {
		return invalidf("invalid job id %q: only letters, numbers, hyphens and underscores are allowed (max 500 chars)", j.Name)
	}
	if len(j.Description) > 500 {
		return invalidf("description must not exceed 500 characters")
	}
	if _, err := ParseSchedule(j.Schedule, j.TimeZone); err != nil {
		return err
	}
	if j.URL == "" {
		return invalidf("httpTarget.uri is required")
	}
	u, err := url.Parse(j.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalidf("httpTarget.uri %q must be an absolute http(s) URL", j.URL)
	}
	if !validMethods[j.HTTPMethod] {
		return invalidf("unsupported httpTarget.httpMethod %q", j.HTTPMethod)
	}
	if j.AttemptDeadline < MinAttemptDeadline || j.AttemptDeadline > MaxAttemptDeadline {
		return invalidf("attemptDeadline must be between %s and %s", MinAttemptDeadline, MaxAttemptDeadline)
	}
	r := j.Retry
	if r.RetryCount < 0 {
		return invalidf("retryConfig.retryCount must not be negative")
	}
	if r.MaxRetryDuration < 0 || r.MinBackoff < 0 || r.MaxBackoff < 0 || r.MaxDoublings < 0 {
		return invalidf("retryConfig values must not be negative")
	}
	if r.MinBackoff > r.MaxBackoff {
		return invalidf("retryConfig.minBackoffDuration must not exceed maxBackoffDuration")
	}
	if j.State != StateEnabled && j.State != StatePaused {
		return invalidf("invalid state %q", j.State)
	}
	return nil
}

// Backoff returns the wait before retry number `retry` (1-based): the minimum
// backoff doubles maxDoublings times, then grows linearly, capped at MaxBackoff.
func (r RetryConfig) Backoff(retry int) time.Duration {
	if retry < 1 {
		retry = 1
	}
	d := r.MinBackoff
	doublings := retry - 1
	if doublings <= r.MaxDoublings {
		for i := 0; i < doublings; i++ {
			d *= 2
			if r.MaxBackoff > 0 && d >= r.MaxBackoff {
				return r.MaxBackoff
			}
		}
	} else {
		base := r.MinBackoff
		for i := 0; i < r.MaxDoublings; i++ {
			base *= 2
		}
		d = base * time.Duration(doublings-r.MaxDoublings+1)
	}
	if r.MaxBackoff > 0 && d > r.MaxBackoff {
		d = r.MaxBackoff
	}
	return d
}
