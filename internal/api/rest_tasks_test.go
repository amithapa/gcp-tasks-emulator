package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const restQ = "/v2/projects/p/locations/l/queues"

func doReq(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func mustQueue(t *testing.T, h http.Handler, name string) {
	t.Helper()
	if w := doReq(t, h, "POST", restQ, `{"queue":{"name":"`+name+`"}}`); w.Code != 200 {
		t.Fatalf("create queue: %d %s", w.Code, w.Body)
	}
}

func TestCreateTaskRejectsInvalidScheduleTime(t *testing.T) {
	h, _ := setupTestAPI(t)
	mustQueue(t, h, "q")
	w := doReq(t, h, "POST", restQ+"/q/tasks", `{"task":{"scheduleTime":"tomorrow","httpRequest":{"url":"http://localhost:1/x"}}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body)
	}
}

func TestInvalidTaskDoesNotAutoCreateQueue(t *testing.T) {
	h, _ := setupTestAPI(t)
	w := doReq(t, h, "POST", restQ+"/ghost/tasks", `{"task":{"httpRequest":{"url":"not a url"}}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "GET", restQ+"/ghost", ""); w.Code != http.StatusNotFound {
		t.Fatalf("queue was auto-created by an invalid request (status %d)", w.Code)
	}
}

func TestCreateTaskNameDedupe(t *testing.T) {
	h, _ := setupTestAPI(t)
	body := `{"task":{"name":"projects/p/locations/l/queues/q/tasks/my-task","httpRequest":{"url":"http://localhost:1/x"}}}`
	w := doReq(t, h, "POST", restQ+"/q/tasks", body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"projects/p/locations/l/queues/q/tasks/my-task"`) {
		t.Fatalf("first create: %d %s", w.Code, w.Body)
	}
	w = doReq(t, h, "POST", restQ+"/q/tasks", body)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate: status = %d, want 409: %s", w.Code, w.Body)
	}
	// Short name works too, and a name from another queue is rejected.
	if w := doReq(t, h, "POST", restQ+"/q/tasks", `{"task":{"name":"short","httpRequest":{"url":"http://localhost:1/x"}}}`); w.Code != 200 {
		t.Fatalf("short name: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "POST", restQ+"/q/tasks", `{"task":{"name":"projects/p/locations/l/queues/other/tasks/x","httpRequest":{"url":"http://localhost:1/x"}}}`); w.Code != 400 {
		t.Fatalf("foreign name: %d", w.Code)
	}
}

func TestCreateTaskDispatchDeadlineFormats(t *testing.T) {
	h, _ := setupTestAPI(t)
	cases := map[string]string{`"45s"`: "45s", `60`: "60s", `"20.5s"`: "21s"}
	for in, want := range cases {
		w := doReq(t, h, "POST", restQ+"/q/tasks", `{"task":{"dispatchDeadline":`+in+`,"httpRequest":{"url":"http://localhost:1/x"}}}`)
		if w.Code != 200 {
			t.Fatalf("deadline %s: %d %s", in, w.Code, w.Body)
		}
		var resp struct {
			DispatchDeadline string `json:"dispatchDeadline"`
		}
		json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.DispatchDeadline != want {
			t.Errorf("deadline %s -> %q, want %q", in, resp.DispatchDeadline, want)
		}
	}
	if w := doReq(t, h, "POST", restQ+"/q/tasks", `{"task":{"dispatchDeadline":"soon","httpRequest":{"url":"http://localhost:1/x"}}}`); w.Code != 400 {
		t.Fatalf("bad deadline: %d", w.Code)
	}
}

func TestCreateTaskBodyBase64(t *testing.T) {
	h, _ := setupTestAPI(t)
	payload := `{"hello":"world"}`
	enc := base64.StdEncoding.EncodeToString([]byte(payload))
	w := doReq(t, h, "POST", restQ+"/q/tasks", `{"task":{"httpRequest":{"url":"http://localhost:1/x","body":"`+enc+`"}}}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var resp struct {
		Name        string `json:"name"`
		HTTPRequest struct {
			Body string `json:"body"`
		} `json:"httpRequest"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.HTTPRequest.Body != enc {
		t.Fatalf("body round trip = %q, want %q", resp.HTTPRequest.Body, enc)
	}
	// Not valid base64: must be an error, not silently stored with quotes.
	if w := doReq(t, h, "POST", restQ+"/q/tasks", `{"task":{"httpRequest":{"url":"http://localhost:1/x","body":"not base64!!"}}}`); w.Code != 400 {
		t.Fatalf("invalid base64: status %d", w.Code)
	}
	// A raw JSON object is accepted as-is.
	if w := doReq(t, h, "POST", restQ+"/q/tasks", `{"task":{"httpRequest":{"url":"http://localhost:1/x","body":{"a":1}}}}`); w.Code != 200 {
		t.Fatalf("raw json body: %d %s", w.Code, w.Body)
	}
}

func TestQueueConflictAndValidation(t *testing.T) {
	h, _ := setupTestAPI(t)
	mustQueue(t, h, "dup")
	if w := doReq(t, h, "POST", restQ, `{"queue":{"name":"dup"}}`); w.Code != http.StatusConflict {
		t.Fatalf("duplicate queue: %d", w.Code)
	}
	if w := doReq(t, h, "POST", restQ, `{"queue":{"name":"bad name/<x>"}}`); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid queue name: %d", w.Code)
	}
	if w := doReq(t, h, "DELETE", restQ+"/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("delete missing queue: %d", w.Code)
	}
}

func TestDeleteQueueDeletesTasks(t *testing.T) {
	h, database := setupTestAPI(t)
	mustQueue(t, h, "q")
	doReq(t, h, "POST", restQ+"/q/tasks", `{"task":{"httpRequest":{"url":"http://localhost:1/x"}}}`)
	if w := doReq(t, h, "DELETE", restQ+"/q", ""); w.Code != 200 {
		t.Fatalf("delete: %d", w.Code)
	}
	var n int
	database.Conn().QueryRow("SELECT COUNT(*) FROM tasks").Scan(&n)
	if n != 0 {
		t.Fatalf("%d orphaned tasks", n)
	}
}

func TestPauseResumePurgeREST(t *testing.T) {
	h, _ := setupTestAPI(t)
	mustQueue(t, h, "q")
	doReq(t, h, "POST", restQ+"/q/tasks", `{"task":{"httpRequest":{"url":"http://localhost:1/x"}}}`)

	w := doReq(t, h, "POST", restQ+"/q:pause", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"PAUSED"`) {
		t.Fatalf("pause: %d %s", w.Code, w.Body)
	}
	w = doReq(t, h, "POST", restQ+"/q:resume", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"RUNNING"`) {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "POST", restQ+"/q:purge", ""); w.Code != 200 {
		t.Fatalf("purge: %d %s", w.Code, w.Body)
	}
	var list struct {
		Tasks []any `json:"tasks"`
	}
	json.Unmarshal(doReq(t, h, "GET", restQ+"/q/tasks", "").Body.Bytes(), &list)
	if len(list.Tasks) != 0 {
		t.Fatalf("tasks after purge: %d", len(list.Tasks))
	}
	if w := doReq(t, h, "POST", restQ+"/nope:pause", ""); w.Code != 404 {
		t.Fatalf("pause missing: %d", w.Code)
	}
	if w := doReq(t, h, "POST", restQ+"/q:explode", ""); w.Code != 404 {
		t.Fatalf("unknown verb: %d", w.Code)
	}
}

func TestRunTaskREST(t *testing.T) {
	h, _ := setupTestAPI(t)
	w := doReq(t, h, "POST", restQ+"/q/tasks", `{"task":{"name":"t1","httpRequest":{"url":"http://localhost:1/x"}}}`)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := doReq(t, h, "POST", restQ+"/q/tasks/t1:run", ""); w.Code != 200 {
		t.Fatalf("run: %d %s", w.Code, w.Body)
	}
	if w := doReq(t, h, "POST", restQ+"/q/tasks/t1/run", ""); w.Code != 200 {
		t.Fatalf("run (slash form): %d", w.Code)
	}
	if w := doReq(t, h, "POST", restQ+"/q/tasks/missing:run", ""); w.Code != 404 {
		t.Fatalf("run missing task: %d, want 404", w.Code)
	}
	if w := doReq(t, h, "POST", restQ+"/q/tasks/missing/run", ""); w.Code != 404 {
		t.Fatalf("run missing task (slash form): %d, want 404", w.Code)
	}
}

func TestListTasksPagination(t *testing.T) {
	h, _ := setupTestAPI(t)
	for i := 0; i < 5; i++ {
		doReq(t, h, "POST", restQ+"/q/tasks", `{"task":{"httpRequest":{"url":"http://localhost:1/x"}}}`)
	}
	seen := map[string]bool{}
	token := ""
	for pages := 0; pages < 10; pages++ {
		path := restQ + "/q/tasks?pageSize=2"
		if token != "" {
			path += "&pageToken=" + token
		}
		var resp struct {
			Tasks         []struct{ Name string } `json:"tasks"`
			NextPageToken string                  `json:"nextPageToken"`
		}
		json.Unmarshal(doReq(t, h, "GET", path, "").Body.Bytes(), &resp)
		for _, tk := range resp.Tasks {
			seen[tk.Name] = true
		}
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	if len(seen) != 5 {
		t.Fatalf("paged through %d unique tasks, want 5", len(seen))
	}
}
