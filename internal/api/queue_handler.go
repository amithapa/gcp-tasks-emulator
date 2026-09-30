package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

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
	State      string `json:"state,omitempty"`
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

	// Accept either a short name or a full resource name.
	shortName := req.Queue.Name
	if i := strings.LastIndex(shortName, "/"); i >= 0 {
		shortName = shortName[i+1:]
	}
	if err := queues.ValidateName(shortName); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}

	q := &queues.Queue{
		Project:  project,
		Location: location,
		Name:     shortName,
	}
	if req.Queue.RateLimits != nil {
		q.RateLimits = &queues.RateLimits{
			MaxDispatchesPerSecond:  req.Queue.RateLimits.MaxDispatchesPerSecond,
			MaxConcurrentDispatches: req.Queue.RateLimits.MaxConcurrentDispatches,
		}
	}

	repo := queues.NewRepository(h.db.Conn())
	if err := repo.Create(q); err != nil {
		if errors.Is(err, queues.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "ALREADY_EXISTS", "queue already exists: "+q.ID)
		} else {
			writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		}
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
		writeQueueError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func writeQueueError(w http.ResponseWriter, err error) {
	if errors.Is(err, queues.ErrNotFound) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
}

// Action handles the Google-style custom verbs POST .../queues/{queue}:pause,
// :resume and :purge (ServeMux wildcards cannot match part of a segment).
func (h *QueueHandler) Action(w http.ResponseWriter, r *http.Request) {
	name, verb, _ := strings.Cut(r.PathValue("queue"), ":")
	queueID := "projects/" + r.PathValue("project") + "/locations/" + r.PathValue("location") + "/queues/" + name
	repo := queues.NewRepository(h.db.Conn())

	var err error
	switch verb {
	case "pause":
		err = repo.SetState(queueID, queues.StatePaused)
	case "resume":
		err = repo.SetState(queueID, queues.StateRunning)
	case "purge":
		_, err = repo.Purge(queueID)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "unknown method")
		return
	}
	if err != nil {
		writeQueueError(w, err)
		return
	}
	q, err := repo.Get(queueID)
	if err != nil || q == nil {
		writeQueueError(w, errors.Join(queues.ErrNotFound, err))
		return
	}
	writeJSON(w, http.StatusOK, toQueueResponse(q))
}

func toQueueResponse(q *queues.Queue) *queueResponse {
	resp := &queueResponse{
		Name:  q.ResourceName(q.Project, q.Location),
		State: q.State,
	}
	if resp.State == "" {
		resp.State = queues.StateRunning
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
