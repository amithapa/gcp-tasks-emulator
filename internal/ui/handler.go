package ui

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/queues"
	"cloud-tasks-emulator/internal/tasks"
)

// templatesFS holds every admin UI page. All pages are parsed into one set, so
// template names must be unique across files. Pages share layout.html: see the
// comment at the top of that file.
//
//go:embed templates/*.html
var templatesFS embed.FS

const pageSize = 50

var statusOrder = []string{tasks.StatusPending, tasks.StatusRunning, tasks.StatusCompleted, tasks.StatusFailed}

type Handler struct {
	db        *db.DB
	cfg       *config.Config
	templates *template.Template
}

func NewHandler(database *db.DB, cfg *config.Config) *Handler {
	return &Handler{db: database, cfg: cfg, templates: parseTemplates()}
}

// parseTemplates parses every embedded page with the shared function map.
func parseTemplates() *template.Template {
	return template.Must(template.New("").Funcs(template.FuncMap{
		"taskId":  taskIDOf,
		"timeTag": timeTag,
	}).ParseFS(templatesFS, "templates/*.html"))
}

func taskIDOf(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

// timeTag renders a <time> element that layout.html's script upgrades to local
// time plus a relative label. The text content is a UTC fallback.
func timeTag(t time.Time) template.HTML {
	if t.IsZero() {
		return template.HTML(`<span class="muted">&mdash;</span>`)
	}
	u := t.UTC()
	return template.HTML(fmt.Sprintf(`<time class="ts" datetime="%s">%s UTC</time>`,
		u.Format(time.RFC3339), u.Format("2006-01-02 15:04:05")))
}

// render executes a template into a buffer first so a template error yields a
// clean 500 instead of a half-written page.
func (h *Handler) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := h.templates.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (h *Handler) message(w http.ResponseWriter, status int, title, msg string) {
	h.render(w, status, "message_page", map[string]string{"Title": title, "Message": msg})
}

// safeNext returns a same-site redirect target from the "next" form value,
// falling back to def. Only absolute paths under /ui/ are accepted.
func safeNext(r *http.Request, def string) string {
	next := r.FormValue("next")
	if strings.HasPrefix(next, "/ui/") && !strings.HasPrefix(next, "//") && !strings.Contains(next, "\\") {
		return next
	}
	return def
}

// redirectWith redirects to target with a notice or error message attached.
func redirectWith(w http.ResponseWriter, r *http.Request, target, key, msg string) {
	if msg != "" {
		u, err := url.Parse(target)
		if err == nil {
			q := u.Query()
			q.Del("notice")
			q.Del("error")
			q.Set(key, msg)
			u.RawQuery = q.Encode()
			target = u.String()
		}
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/queues", http.StatusSeeOther)
}

// StatusCounts is the number of tasks per status.
type StatusCounts struct {
	Pending, Running, Completed, Failed, Total int
}

func newStatusCounts(m map[string]int) StatusCounts {
	c := StatusCounts{
		Pending:   m[tasks.StatusPending],
		Running:   m[tasks.StatusRunning],
		Completed: m[tasks.StatusCompleted],
		Failed:    m[tasks.StatusFailed],
	}
	for _, n := range m {
		c.Total += n
	}
	return c
}

type queueRow struct {
	Queue  *queues.Queue
	State  string
	Counts StatusCounts
}

type queuesPageData struct {
	Queues          []queueRow
	Error, Notice   string
	DefaultProject  string
	DefaultLocation string
}

func queueState(q *queues.Queue) string {
	if q.Paused() {
		return queues.StatePaused
	}
	return queues.StateRunning
}

func (h *Handler) ListQueues(w http.ResponseWriter, r *http.Request) {
	list, err := queues.NewRepository(h.db.Conn()).ListAll()
	if err != nil {
		h.message(w, http.StatusInternalServerError, "Error", err.Error())
		return
	}
	counts, err := tasks.NewRepository(h.db.Conn()).CountsByQueue()
	if err != nil {
		h.message(w, http.StatusInternalServerError, "Error", err.Error())
		return
	}
	data := &queuesPageData{
		Error:           r.URL.Query().Get("error"),
		Notice:          r.URL.Query().Get("notice"),
		DefaultProject:  h.cfg.DefaultProject,
		DefaultLocation: h.cfg.DefaultLocation,
	}
	for _, q := range list {
		data.Queues = append(data.Queues, queueRow{Queue: q, State: queueState(q), Counts: newStatusCounts(counts[q.ID])})
	}
	h.render(w, http.StatusOK, "queues.html", data)
}

func (h *Handler) CreateQueue(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	project := strings.TrimSpace(r.FormValue("project"))
	location := strings.TrimSpace(r.FormValue("location"))
	name := strings.TrimSpace(r.FormValue("name"))
	if project == "" {
		project = h.cfg.DefaultProject
	}
	if location == "" {
		location = h.cfg.DefaultLocation
	}
	if name == "" {
		redirectWith(w, r, "/ui/queues", "error", "Queue name is required.")
		return
	}
	if err := queues.ValidateName(name); err != nil {
		redirectWith(w, r, "/ui/queues", "error", err.Error())
		return
	}
	q := &queues.Queue{Project: project, Location: location, Name: name, RateLimits: &queues.RateLimits{}}
	q.RateLimits.MaxDispatchesPerSecond, _ = strconv.Atoi(r.FormValue("max_dispatches_per_second"))
	q.RateLimits.MaxConcurrentDispatches, _ = strconv.Atoi(r.FormValue("max_concurrent_dispatches"))
	if err := queues.NewRepository(h.db.Conn()).Create(q); err != nil {
		msg := err.Error()
		if errors.Is(err, queues.ErrAlreadyExists) {
			msg = "Queue " + q.ID + " already exists."
		}
		redirectWith(w, r, "/ui/queues", "error", msg)
		return
	}
	redirectWith(w, r, "/ui/queues", "notice", "Created queue "+name+".")
}

type filterTab struct {
	Label  string
	URL    string
	Count  int
	Active bool
}

type queueDetailData struct {
	Queue         *queues.Queue
	State         string
	Tasks         []*tasks.Task
	StatusFilter  string
	StatusLabel   string
	Counts        StatusCounts
	Tabs          []filterTab
	Total         int // tasks matching the filter
	From, To      int
	PrevURL       string
	NextURL       string
	CurrentURL    string
	Error, Notice string
}

func queueURL(queueID, status string, page int) string {
	v := url.Values{"queue": []string{queueID}}
	if status != "" {
		v.Set("status", status)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	return "/ui/queue?" + v.Encode()
}

func (h *Handler) QueueDetail(w http.ResponseWriter, r *http.Request) {
	queueID := r.URL.Query().Get("queue")
	if queueID == "" {
		h.message(w, http.StatusBadRequest, "Bad request", "A queue is required.")
		return
	}
	q, err := queues.NewRepository(h.db.Conn()).Get(queueID)
	if err != nil {
		h.message(w, http.StatusInternalServerError, "Error", err.Error())
		return
	}
	if q == nil {
		h.message(w, http.StatusNotFound, "Queue not found", "There is no queue "+queueID+".")
		return
	}

	statusFilter := strings.ToUpper(r.URL.Query().Get("status"))
	valid := false
	for _, s := range statusOrder {
		valid = valid || s == statusFilter
	}
	if !valid {
		statusFilter = ""
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}

	taskRepo := tasks.NewRepository(h.db.Conn())
	all, err := taskRepo.CountsByQueue()
	if err != nil {
		h.message(w, http.StatusInternalServerError, "Error", err.Error())
		return
	}
	counts := newStatusCounts(all[queueID])
	total := counts.Total
	if statusFilter != "" {
		total = all[queueID][statusFilter]
	}
	// Clamp the page in case tasks were deleted since the link was made.
	if maxPage := (total + pageSize - 1) / pageSize; maxPage > 0 && page > maxPage {
		page = maxPage
	}

	taskList, err := taskRepo.ListPage(queueID, statusFilter, pageSize, (page-1)*pageSize)
	if err != nil {
		h.message(w, http.StatusInternalServerError, "Error", err.Error())
		return
	}

	data := &queueDetailData{
		Queue:        q,
		State:        queueState(q),
		Tasks:        taskList,
		StatusFilter: statusFilter,
		StatusLabel:  strings.ToLower(statusFilter),
		Counts:       counts,
		Total:        total,
		CurrentURL:   queueURL(queueID, statusFilter, page),
		Error:        r.URL.Query().Get("error"),
		Notice:       r.URL.Query().Get("notice"),
	}
	data.Tabs = append(data.Tabs, filterTab{"All", queueURL(queueID, "", 1), counts.Total, statusFilter == ""})
	for _, s := range statusOrder {
		data.Tabs = append(data.Tabs, filterTab{
			Label: strings.ToUpper(s[:1]) + strings.ToLower(s[1:]), URL: queueURL(queueID, s, 1),
			Count: all[queueID][s], Active: statusFilter == s,
		})
	}
	if len(taskList) > 0 {
		data.From = (page-1)*pageSize + 1
		data.To = data.From + len(taskList) - 1
	}
	if page > 1 {
		data.PrevURL = queueURL(queueID, statusFilter, page-1)
	}
	if data.To < total {
		data.NextURL = queueURL(queueID, statusFilter, page+1)
	}
	h.render(w, http.StatusOK, "queue_detail.html", data)
}

type taskDetailData struct {
	Queue      *queues.Queue
	Task       *tasks.Task
	TaskID     string
	Body       string
	BodyNote   string
	CurrentURL string
	Notice     string
}

const maxBodyDisplay = 256 << 10

// renderBody returns a printable form of a task payload and a short note on
// how it was rendered. Everything is plain text; the template escapes it.
func renderBody(b []byte) (text, note string) {
	if len(b) == 0 {
		return "", ""
	}
	truncated := false
	if len(b) > maxBodyDisplay {
		b, truncated = b[:maxBodyDisplay], true
	}
	switch {
	case !utf8.Valid(b):
		text, note = base64.StdEncoding.EncodeToString(b), fmt.Sprintf("binary, base64-encoded, %d bytes", len(b))
	case json.Valid(b):
		var out bytes.Buffer
		if json.Indent(&out, b, "", "  ") == nil {
			text, note = out.String(), "JSON"
		} else {
			text, note = string(b), "text"
		}
	default:
		text, note = string(b), "text"
	}
	if truncated {
		note += ", truncated to 256 KiB"
	}
	return text, note
}

func (h *Handler) TaskDetail(w http.ResponseWriter, r *http.Request) {
	queueID := r.URL.Query().Get("queue")
	taskID := r.URL.Query().Get("task")
	if queueID == "" || taskID == "" {
		h.message(w, http.StatusBadRequest, "Bad request", "A queue and a task are required.")
		return
	}
	q, err := queues.NewRepository(h.db.Conn()).Get(queueID)
	if err != nil {
		h.message(w, http.StatusInternalServerError, "Error", err.Error())
		return
	}
	if q == nil {
		h.message(w, http.StatusNotFound, "Queue not found", "There is no queue "+queueID+".")
		return
	}
	t, err := tasks.NewRepository(h.db.Conn()).Get(queueID + "/tasks/" + taskID)
	if err != nil {
		h.message(w, http.StatusInternalServerError, "Error", err.Error())
		return
	}
	if t == nil {
		h.message(w, http.StatusNotFound, "Task not found", "Task "+taskID+" does not exist (it may have been deleted or purged).")
		return
	}
	body, note := renderBody(t.Body)
	v := url.Values{"queue": []string{queueID}, "task": []string{taskID}}
	h.render(w, http.StatusOK, "task_detail.html", &taskDetailData{
		Queue: q, Task: t, TaskID: taskID, Body: body, BodyNote: note,
		CurrentURL: "/ui/task?" + v.Encode(), Notice: r.URL.Query().Get("notice"),
	})
}

// queueAction runs fn against the queue named in the form and redirects back.
func (h *Handler) queueAction(w http.ResponseWriter, r *http.Request, def string, fn func(repo *queues.Repository, id string) (string, error)) {
	_ = r.ParseForm()
	queueID := r.FormValue("queue")
	if queueID == "" {
		http.Redirect(w, r, "/ui/queues", http.StatusSeeOther)
		return
	}
	next := safeNext(r, def)
	msg, err := fn(queues.NewRepository(h.db.Conn()), queueID)
	if err != nil {
		redirectWith(w, r, next, "error", err.Error())
		return
	}
	redirectWith(w, r, next, "notice", msg)
}

func (h *Handler) DeleteQueue(w http.ResponseWriter, r *http.Request) {
	h.queueAction(w, r, "/ui/queues", func(repo *queues.Repository, id string) (string, error) {
		if err := repo.Delete(id); err != nil {
			return "", err
		}
		return "Deleted queue.", nil
	})
}

func (h *Handler) PauseQueue(w http.ResponseWriter, r *http.Request) {
	h.queueAction(w, r, "/ui/queues", func(repo *queues.Repository, id string) (string, error) {
		return "Queue paused.", repo.SetState(id, queues.StatePaused)
	})
}

func (h *Handler) ResumeQueue(w http.ResponseWriter, r *http.Request) {
	h.queueAction(w, r, "/ui/queues", func(repo *queues.Repository, id string) (string, error) {
		return "Queue resumed.", repo.SetState(id, queues.StateRunning)
	})
}

func (h *Handler) PurgeQueue(w http.ResponseWriter, r *http.Request) {
	h.queueAction(w, r, "/ui/queues", func(repo *queues.Repository, id string) (string, error) {
		n, err := repo.Purge(id)
		return fmt.Sprintf("Purged %d task(s).", n), err
	})
}

// taskAction runs fn against the task named in the form and redirects back.
func (h *Handler) taskAction(w http.ResponseWriter, r *http.Request, fn func(repo *tasks.Repository, name string) (string, error)) {
	_ = r.ParseForm()
	queueID := r.FormValue("queue")
	taskID := r.FormValue("task")
	if queueID == "" || taskID == "" {
		http.Redirect(w, r, "/ui/queues", http.StatusSeeOther)
		return
	}
	next := safeNext(r, "/ui/queue?queue="+url.QueryEscape(queueID))
	msg, err := fn(tasks.NewRepository(h.db.Conn()), queueID+"/tasks/"+taskID)
	if err != nil {
		redirectWith(w, r, next, "error", err.Error())
		return
	}
	redirectWith(w, r, next, "notice", msg)
}

// RetryTask and RunTask both make the task due now; FAILED and COMPLETED
// tasks are re-queued from scratch (see tasks.Repository.SetNextAttemptNow).
func (h *Handler) RetryTask(w http.ResponseWriter, r *http.Request) { h.RunTask(w, r) }

func (h *Handler) RunTask(w http.ResponseWriter, r *http.Request) {
	h.taskAction(w, r, func(repo *tasks.Repository, name string) (string, error) {
		return "Task scheduled to run now.", repo.SetNextAttemptNow(name)
	})
}

func (h *Handler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	h.taskAction(w, r, func(repo *tasks.Repository, name string) (string, error) {
		return "Task deleted.", repo.Delete(name)
	})
}
