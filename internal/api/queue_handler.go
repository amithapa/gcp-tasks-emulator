package api

import (
	"encoding/json"
	"net/http"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/queues"
)

type QueueHandler struct {
	db  *db.DB
	cfg *config.Config
}

func NewQueueHandler(database *db.DB, cfg *config.Config) *QueueHandler {
	return &QueueHandler{db: database, cfg: cfg}
}

type createQueueRequest struct {
	Queue *struct {
		Name       string `json:"name"`
		RateLimits *struct {
			MaxDispatchesPerSecond  int `json:"maxDispatchesPerSecond"`
			MaxConcurrentDispatches int `json:"maxConcurrentDispatches"`
		} `json:"rateLimits"`
	} `json:"queue"`
}

type queueResponse struct {
	Name       string `json:"name"`
	RateLimits *struct {
		MaxDispatchesPerSecond  int `json:"maxDispatchesPerSecond,omitempty"`
		MaxConcurrentDispatches int `json:"maxConcurrentDispatches,omitempty"`
	} `json:"rateLimits,omitempty"`
}

type listQueuesResponse struct {
	Queues []*queueResponse `json:"queues"`
}

func (h *QueueHandler) Create(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	if project == "" {
		project = h.cfg.DefaultProject
	}
	if location == "" {
		location = h.cfg.DefaultLocation
	}

	var req createQueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	if req.Queue == nil || req.Queue.Name == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "queue name is required")
		return
	}

	q := &queues.Queue{
		Project:  project,
		Location: location,
		Name:     req.Queue.Name,
	}
	if req.Queue.RateLimits != nil {
		q.RateLimits = &queues.RateLimits{
			MaxDispatchesPerSecond:  req.Queue.RateLimits.MaxDispatchesPerSecond,
			MaxConcurrentDispatches: req.Queue.RateLimits.MaxConcurrentDispatches,
		}
	}

	repo := queues.NewRepository(h.db.Conn())
	if err := repo.Create(q); err != nil {
		writeError(w, http.StatusConflict, "ALREADY_EXISTS", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, toQueueResponse(q))
}

func (h *QueueHandler) List(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	location := r.PathValue("location")
	if project == "" {
		project = h.cfg.DefaultProject
	}
	if location == "" {
		location = h.cfg.DefaultLocation
	}

	repo := queues.NewRepository(h.db.Conn())
	list, err := repo.List(project, location)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	resp := &listQueuesResponse{
		Queues: make([]*queueResponse, len(list)),
	}
	for i, q := range list {
		resp.Queues[i] = toQueueResponse(q)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *QueueHandler) Get(w http.ResponseWriter, r *http.Request) {
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
	repo := queues.NewRepository(h.db.Conn())
	q, err := repo.Get(queueID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if q == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "queue not found")
		return
	}
	writeJSON(w, http.StatusOK, toQueueResponse(q))
}

func (h *QueueHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	repo := queues.NewRepository(h.db.Conn())
	if err := repo.Delete(queueID); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

func toQueueResponse(q *queues.Queue) *queueResponse {
	resp := &queueResponse{
		Name: q.ResourceName(q.Project, q.Location),
	}
	if q.RateLimits != nil {
		resp.RateLimits = &struct {
			MaxDispatchesPerSecond  int `json:"maxDispatchesPerSecond,omitempty"`
			MaxConcurrentDispatches int `json:"maxConcurrentDispatches,omitempty"`
		}{
			MaxDispatchesPerSecond:  q.RateLimits.MaxDispatchesPerSecond,
			MaxConcurrentDispatches: q.RateLimits.MaxConcurrentDispatches,
		}
	}
	return resp
}
