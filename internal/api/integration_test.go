package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/worker"
)

func TestCreateTaskAndWorkerDispatch(t *testing.T) {
	tmp, err := os.CreateTemp("", "integration_test_*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	database, err := db.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	cfg, _ := config.Load()

	// Mock HTTP server to receive task
	var received sync.WaitGroup
	received.Add(1)
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			received.Done()
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer mockServer.Close()

	// Create queue and task via API
	router := NewRouter(database, cfg)

	createQueue := httptest.NewRequest(http.MethodPost, "/v2/projects/p/locations/l/queues", bytes.NewReader([]byte(`{"queue":{"name":"q"}}`)))
	createQueue.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, createQueue)
	if w.Code != http.StatusOK {
		t.Fatalf("create queue: %d %s", w.Code, w.Body.String())
	}

	taskBody := `{"task":{"httpRequest":{"url":"` + mockServer.URL + `","httpMethod":"POST"}}}`
	createTask := httptest.NewRequest(http.MethodPost, "/v2/projects/p/locations/l/queues/q/tasks", bytes.NewReader([]byte(taskBody)))
	createTask.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, createTask)
	if w.Code != http.StatusOK {
		t.Fatalf("create task: %d %s", w.Code, w.Body.String())
	}

	var taskResp struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(w.Body).Decode(&taskResp); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	if taskResp.Name == "" {
		t.Fatal("task name empty")
	}

	// Start worker
	wrk := worker.New(database, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go wrk.Run(ctx)

	// Wait for task to be dispatched (max 3 seconds)
	done := make(chan struct{})
	go func() {
		received.Wait()
		close(done)
	}()
	select {
	case <-done:
		// Success
	case <-time.After(3 * time.Second):
		t.Fatal("task was not dispatched within 3 seconds")
	}

	wrk.Stop()
}
