package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/queues"
	"cloud-tasks-emulator/internal/tasks"
)

const (
	defaultPageSize = 1000
	maxPageSize     = 1000
)

var validHTTPMethods = map[string]bool{
	"POST": true, "GET": true, "HEAD": true, "PUT": true,
	"DELETE": true, "PATCH": true, "OPTIONS": true,
}

type TaskHandler struct {
	db  *db.DB
	cfg *config.Config
}

func NewTaskHandler(database *db.DB, cfg *config.Config) *TaskHandler {
	return &TaskHandler{db: database, cfg: cfg}
}

// flexDuration accepts a dispatch deadline as a JSON number of seconds or as
// a protobuf JSON duration string such as "30s" or "30.5s".
type flexDuration struct {
	Set     bool
	Seconds float64
}

func (d *flexDuration) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		return nil
	}
	s = strings.Trim(s, `"`)
	s = strings.TrimSuffix(s, "s")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("invalid duration %s (expected e.g. \"30s\")", string(b))
	}
	d.Set, d.Seconds = true, f
	return nil
}

type createTaskRequest struct {
	Task *struct {
		Name        string `json:"name"`
		HTTPRequest *struct {
			HTTPMethod string            `json:"httpMethod"`
			URL        string            `json:"url"`
			Headers    map[string]string `json:"headers"`
			Body       json.RawMessage   `json:"body"`
		} `json:"httpRequest"`
		ScheduleTime     string       `json:"scheduleTime"`
		DispatchDeadline flexDuration `json:"dispatchDeadline"`
	} `json:"task"`
}

// decodeBody decodes the proto3-JSON "body" field (base64). For leniency a
// raw JSON object/array is passed through as-is.
func decodeBody(raw json.RawMessage) ([]byte, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] != '"' {
		return []byte(raw), nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("httpRequest.body must be a base64-encoded string")
}

func queueIDFrom(r *http.Request, cfg *config.Config) string {
	project := r.PathValue("project")
	location := r.PathValue("location")
	if project == "" {
		project = cfg.DefaultProject
	}
	if location == "" {
		location = cfg.DefaultLocation
	}
	return "projects/" + project + "/locations/" + location + "/queues/" + r.PathValue("queue")
}

