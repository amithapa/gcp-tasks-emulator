package ui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/queues"
	"cloud-tasks-emulator/internal/tasks"
)

type uiEnv struct {
	mux http.Handler
	db  *db.DB
	q   *queues.Queue
}

func newUIEnv(t *testing.T) *uiEnv {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "ui.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	cfg, _ := config.Load()
	h := NewHandler(database, cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui/queues", h.ListQueues)
	mux.HandleFunc("POST /ui/queues", h.CreateQueue)
	mux.HandleFunc("GET /ui/queue", h.QueueDetail)
	mux.HandleFunc("POST /ui/queue/delete", h.DeleteQueue)
	mux.HandleFunc("POST /ui/queue/tasks/run", h.RunTask)
	mux.HandleFunc("POST /ui/queue/tasks/retry", h.RetryTask)
	mux.HandleFunc("POST /ui/queue/tasks/delete", h.DeleteTask)
	mux.HandleFunc("POST /ui/queue/pause", h.PauseQueue)
	mux.HandleFunc("POST /ui/queue/resume", h.ResumeQueue)
	mux.HandleFunc("POST /ui/queue/purge", h.PurgeQueue)
	mux.HandleFunc("GET /ui/task", h.TaskDetail)

	q := &queues.Queue{Project: "p", Location: "l", Name: "q"}
	if err := queues.NewRepository(database.Conn()).Create(q); err != nil {
		t.Fatal(err)
	}
	return &uiEnv{mux: mux, db: database, q: q}
}

func (e *uiEnv) addTask(t *testing.T, id string, mut func(*tasks.Task)) string {
	t.Helper()
	now := time.Now()
	tk := &tasks.Task{
		Name: e.q.ID + "/tasks/" + id, QueueID: e.q.ID, HTTPMethod: "POST", URL: "http://localhost:9/hook",
		ScheduleTime: now, NextAttemptAt: now, Status: tasks.StatusPending, MaxRetries: 5, DispatchDeadline: 30,
	}
	if mut != nil {
		mut(tk)
	}
	if err := tasks.NewRepository(e.db.Conn()).Create(tk); err != nil {
		t.Fatal(err)
	}
	return tk.Name
}

func (e *uiEnv) get(path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func (e *uiEnv) post(path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

const xss = `<script>alert(1)</script>`

// Payloads, headers and errors are attacker-influenced (anyone who can create
// a task). None of it may reach the page as live markup.
func TestPagesEscapeTaskContent(t *testing.T) {
	e := newUIEnv(t)
	e.addTask(t, "evil", func(tk *tasks.Task) {
		tk.Body = []byte(`{"x":"` + xss + `"}`)
		tk.Headers = map[string]string{"X-" + xss: xss}
		tk.LastError = xss
		tk.URL = "http://localhost:9/" + xss
	})
	for _, path := range []string{
		"/ui/queue?queue=" + url.QueryEscape(e.q.ID),
		"/ui/task?queue=" + url.QueryEscape(e.q.ID) + "&task=evil",
		"/ui/queues?error=" + url.QueryEscape(xss) + "&notice=" + url.QueryEscape(xss),
	} {
		w := e.get(path)
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), xss) {
			t.Errorf("%s: unescaped payload in page", path)
		}
		if strings.Contains(w.Body.String(), "innerHTML") {
			t.Errorf("%s: page must not copy content via innerHTML", path)
		}
	}
	// It is present, escaped, on the detail page.
	body := e.get("/ui/task?queue=" + url.QueryEscape(e.q.ID) + "&task=evil").Body.String()
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("escaped payload missing from task detail")
	}
}

func TestQueueListShowsCountsAndState(t *testing.T) {
	e := newUIEnv(t)
	e.addTask(t, "a", nil)
	e.addTask(t, "b", func(tk *tasks.Task) { tk.Status = tasks.StatusFailed })
	e.addTask(t, "c", func(tk *tasks.Task) { tk.Status = tasks.StatusFailed })
	if w := e.post("/ui/queue/pause", url.Values{"queue": []string{e.q.ID}}); w.Code != http.StatusSeeOther {
		t.Fatalf("pause: %d", w.Code)
	}
	body := e.get("/ui/queues").Body.String()
	for _, want := range []string{"1 pending", "2 failed", "badge-PAUSED", `href="/ui/jobs"`, "Resume"} {
		if !strings.Contains(body, want) {
			t.Errorf("queue list missing %q", want)
		}
	}
}

