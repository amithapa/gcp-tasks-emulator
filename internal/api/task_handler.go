package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/queues"
	"cloud-tasks-emulator/internal/tasks"
)

type TaskHandler struct {
	db  *db.DB
	cfg *config.Config
}

func NewTaskHandler(database *db.DB, cfg *config.Config) *TaskHandler {
	return &TaskHandler{db: database, cfg: cfg}
}

type createTaskRequest struct {
	Task *struct {
		HTTPRequest *struct {
			HTTPMethod string            `json:"httpMethod"`
			URL        string            `json:"url"`
			Headers    map[string]string `json:"headers"`
			Body       json.RawMessage   `json:"body"`
		} `json:"httpRequest"`
		ScheduleTime     string `json:"scheduleTime"`
		DispatchDeadline int    `json:"dispatchDeadline"`
	} `json:"task"`
}

func (h *TaskHandler) Create(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	queueName := r.PathValue("queue")
	if project == "" {
		project = h.cfg.DefaultProject
	}
	if location == "" {
		location = h.cfg.DefaultLocation
	}

	queueID := "projects/" + project + "/locations/" + location + "/queues/" + queueName
	queueRepo := queues.NewRepository(h.db.Conn())
	q, err := queueRepo.Get(queueID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if q == nil {
		if h.cfg.AutoCreateQueues {
			q = &queues.Queue{Project: project, Location: location, Name: queueName}
			if err := queueRepo.Create(q); err != nil {
				writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
				return
			}
		} else {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "queue not found")
			return
		}
	}

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

	method := "POST"
	if httpReq.HTTPMethod != "" {
		method = httpReq.HTTPMethod
	}

	scheduleTime := time.Now()
	if req.Task.ScheduleTime != "" {
		if t, err := time.Parse(time.RFC3339, req.Task.ScheduleTime); err == nil {
			scheduleTime = t
		}
	}

	dispatchDeadline := 30
	if req.Task.DispatchDeadline > 0 {
		dispatchDeadline = req.Task.DispatchDeadline
	}

	var body []byte
	if len(httpReq.Body) > 0 {
		if httpReq.Body[0] == '"' {
			var s string
			if err := json.Unmarshal(httpReq.Body, &s); err == nil {
				body, _ = base64.StdEncoding.DecodeString(s)
			}
		}
		if body == nil {
			body = []byte(httpReq.Body)
		}
	}

	taskID := uuid.New().String()
	taskName := queueID + "/tasks/" + taskID

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
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, toTaskResponse(t))
}

func (h *TaskHandler) List(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	queueName := r.PathValue("queue")
	if project == "" {
		project = h.cfg.DefaultProject
	}
	if location == "" {
		location = h.cfg.DefaultLocation
	}

	queueID := "projects/" + project + "/locations/" + location + "/queues/" + queueName
	statusFilter := r.URL.Query().Get("status")

	repo := tasks.NewRepository(h.db.Conn())
	list, err := repo.List(queueID, statusFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	resp := &listTasksResponse{
		Tasks: make([]*taskResponse, len(list)),
	}
	for i, t := range list {
		resp.Tasks[i] = toTaskResponse(t)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *TaskHandler) Get(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	queueName := r.PathValue("queue")
	taskID := r.PathValue("task")
	if project == "" {
		project = h.cfg.DefaultProject
	}
	if location == "" {
		location = h.cfg.DefaultLocation
	}

	taskName := "projects/" + project + "/locations/" + location + "/queues/" + queueName + "/tasks/" + taskID
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
	project := r.PathValue("project")
	location := r.PathValue("location")
	queueName := r.PathValue("queue")
	taskID := r.PathValue("task")
	if project == "" {
		project = h.cfg.DefaultProject
	}
	if location == "" {
		location = h.cfg.DefaultLocation
	}

	taskName := "projects/" + project + "/locations/" + location + "/queues/" + queueName + "/tasks/" + taskID
	repo := tasks.NewRepository(h.db.Conn())
	if err := repo.Delete(taskName); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *TaskHandler) Run(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	queueName := r.PathValue("queue")
	taskID := r.PathValue("task")
	if project == "" {
		project = h.cfg.DefaultProject
	}
	if location == "" {
		location = h.cfg.DefaultLocation
	}

	taskName := "projects/" + project + "/locations/" + location + "/queues/" + queueName + "/tasks/" + taskID
	repo := tasks.NewRepository(h.db.Conn())
	if err := repo.SetNextAttemptNow(taskName); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": taskName})
}

type listTasksResponse struct {
	Tasks []*taskResponse `json:"tasks"`
}

type taskResponse struct {
	Name string `json:"name"`
}

func toTaskResponse(t *tasks.Task) *taskResponse {
	return &taskResponse{Name: t.Name}
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
