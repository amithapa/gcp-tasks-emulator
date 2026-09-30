package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const jobsBase = "/v1/projects/p/locations/l/jobs"

func doJSON(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestJobLifecycleREST(t *testing.T) {
	router, _ := setupTestAPI(t)
	hit := make(chan string, 4)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit <- r.Method }))
	defer target.Close()

	body := `{"name":"projects/p/locations/l/jobs/nightly","description":"d","schedule":"0 3 * * *","timeZone":"America/New_York",
		"httpTarget":{"uri":"` + target.URL + `","httpMethod":"PUT","headers":{"X-A":"1"},"body":"` + base64.StdEncoding.EncodeToString([]byte("hi")) + `"},
		"retryConfig":{"retryCount":2,"minBackoffDuration":"1s"},"attemptDeadline":"60s"}`
	w, job := doJSON(t, router, "POST", jobsBase, body)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if job["name"] != "projects/p/locations/l/jobs/nightly" || job["state"] != "ENABLED" || job["timeZone"] != "America/New_York" ||
		job["scheduleTime"] == nil || job["attemptDeadline"] != "60s" {
		t.Errorf("unexpected job: %v", job)
	}
	ht := job["httpTarget"].(map[string]any)
	if ht["httpMethod"] != "PUT" || ht["body"] != base64.StdEncoding.EncodeToString([]byte("hi")) {
		t.Errorf("httpTarget: %v", ht)
	}
	if rc := job["retryConfig"].(map[string]any); rc["retryCount"].(float64) != 2 || rc["minBackoffDuration"] != "1s" {
		t.Errorf("retryConfig: %v", rc)
	}

	if w, _ := doJSON(t, router, "POST", jobsBase, body); w.Code != 409 {
		t.Errorf("duplicate: %d", w.Code)
	}

	_, list := doJSON(t, router, "GET", jobsBase, "")
	if jobs := list["jobs"].([]any); len(jobs) != 1 {
		t.Errorf("list: %v", list)
	}
	if w, _ := doJSON(t, router, "GET", jobsBase+"/nightly", ""); w.Code != 200 {
		t.Errorf("get: %d", w.Code)
	}
	if w, _ := doJSON(t, router, "GET", jobsBase+"/nope", ""); w.Code != 404 {
		t.Errorf("get missing: %d", w.Code)
	}

	_, paused := doJSON(t, router, "POST", jobsBase+"/nightly:pause", "")
	if paused["state"] != "PAUSED" || paused["scheduleTime"] != nil {
		t.Errorf("pause: %v", paused)
	}
	_, resumed := doJSON(t, router, "POST", jobsBase+"/nightly:resume", "")
	if resumed["state"] != "ENABLED" || resumed["scheduleTime"] == nil {
		t.Errorf("resume: %v", resumed)
	}

	w, _ = doJSON(t, router, "POST", jobsBase+"/nightly:run", "")
	if w.Code != 200 {
		t.Fatalf("run: %d %s", w.Code, w.Body)
	}
	select {
	case m := <-hit:
		if m != "PUT" {
			t.Errorf("method %s", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not hit target")
	}

	w, upd := doJSON(t, router, "PATCH", jobsBase+"/nightly?updateMask=schedule,description", `{"schedule":"@daily","description":"new","timeZone":"Asia/Tokyo"}`)
	if w.Code != 200 || upd["schedule"] != "@daily" || upd["description"] != "new" || upd["timeZone"] != "America/New_York" {
		t.Errorf("patch: %d %v", w.Code, upd)
	}

	if w, _ := doJSON(t, router, "POST", jobsBase+"/nightly:explode", ""); w.Code != 404 {
		t.Errorf("unknown verb: %d", w.Code)
	}
	if w, _ := doJSON(t, router, "POST", jobsBase+"/missing:pause", ""); w.Code != 404 {
		t.Errorf("pause missing: %d", w.Code)
	}
	if w, _ := doJSON(t, router, "DELETE", jobsBase+"/nightly", ""); w.Code != 200 {
		t.Errorf("delete: %d", w.Code)
	}
	if w, _ := doJSON(t, router, "DELETE", jobsBase+"/nightly", ""); w.Code != 404 {
		t.Errorf("delete again: %d", w.Code)
	}
}

func TestJobValidationREST(t *testing.T) {
	router, _ := setupTestAPI(t)
	cases := map[string]string{
		"bad cron":     `{"name":"a","schedule":"nope","httpTarget":{"uri":"http://x.test"}}`,
		"bad tz":       `{"name":"a","schedule":"* * * * *","timeZone":"Nowhere/Land","httpTarget":{"uri":"http://x.test"}}`,
		"no target":    `{"name":"a","schedule":"* * * * *"}`,
		"bad uri":      `{"name":"a","schedule":"* * * * *","httpTarget":{"uri":"not-a-url"}}`,
		"bad method":   `{"name":"a","schedule":"* * * * *","httpTarget":{"uri":"http://x.test","httpMethod":"FETCH"}}`,
		"no name":      `{"schedule":"* * * * *","httpTarget":{"uri":"http://x.test"}}`,
		"wrong parent": `{"name":"projects/other/locations/l/jobs/a","schedule":"* * * * *","httpTarget":{"uri":"http://x.test"}}`,
		"bad duration": `{"name":"a","schedule":"* * * * *","httpTarget":{"uri":"http://x.test"},"attemptDeadline":"abc"}`,
		"bad json":     `{`,
	}
	for name, body := range cases {
		w, out := doJSON(t, router, "POST", jobsBase, body)
		if w.Code != 400 {
			t.Errorf("%s: got %d %s", name, w.Code, w.Body)
			continue
		}
		if e := out["error"].(map[string]any); e["code"] != "INVALID_ARGUMENT" || e["message"] == "" {
			t.Errorf("%s: %v", name, e)
		}
	}
}
