package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
)

func setupTestAPI(t *testing.T) (http.Handler, *db.DB) {
	tmp, err := os.CreateTemp("", "api_test_*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := tmp.Name()
	tmp.Close()
	t.Cleanup(func() { os.Remove(path) })

	database, err := db.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	cfg, _ := config.Load()
	return NewRouter(database, cfg), database
}

func TestHealth(t *testing.T) {
	router, _ := setupTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("health: got status %d", w.Code)
	}
}

func TestCreateAndListQueue(t *testing.T) {
	router, _ := setupTestAPI(t)

	body := `{"queue":{"name":"my-queue"}}`
	req := httptest.NewRequest(http.MethodPost, "/v2/projects/p/locations/l/queues", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("create queue: got status %d, body %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v2/projects/p/locations/l/queues", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("list queues: got status %d", w.Code)
	}
	var resp struct {
		Queues []struct {
			Name string `json:"name"`
		} `json:"queues"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Queues) != 1 || resp.Queues[0].Name != "projects/p/locations/l/queues/my-queue" {
		t.Errorf("list queues: got %v", resp.Queues)
	}
}
