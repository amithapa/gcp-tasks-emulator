package ui

import (
	"embed"
	"html/template"
	"net/http"
	"net/url"
	"time"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/queues"
	"cloud-tasks-emulator/internal/tasks"
)

//go:embed templates/*.html
var templatesFS embed.FS

type Handler struct {
	db        *db.DB
	cfg       *config.Config
	templates *template.Template
}

func NewHandler(database *db.DB, cfg *config.Config) *Handler {
	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"taskId": func(name string) string {
			for i := len(name) - 1; i >= 0; i-- {
				if name[i] == '/' {
					return name[i+1:]
				}
			}
			return name
		},
		"urlquery": url.QueryEscape,
	}).ParseFS(templatesFS, "templates/*.html"))
	return &Handler{
		db:        database,
		cfg:       cfg,
		templates: tmpl,
	}
}

type queuesPageData struct {
	Queues []*queues.Queue
	Error  string
}

func (h *Handler) ListQueues(w http.ResponseWriter, r *http.Request) {
	repo := queues.NewRepository(h.db.Conn())
	list, err := repo.ListAll()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.templates.ExecuteTemplate(w, "queues.html", &queuesPageData{
		Queues: list,
		Error:  r.URL.Query().Get("error"),
	})
}

func (h *Handler) CreateQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/ui/queues", http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	project := r.FormValue("project")
	location := r.FormValue("location")
	name := r.FormValue("name")
	if project == "" {
		project = h.cfg.DefaultProject
	}
	if location == "" {
		location = h.cfg.DefaultLocation
	}
	if name == "" {
		http.Redirect(w, r, "/ui/queues?error=name+required", http.StatusSeeOther)
		return
	}
	q := &queues.Queue{Project: project, Location: location, Name: name}
	repo := queues.NewRepository(h.db.Conn())
	if err := repo.Create(q); err != nil {
		http.Redirect(w, r, "/ui/queues?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/ui/queues", http.StatusSeeOther)
}

type queueDetailData struct {
	Queue   *queues.Queue
	Tasks   []*tasks.Task
	StatusFilter string
}

func (h *Handler) QueueDetail(w http.ResponseWriter, r *http.Request) {
	queueID := r.URL.Query().Get("queue")
	if queueID == "" {
		http.Error(w, "queue required", http.StatusBadRequest)
		return
	}
	queueRepo := queues.NewRepository(h.db.Conn())
	q, err := queueRepo.Get(queueID)
	if err != nil || q == nil {
		http.Error(w, "queue not found", http.StatusNotFound)
		return
	}
	statusFilter := r.URL.Query().Get("status")
	taskRepo := tasks.NewRepository(h.db.Conn())
	taskList, err := taskRepo.List(queueID, statusFilter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.templates.ExecuteTemplate(w, "queue_detail.html", &queueDetailData{
		Queue:        q,
		Tasks:        taskList,
		StatusFilter: statusFilter,
	})
}

func (h *Handler) DeleteQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/ui/queues", http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	queueID := r.FormValue("queue")
	if queueID == "" {
		http.Redirect(w, r, "/ui/queues", http.StatusSeeOther)
		return
	}
	repo := queues.NewRepository(h.db.Conn())
	_ = repo.Delete(queueID)
	http.Redirect(w, r, "/ui/queues", http.StatusSeeOther)
}

func (h *Handler) RetryTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/ui/queues", http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	queueID := r.FormValue("queue")
	taskID := r.FormValue("task")
	if queueID == "" || taskID == "" {
		http.Redirect(w, r, "/ui/queues", http.StatusSeeOther)
		return
	}
	taskName := queueID + "/tasks/" + taskID
	repo := tasks.NewRepository(h.db.Conn())
	_ = repo.UpdateRetry(taskName, 0, time.Now(), "")
	http.Redirect(w, r, "/ui/queue?queue="+url.QueryEscape(queueID), http.StatusSeeOther)
}

func (h *Handler) RunTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/ui/queues", http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	queueID := r.FormValue("queue")
	taskID := r.FormValue("task")
	if queueID == "" || taskID == "" {
		http.Redirect(w, r, "/ui/queues", http.StatusSeeOther)
		return
	}
	taskName := queueID + "/tasks/" + taskID
	repo := tasks.NewRepository(h.db.Conn())
	_ = repo.SetNextAttemptNow(taskName)
	http.Redirect(w, r, "/ui/queue?queue="+url.QueryEscape(queueID), http.StatusSeeOther)
}