func TestQueueDetailPaginationAndFilter(t *testing.T) {
	e := newUIEnv(t)
	n := pageSize + 5
	for i := 0; i < n; i++ {
		e.addTask(t, fmt.Sprintf("t%03d", i), nil)
	}
	e.addTask(t, "failed-one", func(tk *tasks.Task) { tk.Status = tasks.StatusFailed })

	base := "/ui/queue?queue=" + url.QueryEscape(e.q.ID)
	p1 := e.get(base).Body.String()
	if !strings.Contains(p1, fmt.Sprintf("of %d", n+1)) || !strings.Contains(p1, "page=2") {
		t.Error("page 1 missing total or next link")
	}
	if got := strings.Count(p1, "/ui/queue/tasks/delete"); got != pageSize {
		t.Errorf("page 1 has %d rows, want %d", got, pageSize)
	}
	p2 := e.get(base + "&page=2").Body.String()
	if got := strings.Count(p2, "/ui/queue/tasks/delete"); got != 6 {
		t.Errorf("page 2 has %d rows, want 6", got)
	}
	if strings.Contains(p2, `rel="next"`) {
		t.Error("last page must not link to a next page")
	}
	// Out-of-range pages are clamped rather than empty.
	if got := strings.Count(e.get(base+"&page=99").Body.String(), "/ui/queue/tasks/delete"); got != 6 {
		t.Errorf("clamped page has %d rows", got)
	}
	failed := e.get(base + "&status=failed").Body.String()
	if strings.Count(failed, "/ui/queue/tasks/delete") != 1 || !strings.Contains(failed, "failed-one") {
		t.Error("status filter did not narrow the list")
	}
	if !strings.Contains(e.get(base+"&status=COMPLETED").Body.String(), "No completed tasks") {
		t.Error("missing empty state for filter")
	}
}

func TestTaskActions(t *testing.T) {
	e := newUIEnv(t)
	name := e.addTask(t, "a", func(tk *tasks.Task) {
		tk.Status = tasks.StatusFailed
		tk.RetryCount = 5
		tk.LastError = "boom"
		tk.NextAttemptAt = time.Now().Add(time.Hour)
	})
	repo := tasks.NewRepository(e.db.Conn())

	w := e.post("/ui/queue/tasks/retry", url.Values{"queue": []string{e.q.ID}, "task": []string{"a"}, "next": []string{"/ui/task?x=1"}})
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/ui/task?") {
		t.Fatalf("retry redirect: %d %q", w.Code, w.Header().Get("Location"))
	}
	got, _ := repo.Get(name)
	if got.Status != tasks.StatusPending || got.RetryCount != 0 || got.LastError != "" {
		t.Fatalf("after retry: %+v", got)
	}
	if due, _ := repo.ListPending(10); len(due) != 1 {
		t.Fatal("retried task is not due (Run now on a FAILED task used to do nothing)")
	}

	e.post("/ui/queue/tasks/delete", url.Values{"queue": []string{e.q.ID}, "task": []string{"a"}})
	if got, _ := repo.Get(name); got != nil {
		t.Fatal("task not deleted")
	}
	// Deleting again surfaces an error message instead of failing silently.
	w = e.post("/ui/queue/tasks/delete", url.Values{"queue": []string{e.q.ID}, "task": []string{"a"}})
	if !strings.Contains(w.Header().Get("Location"), "error=") {
		t.Fatalf("missing error redirect: %q", w.Header().Get("Location"))
	}
}

