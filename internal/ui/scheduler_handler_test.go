package ui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
)

func TestSchedulerUI(t *testing.T) {
	database, err := db.New(filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cfg, _ := config.Load()
	h := NewSchedulerHandler(database, cfg)
	// The shared handler parses templates/*.html and must still accept jobs.html.
	_ = NewHandler(database, cfg)

	post := func(fn http.HandlerFunc, form url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		fn(w, req)
		return w
	}
	w := post(h.CreateJob, url.Values{"name": []string{"ui-job"}, "schedule": []string{"*/5 * * * *"}, "url": []string{"http://x.test/h"}, "headers": []string{"X-A: b"}})
	if w.Code != 303 || strings.Contains(w.Header().Get("Location"), "error") {
		t.Fatalf("create: %d %s", w.Code, w.Header().Get("Location"))
	}
	w = post(h.CreateJob, url.Values{"name": []string{"bad"}, "schedule": []string{"nope"}, "url": []string{"http://x.test/h"}})
	if !strings.Contains(w.Header().Get("Location"), "error=") {
		t.Errorf("expected error redirect, got %s", w.Header().Get("Location"))
	}

	rec := httptest.NewRecorder()
	h.ListJobs(rec, httptest.NewRequest("GET", "/ui/jobs", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ui-job") || !strings.Contains(rec.Body.String(), "ENABLED") {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}

	id := "projects/local-project/locations/us-central1/jobs/ui-job"
	post(h.PauseJob, url.Values{"job": []string{id}})
	rec = httptest.NewRecorder()
	h.JobDetail(rec, httptest.NewRequest("GET", "/ui/job?job="+url.QueryEscape(id), nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "PAUSED") || !strings.Contains(rec.Body.String(), "X-A: b") {
		t.Errorf("detail: %d %s", rec.Code, rec.Body)
	}
	w = post(h.UpdateJob, url.Values{"job": []string{id}, "schedule": []string{"@hourly"}, "url": []string{"http://x.test/h2"}, "http_method": []string{"GET"}})
	if strings.Contains(w.Header().Get("Location"), "error") {
		t.Errorf("update: %s", w.Header().Get("Location"))
	}
	post(h.DeleteJob, url.Values{"job": []string{id}})
	rec = httptest.NewRecorder()
	h.JobDetail(rec, httptest.NewRequest("GET", "/ui/job?job="+url.QueryEscape(id), nil))
	if rec.Code != 404 {
		t.Errorf("deleted detail: %d", rec.Code)
	}
}
