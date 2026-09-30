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
	jobH := NewJobHandler(database, cfg)
	schedUI := ui.NewSchedulerHandler(database, cfg)

	mux.HandleFunc("GET /health", healthHandler)

	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues", queueH.Create)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues", queueH.List)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues/{queue}", queueH.Get)
	mux.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/queues/{queue}", queueH.Delete)
	// Custom verbs (:pause, :resume, :purge); wildcards must span a whole segment.
	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queue}", queueH.Action)

	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks", taskH.Create)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues/{queue}/tasks", taskH.List)
	mux.HandleFunc("GET /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{task}", taskH.Get)
	mux.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{task}", taskH.Delete)

	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{task}/run", taskH.Run)
	// Google-style custom verb (:run).
	mux.HandleFunc("POST /v2/projects/{project}/locations/{location}/queues/{queue}/tasks/{task}", taskH.Action)

	mux.HandleFunc("GET /ui/queues", uiH.ListQueues)
	mux.HandleFunc("POST /ui/queues", uiH.CreateQueue)
	mux.HandleFunc("GET /ui/queue", uiH.QueueDetail)
	mux.HandleFunc("POST /ui/queue/delete", uiH.DeleteQueue)
	mux.HandleFunc("POST /ui/queue/tasks/retry", uiH.RetryTask)
	mux.HandleFunc("POST /ui/queue/tasks/run", uiH.RunTask)
	mux.HandleFunc("POST /ui/queue/tasks/delete", uiH.DeleteTask)
	mux.HandleFunc("POST /ui/queue/pause", uiH.PauseQueue)
	mux.HandleFunc("POST /ui/queue/resume", uiH.ResumeQueue)
	mux.HandleFunc("POST /ui/queue/purge", uiH.PurgeQueue)
	mux.HandleFunc("GET /ui/task", uiH.TaskDetail)
	mux.HandleFunc("GET /ui/{$}", uiH.Index)
	mux.HandleFunc("GET /ui", uiH.Index)

	jobBase := "/v1/projects/{project}/locations/{location}/jobs"
	mux.HandleFunc("POST "+jobBase, jobH.Create)
	mux.HandleFunc("GET "+jobBase, jobH.List)
	mux.HandleFunc("GET "+jobBase+"/{job}", jobH.Get)
	mux.HandleFunc("PATCH "+jobBase+"/{job}", jobH.Update)
	mux.HandleFunc("DELETE "+jobBase+"/{job}", jobH.Delete)
	// ServeMux wildcards must span a whole segment, so the custom verbs
	// (:pause, :resume, :run) are dispatched by jobH.Action.
	mux.HandleFunc("POST "+jobBase+"/{job}", jobH.Action)

	mux.HandleFunc("GET /ui/jobs", schedUI.ListJobs)
	mux.HandleFunc("POST /ui/jobs", schedUI.CreateJob)
	mux.HandleFunc("GET /ui/job", schedUI.JobDetail)
	mux.HandleFunc("POST /ui/job/update", schedUI.UpdateJob)
	mux.HandleFunc("POST /ui/job/pause", schedUI.PauseJob)
	mux.HandleFunc("POST /ui/job/resume", schedUI.ResumeJob)
	mux.HandleFunc("POST /ui/job/run", schedUI.RunJob)
	mux.HandleFunc("POST /ui/job/delete", schedUI.DeleteJob)

	return mux
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