func (h *TaskHandler) Create(w http.ResponseWriter, r *http.Request) {
	queueID := queueIDFrom(r, h.cfg)

	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if req.Task == nil || req.Task.HTTPRequest == nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "task.httpRequest is required")
		return
	}
	httpReq := req.Task.HTTPRequest
	if httpReq.URL == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "task.httpRequest.url is required")
		return
	}
	if err := tasks.ValidateURL(httpReq.URL); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}

	method := strings.ToUpper(httpReq.HTTPMethod)
	if method == "" || method == "HTTP_METHOD_UNSPECIFIED" {
		method = "POST"
	}
	if !validHTTPMethods[method] {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "unsupported httpMethod "+httpReq.HTTPMethod)
		return
	}

	scheduleTime := time.Now()
	if req.Task.ScheduleTime != "" {
		t, err := time.Parse(time.RFC3339, req.Task.ScheduleTime)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid scheduleTime (expected RFC 3339): "+err.Error())
			return
		}
		scheduleTime = t
	}

	dispatchDeadline := 30
	if req.Task.DispatchDeadline.Set {
		if req.Task.DispatchDeadline.Seconds <= 0 {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "dispatchDeadline must be positive")
			return
		}
		dispatchDeadline = int(req.Task.DispatchDeadline.Seconds + 0.5)
	}

	body, err := decodeBody(httpReq.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}

	taskID := uuid.New().String()
	if name := req.Task.Name; name != "" {
		if strings.Contains(name, "/") {
			prefix := queueID + "/tasks/"
			if !strings.HasPrefix(name, prefix) {
				writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "task.name must belong to queue "+queueID)
				return
			}
			name = strings.TrimPrefix(name, prefix)
		}
		if err := tasks.ValidateID(name); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		taskID = name
	}
	taskName := queueID + "/tasks/" + taskID

	queueRepo := queues.NewRepository(h.db.Conn())
	q, err := queueRepo.Get(queueID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if q == nil {
		if !h.cfg.AutoCreateQueues {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "queue not found")
			return
		}
		q = &queues.Queue{Project: r.PathValue("project"), Location: r.PathValue("location"), Name: r.PathValue("queue")}
		if err := queues.ValidateName(q.Name); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		if err := queueRepo.Create(q); err != nil && !errors.Is(err, queues.ErrAlreadyExists) {
			writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
	}

	t := &tasks.Task{
		ID:               taskName,
		Name:             taskName,
		QueueID:          queueID,
		HTTPMethod:       method,
		URL:              httpReq.URL,
		Headers:          httpReq.Headers,
		Body:             body,
		ScheduleTime:     scheduleTime,
		DispatchDeadline: dispatchDeadline,
		Status:           tasks.StatusPending,
		RetryCount:       0,
		MaxRetries:       h.cfg.DefaultMaxRetries,
		NextAttemptAt:    scheduleTime,
	}

	repo := tasks.NewRepository(h.db.Conn())
	if err := repo.Create(t); err != nil {
		if errors.Is(err, tasks.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "ALREADY_EXISTS", "task already exists: "+taskName)
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	// Re-read so createTime and stored precision are reflected.
	if stored, err := repo.Get(taskName); err == nil && stored != nil {
		t = stored
	}
	writeJSON(w, http.StatusOK, toTaskResponse(t))
}

func (h *TaskHandler) List(w http.ResponseWriter, r *http.Request) {
	queueID := queueIDFrom(r, h.cfg)
	statusFilter := r.URL.Query().Get("status")

	pageSize := defaultPageSize
	if v := r.URL.Query().Get("pageSize"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid pageSize")
			return
		}
		if n > 0 {
			pageSize = min(n, maxPageSize)
		}
	}
	offset := 0
	if v := r.URL.Query().Get("pageToken"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid pageToken")
			return
		}
		offset = n
	}

	repo := tasks.NewRepository(h.db.Conn())
	// Fetch one extra row to know whether another page exists.
	list, err := repo.ListPage(queueID, statusFilter, pageSize+1, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	resp := &listTasksResponse{Tasks: []*taskResponse{}}
	if len(list) > pageSize {
		list = list[:pageSize]
		resp.NextPageToken = strconv.Itoa(offset + pageSize)
	}
	for _, t := range list {
		resp.Tasks = append(resp.Tasks, toTaskResponse(t))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *TaskHandler) Get(w http.ResponseWriter, r *http.Request) {
	taskName := queueIDFrom(r, h.cfg) + "/tasks/" + r.PathValue("task")
	repo := tasks.NewRepository(h.db.Conn())
	t, err := repo.Get(taskName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if t == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
		return
	}
	writeJSON(w, http.StatusOK, toTaskResponse(t))
}

func (h *TaskHandler) Delete(w http.ResponseWriter, r *http.Request) {
	taskName := queueIDFrom(r, h.cfg) + "/tasks/" + r.PathValue("task")
	repo := tasks.NewRepository(h.db.Conn())
	if err := repo.Delete(taskName); err != nil {
		if errors.Is(err, tasks.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

// Run handles POST .../tasks/{task}/run.
func (h *TaskHandler) Run(w http.ResponseWriter, r *http.Request) {
	h.run(w, queueIDFrom(r, h.cfg)+"/tasks/"+r.PathValue("task"))
}

// Action handles the Google-style custom verb POST .../tasks/{task}:run
// (ServeMux wildcards cannot match part of a segment).
func (h *TaskHandler) Action(w http.ResponseWriter, r *http.Request) {
	id, verb, _ := strings.Cut(r.PathValue("task"), ":")
	if verb != "run" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "unknown method")
		return
	}
	h.run(w, queueIDFrom(r, h.cfg)+"/tasks/"+id)
}

func (h *TaskHandler) run(w http.ResponseWriter, taskName string) {
	repo := tasks.NewRepository(h.db.Conn())
	if err := repo.SetNextAttemptNow(taskName); err != nil {
		if errors.Is(err, tasks.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "task not found")
		} else {
			writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		}
		return
	}
	t, err := repo.Get(taskName)
	if err != nil || t == nil {
		writeJSON(w, http.StatusOK, map[string]string{"name": taskName})
		return
	}
	writeJSON(w, http.StatusOK, toTaskResponse(t))
}

type listTasksResponse struct {
	Tasks         []*taskResponse `json:"tasks"`
	NextPageToken string          `json:"nextPageToken,omitempty"`
}

type httpRequestResponse struct {
	URL        string            `json:"url"`
	HTTPMethod string            `json:"httpMethod"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
}

type taskResponse struct {
	Name             string               `json:"name"`
	HTTPRequest      *httpRequestResponse `json:"httpRequest,omitempty"`
	ScheduleTime     string               `json:"scheduleTime,omitempty"`
	CreateTime       string               `json:"createTime,omitempty"`
	DispatchDeadline string               `json:"dispatchDeadline,omitempty"`
	DispatchCount    int                  `json:"dispatchCount"`
	ResponseCount    int                  `json:"responseCount"`
}

func toTaskResponse(t *tasks.Task) *taskResponse {
	resp := &taskResponse{
		Name:          t.Name,
		DispatchCount: t.Attempts(),
		ResponseCount: t.Attempts(),
	}
	if t.Status == tasks.StatusRunning {
		resp.ResponseCount--
	}
	if t.URL == "" {
		return resp
	}
	resp.HTTPRequest = &httpRequestResponse{
		URL:        t.URL,
		HTTPMethod: t.HTTPMethod,
		Headers:    t.Headers,
		Body:       base64.StdEncoding.EncodeToString(t.Body),
	}
	if !t.ScheduleTime.IsZero() {
		resp.ScheduleTime = t.ScheduleTime.UTC().Format(time.RFC3339Nano)
	}
	if !t.CreatedAt.IsZero() {
		resp.CreateTime = t.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	resp.DispatchDeadline = strconv.Itoa(t.DispatchDeadline) + "s"
	return resp
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
