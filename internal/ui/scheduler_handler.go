package ui

import (
	"embed"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/scheduler"
)

// The jobs template is also picked up by templates/*.html in handler.go, so it
// must only use built-in template functions and uniquely named definitions.
// Pages share the head/nav/foot partials from layout.html.
//
//go:embed templates/jobs.html templates/layout.html
var jobsTemplateFS embed.FS

// SchedulerHandler serves the Cloud Scheduler pages of the admin UI.
type SchedulerHandler struct {
	svc       *scheduler.Service
	cfg       *config.Config
	templates *template.Template
}

func NewSchedulerHandler(database *db.DB, cfg *config.Config) *SchedulerHandler {
	return &SchedulerHandler{
		svc:       scheduler.ForDB(database.Conn()),
		cfg:       cfg,
		templates: template.Must(template.New("").ParseFS(jobsTemplateFS, "templates/layout.html", "templates/jobs.html")),
	}
}

type jobView struct {
	ID          string
	Name        string
	Project     string
	Location    string
	Description string
	Schedule    string
	TimeZone    string
	State       string
	Enabled     bool
	Method      string
	URL         string
	Headers     string // "Key: Value" per line
	Body        string
	Deadline    string
	RetryCount  int
	MinBackoff  string
	MaxBackoff  string
	MaxRetryDur string
	MaxDoubling int
	NextRun     string
	LastAttempt string
	LastStatus  string
	LastError   string
	Created     string
}

type jobsPageData struct {
	Jobs            []*jobView
	Error           string
	DefaultProject  string
	DefaultLocation string
	Now             string
}

type jobDetailData struct {
	Job   *jobView
	Error string
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

func toJobView(j *scheduler.Job) *jobView {
	var hs []string
	for k, v := range j.Headers {
		hs = append(hs, k+": "+v)
	}
	sort.Strings(hs)
	status := j.LastStatus
	if status == "" {
		status = "-"
	}
	return &jobView{
		ID: j.ID, Name: j.Name, Project: j.Project, Location: j.Location,
		Description: j.Description, Schedule: j.Schedule, TimeZone: j.TimeZone,
		State: j.State, Enabled: j.State == scheduler.StateEnabled,
		Method: j.HTTPMethod, URL: j.URL, Headers: strings.Join(hs, "\n"), Body: string(j.Body),
		Deadline: j.AttemptDeadline.String(), RetryCount: j.Retry.RetryCount,
		MinBackoff: j.Retry.MinBackoff.String(), MaxBackoff: j.Retry.MaxBackoff.String(),
		MaxRetryDur: j.Retry.MaxRetryDuration.String(), MaxDoubling: j.Retry.MaxDoublings,
		NextRun: fmtTime(j.NextRunAt), LastAttempt: fmtTime(j.LastAttemptAt),
		LastStatus: status, LastError: j.LastError, Created: fmtTime(j.CreatedAt),
	}
}

func parseHeaderLines(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || strings.TrimSpace(k) == "" {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func errText(err error) string {
	var ve *scheduler.ValidationError
	if errors.As(err, &ve) {
		return ve.Msg
	}
	return err.Error()
}

func redirectJobs(w http.ResponseWriter, r *http.Request, err error) {
	target := "/ui/jobs"
	if err != nil {
		target += "?error=" + url.QueryEscape(errText(err))
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (h *SchedulerHandler) ListJobs(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Repo().ListAll()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := &jobsPageData{
		Error:           r.URL.Query().Get("error"),
		DefaultProject:  h.cfg.DefaultProject,
		DefaultLocation: h.cfg.DefaultLocation,
		Now:             fmtTime(time.Now()),
	}
	for _, j := range list {
		data.Jobs = append(data.Jobs, toJobView(j))
	}
	_ = h.templates.ExecuteTemplate(w, "jobs.html", data)
}

func (h *SchedulerHandler) CreateJob(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	project := strings.TrimSpace(r.FormValue("project"))
	location := strings.TrimSpace(r.FormValue("location"))
	if project == "" {
		project = h.cfg.DefaultProject
	}
	if location == "" {
		location = h.cfg.DefaultLocation
	}
	retry, _ := strconv.Atoi(r.FormValue("retry_count"))
	j := &scheduler.Job{
		Project: project, Location: location, Name: strings.TrimSpace(r.FormValue("name")),
		Description: r.FormValue("description"), Schedule: strings.TrimSpace(r.FormValue("schedule")),
		TimeZone: strings.TrimSpace(r.FormValue("time_zone")), HTTPMethod: r.FormValue("http_method"),
		URL: strings.TrimSpace(r.FormValue("url")), Headers: parseHeaderLines(r.FormValue("headers")),
	}
	j.Retry.RetryCount = retry
	if b := r.FormValue("body"); b != "" {
		j.Body = []byte(b)
	}
	_, err := h.svc.Create(j)
	redirectJobs(w, r, err)
}

func (h *SchedulerHandler) JobDetail(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("job")
	j, err := h.svc.Get(id)
	if err != nil {
		if errors.Is(err, scheduler.ErrNotFound) {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = h.templates.ExecuteTemplate(w, "scheduler_job_detail", &jobDetailData{
		Job: toJobView(j), Error: r.URL.Query().Get("error"),
	})
}

func detailRedirect(w http.ResponseWriter, r *http.Request, id string, err error) {
	target := "/ui/job?job=" + url.QueryEscape(id)
	if err != nil {
		target += "&error=" + url.QueryEscape(errText(err))
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (h *SchedulerHandler) UpdateJob(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id := r.FormValue("job")
	retry, _ := strconv.Atoi(r.FormValue("retry_count"))
	patch := &scheduler.Job{
		Description: r.FormValue("description"), Schedule: strings.TrimSpace(r.FormValue("schedule")),
		TimeZone: strings.TrimSpace(r.FormValue("time_zone")), HTTPMethod: r.FormValue("http_method"),
		URL: strings.TrimSpace(r.FormValue("url")), Headers: parseHeaderLines(r.FormValue("headers")),
	}
	patch.Retry.RetryCount = retry
	if b := r.FormValue("body"); b != "" {
		patch.Body = []byte(b)
	}
	_, err := h.svc.Update(id, patch, []string{"description", "schedule", "time_zone", "http_target", "retry_config.retry_count"})
	detailRedirect(w, r, id, err)
}

func (h *SchedulerHandler) act(w http.ResponseWriter, r *http.Request, fn func(string) (*scheduler.Job, error)) {
	_ = r.ParseForm()
	_, err := fn(r.FormValue("job"))
	if r.FormValue("from") == "detail" {
		detailRedirect(w, r, r.FormValue("job"), err)
		return
	}
	redirectJobs(w, r, err)
}

func (h *SchedulerHandler) PauseJob(w http.ResponseWriter, r *http.Request) { h.act(w, r, h.svc.Pause) }
func (h *SchedulerHandler) ResumeJob(w http.ResponseWriter, r *http.Request) {
	h.act(w, r, h.svc.Resume)
}
func (h *SchedulerHandler) RunJob(w http.ResponseWriter, r *http.Request) { h.act(w, r, h.svc.Run) }

func (h *SchedulerHandler) DeleteJob(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	redirectJobs(w, r, h.svc.Delete(r.FormValue("job")))
}
