package api

import (
	"net/http"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/ui"
)

func NewRouter(database *db.DB, cfg *config.Config) http.Handler {
	mux := http.NewServeMux()

	queueH := NewQueueHandler(database, cfg)
	taskH := NewTaskHandler(database, cfg)
	uiH := ui.NewHandler(database, cfg)

	mux.HandleFunc("GET /health", healthHandler)

	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues", queueH.Create)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues", queueH.List)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues/{queue}", queueH.Get)
	mux.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/queues/{queue}", queueH.Delete)

	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks", taskH.Create)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues/{queue}/tasks", taskH.List)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{task}", taskH.Get)
	mux.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{task}", taskH.Delete)

	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{task}/run", taskH.Run)

	mux.HandleFunc("GET /ui/queues", uiH.ListQueues)
	mux.HandleFunc("POST /ui/queues", uiH.CreateQueue)
	mux.HandleFunc("GET /ui/queue", uiH.QueueDetail)
	mux.HandleFunc("POST /ui/queue/delete", uiH.DeleteQueue)
	mux.HandleFunc("POST /ui/queue/tasks/retry", uiH.RetryTask)
	mux.HandleFunc("POST /ui/queue/tasks/run", uiH.RunTask)

	return mux
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
