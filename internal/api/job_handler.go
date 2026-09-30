package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/scheduler"
)

// JobHandler serves the Cloud Scheduler v1 REST surface.
type JobHandler struct {
	svc *scheduler.Service
	cfg *config.Config
}

func NewJobHandler(database *db.DB, cfg *config.Config) *JobHandler {
	return &JobHandler{svc: scheduler.ForDB(database.Conn()), cfg: cfg}
}

type jobHTTPTarget struct {
	URI        string            `json:"uri,omitempty"`
	HTTPMethod string            `json:"httpMethod,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       []byte            `json:"body,omitempty"` // base64 in JSON
}

type jobRetryConfig struct {
	RetryCount         int    `json:"retryCount,omitempty"`
	MaxRetryDuration   string `json:"maxRetryDuration,omitempty"`
	MinBackoffDuration string `json:"minBackoffDuration,omitempty"`
	MaxBackoffDuration string `json:"maxBackoffDuration,omitempty"`
	MaxDoublings       int    `json:"maxDoublings,omitempty"`
}

type jobStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type jobJSON struct {
	Name            string          `json:"name,omitempty"`
	Description     string          `json:"description,omitempty"`
	Schedule        string          `json:"schedule,omitempty"`
	TimeZone        string          `json:"timeZone,omitempty"`
	State           string          `json:"state,omitempty"`
	HTTPTarget      *jobHTTPTarget  `json:"httpTarget,omitempty"`
	RetryConfig     *jobRetryConfig `json:"retryConfig,omitempty"`
	AttemptDeadline string          `json:"attemptDeadline,omitempty"`
	ScheduleTime    string          `json:"scheduleTime,omitempty"`
	LastAttemptTime string          `json:"lastAttemptTime,omitempty"`
	UserUpdateTime  string          `json:"userUpdateTime,omitempty"`
	Status          *jobStatus      `json:"status,omitempty"`
}

type listJobsResponse struct {
	Jobs          []*jobJSON `json:"jobs"`
	NextPageToken string     `json:"nextPageToken,omitempty"`
}

func formatDuration(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}

func parseDuration(field, s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if !strings.HasSuffix(s, "s") {
		return 0, &scheduler.ValidationError{Msg: field + ` must be a duration in seconds such as "30s"`}
	}
	f, err := strconv.ParseFloat(strings.TrimSuffix(s, "s"), 64)
	if err != nil || f < 0 {
		return 0, &scheduler.ValidationError{Msg: field + ` must be a non-negative duration such as "30s"`}
	}
	return time.Duration(f * float64(time.Second)), nil
}

func toJobJSON(j *scheduler.Job) *jobJSON {
	out := &jobJSON{
		Name:        j.ID,
		Description: j.Description,
		Schedule:    j.Schedule,
		TimeZone:    j.TimeZone,
		State:       j.State,
		HTTPTarget: &jobHTTPTarget{
			URI: j.URL, HTTPMethod: j.HTTPMethod, Headers: j.Headers, Body: j.Body,
		},
		RetryConfig: &jobRetryConfig{
			RetryCount:         j.Retry.RetryCount,
			MaxRetryDuration:   formatDuration(j.Retry.MaxRetryDuration),
			MinBackoffDuration: formatDuration(j.Retry.MinBackoff),
			MaxBackoffDuration: formatDuration(j.Retry.MaxBackoff),
			MaxDoublings:       j.Retry.MaxDoublings,
		},
		AttemptDeadline: formatDuration(j.AttemptDeadline),
		UserUpdateTime:  j.UpdatedAt.Format(time.RFC3339Nano),
	}
	if !j.NextRunAt.IsZero() {
		out.ScheduleTime = j.NextRunAt.Format(time.RFC3339Nano)
	}
	if !j.LastAttemptAt.IsZero() {
		out.LastAttemptTime = j.LastAttemptAt.Format(time.RFC3339Nano)
		out.Status = &jobStatus{Code: j.LastStatusCode, Message: j.LastError}
	}
	return out
}

func fromJobJSON(in *jobJSON) (*scheduler.Job, error) {
	j := &scheduler.Job{
		Description: in.Description,
		Schedule:    in.Schedule,
		TimeZone:    in.TimeZone,
	}
	if in.HTTPTarget != nil {
		j.URL = in.HTTPTarget.URI
		j.HTTPMethod = in.HTTPTarget.HTTPMethod
		j.Headers = in.HTTPTarget.Headers
		j.Body = in.HTTPTarget.Body
	}
	var err error
	if j.AttemptDeadline, err = parseDuration("attemptDeadline", in.AttemptDeadline); err != nil {
		return nil, err
	}
	if rc := in.RetryConfig; rc != nil {
		j.Retry.RetryCount = rc.RetryCount
		j.Retry.MaxDoublings = rc.MaxDoublings
		if j.Retry.MaxRetryDuration, err = parseDuration("retryConfig.maxRetryDuration", rc.MaxRetryDuration); err != nil {
			return nil, err
		}
		if j.Retry.MinBackoff, err = parseDuration("retryConfig.minBackoffDuration", rc.MinBackoffDuration); err != nil {
			return nil, err
		}
		if j.Retry.MaxBackoff, err = parseDuration("retryConfig.maxBackoffDuration", rc.MaxBackoffDuration); err != nil {
			return nil, err
		}
	}
	return j, nil
}

func decodeJob(r *http.Request) (*jobJSON, error) {
	var in jobJSON
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return nil, &scheduler.ValidationError{Msg: "invalid request body: " + err.Error()}
	}
	return &in, nil
}

// writeJobError maps scheduler errors onto Google-style HTTP errors.
func writeJobError(w http.ResponseWriter, err error) {
	var ve *scheduler.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", ve.Msg)
	case errors.Is(err, scheduler.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "job not found")
	case errors.Is(err, scheduler.ErrAlreadyExists):
		writeError(w, http.StatusConflict, "ALREADY_EXISTS", "job already exists")
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}

func (h *JobHandler) jobID(r *http.Request) string {
	return scheduler.ResourceName(r.PathValue("project"), r.PathValue("location"), r.PathValue("job"))
}

func (h *JobHandler) Create(w http.ResponseWriter, r *http.Request) {
	project, location := r.PathValue("project"), r.PathValue("location")
	in, err := decodeJob(r)
	if err != nil {
		writeJobError(w, err)
		return
	}
	j, err := fromJobJSON(in)
	if err != nil {
		writeJobError(w, err)
		return
	}
	if in.HTTPTarget == nil {
		writeJobError(w, &scheduler.ValidationError{Msg: "httpTarget is required"})
		return
	}
	// name may be a full resource name (must match the parent) or a bare id.
	name := in.Name
	if strings.Contains(name, "/") {
		p, l, n, perr := scheduler.ParseName(name)
		if perr != nil {
			writeJobError(w, perr)
			return
		}
		if p != project || l != location {
			writeJobError(w, &scheduler.ValidationError{Msg: "job name does not match the parent project/location"})
			return
		}
		name = n
	}
	if name == "" {
		writeJobError(w, &scheduler.ValidationError{Msg: "job name is required"})
		return
	}
	j.Project, j.Location, j.Name = project, location, name
	j.State = in.State
	created, err := h.svc.Create(j)
	if err != nil {
		writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toJobJSON(created))
}

func (h *JobHandler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.List(r.PathValue("project"), r.PathValue("location"))
	if err != nil {
		writeJobError(w, err)
		return
	}
	resp := &listJobsResponse{Jobs: make([]*jobJSON, len(list))}
	for i, j := range list {
		resp.Jobs[i] = toJobJSON(j)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *JobHandler) Get(w http.ResponseWriter, r *http.Request) {
	j, err := h.svc.Get(h.jobID(r))
	if err != nil {
		writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toJobJSON(j))
}

func (h *JobHandler) Update(w http.ResponseWriter, r *http.Request) {
	in, err := decodeJob(r)
	if err != nil {
		writeJobError(w, err)
		return
	}
	patch, err := fromJobJSON(in)
	if err != nil {
		writeJobError(w, err)
		return
	}
	var paths []string
	if m := r.URL.Query().Get("updateMask"); m != "" {
		paths = strings.Split(m, ",")
	}
	j, err := h.svc.Update(h.jobID(r), patch, paths)
	if err != nil {
		writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toJobJSON(j))
}

func (h *JobHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Delete(h.jobID(r)); err != nil {
		writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// Action handles POST .../jobs/{job}:pause|:resume|:run.
func (h *JobHandler) Action(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("job")
	name, verb, ok := strings.Cut(raw, ":")
	if !ok || name == "" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "unknown method; expected :pause, :resume or :run")
		return
	}
	id := scheduler.ResourceName(r.PathValue("project"), r.PathValue("location"), name)
	var (
		j   *scheduler.Job
		err error
	)
	switch verb {
	case "pause":
		j, err = h.svc.Pause(id)
	case "resume":
		j, err = h.svc.Resume(id)
	case "run":
		j, err = h.svc.Run(id)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "unknown method :"+verb)
		return
	}
	if err != nil {
		writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toJobJSON(j))
}