func TestPurgeAndDeleteQueue(t *testing.T) {
	e := newUIEnv(t)
	e.addTask(t, "a", nil)
	e.addTask(t, "b", nil)
	w := e.post("/ui/queue/purge", url.Values{"queue": []string{e.q.ID}, "next": []string{"/ui/queues"}})
	if !strings.Contains(w.Header().Get("Location"), "Purged+2") {
		t.Fatalf("purge location: %q", w.Header().Get("Location"))
	}
	e.addTask(t, "c", nil)
	e.post("/ui/queue/delete", url.Values{"queue": []string{e.q.ID}})
	if n, _ := tasks.NewRepository(e.db.Conn()).Count(e.q.ID, ""); n != 0 {
		t.Fatalf("%d tasks left after queue delete", n)
	}
	if !strings.Contains(e.get("/ui/queues").Body.String(), "No queues yet") {
		t.Error("missing empty state")
	}
}

func TestCreateQueueValidation(t *testing.T) {
	e := newUIEnv(t)
	w := e.post("/ui/queues", url.Values{"name": []string{"bad name"}})
	if !strings.Contains(w.Header().Get("Location"), "error=") {
		t.Fatalf("invalid name accepted: %q", w.Header().Get("Location"))
	}
	w = e.post("/ui/queues", url.Values{"project": []string{"p"}, "location": []string{"l"}, "name": []string{"q"}})
	if !strings.Contains(w.Header().Get("Location"), "already+exists") {
		t.Fatalf("duplicate: %q", w.Header().Get("Location"))
	}
	w = e.post("/ui/queues", url.Values{"name": []string{"fresh"}, "max_dispatches_per_second": []string{"7"}, "max_concurrent_dispatches": []string{"3"}})
	if !strings.Contains(w.Header().Get("Location"), "notice=") {
		t.Fatalf("create: %q", w.Header().Get("Location"))
	}
	cfg, _ := config.Load()
	q, _ := queues.NewRepository(e.db.Conn()).Get("projects/" + cfg.DefaultProject + "/locations/" + cfg.DefaultLocation + "/queues/fresh")
	if q == nil || q.RateLimits.MaxDispatchesPerSecond != 7 || q.RateLimits.MaxConcurrentDispatches != 3 {
		t.Fatalf("queue = %+v", q)
	}
}

func TestSafeNext(t *testing.T) {
	for next, want := range map[string]string{
		"/ui/queue?queue=x": "/ui/queue?queue=x",
		"https://evil.com":  "/ui/queues",
		"//evil.com":        "/ui/queues",
		"/other":            "/ui/queues",
		"":                  "/ui/queues",
		"/ui/\\evil":        "/ui/queues",
	} {
		r := httptest.NewRequest("POST", "/x", strings.NewReader(url.Values{"next": []string{next}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if got := safeNext(r, "/ui/queues"); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", next, got, want)
		}
	}
}

func TestRenderBody(t *testing.T) {
	if text, note := renderBody([]byte(`{"a":1}`)); note != "JSON" || !strings.Contains(text, "\n  \"a\": 1") {
		t.Errorf("json: %q %q", text, note)
	}
	if text, note := renderBody([]byte("plain")); text != "plain" || note != "text" {
		t.Errorf("text: %q %q", text, note)
	}
	if _, note := renderBody([]byte{0xff, 0xfe, 0x00}); !strings.HasPrefix(note, "binary") {
		t.Errorf("binary note: %q", note)
	}
	if _, note := renderBody(make([]byte, maxBodyDisplay+1)); !strings.Contains(note, "truncated") {
		t.Errorf("truncation note: %q", note)
	}
}

func TestMissingQueueAndTask(t *testing.T) {
	e := newUIEnv(t)
	if w := e.get("/ui/queue?queue=nope"); w.Code != 404 {
		t.Errorf("missing queue: %d", w.Code)
	}
	if w := e.get("/ui/task?queue=" + url.QueryEscape(e.q.ID) + "&task=nope"); w.Code != 404 {
		t.Errorf("missing task: %d", w.Code)
	}
	if w := e.get("/ui/queue"); w.Code != 400 {
		t.Errorf("no queue param: %d", w.Code)
	}
}
