package tasks

import (
	"os"
	"testing"
	"time"

	"cloud-tasks-emulator/internal/db"
)

func TestTaskRepository(t *testing.T) {
	tmp, err := os.CreateTemp("", "tasks_test_*.db")
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

	// Create queue first
	database.Conn().Exec(
		"INSERT INTO queues (id, project, location, name) VALUES (?, ?, ?, ?)",
		"projects/p/locations/l/queues/q", "p", "l", "q",
	)

	repo := NewRepository(database.Conn())

	now := time.Now()
	task := &Task{
		Name:             "projects/p/locations/l/queues/q/tasks/t1",
		QueueID:          "projects/p/locations/l/queues/q",
		HTTPMethod:       "POST",
		URL:              "http://localhost:8080/webhook",
		Body:             []byte(`{"key":"value"}`),
		ScheduleTime:     now,
		NextAttemptAt:    now,
		Status:           StatusPending,
		MaxRetries:       5,
		DispatchDeadline: 30,
	}
	if err := repo.Create(task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(task.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.URL != "http://localhost:8080/webhook" {
		t.Errorf("Get: got %v", got)
	}

	list, err := repo.List("projects/p/locations/l/queues/q", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("List: got %d tasks", len(list))
	}

	// ListPending should return the task (next_attempt_at <= now)
	pending, err := repo.ListPending(10)
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("ListPending: got %d tasks, want 1", len(pending))
	}

	claimed, err := repo.Claim(task.Name)
	if err != nil || !claimed {
		t.Fatalf("Claim: %v", err)
	}

	// Second claim should fail (already running)
	claimed2, _ := repo.Claim(task.Name)
	if claimed2 {
		t.Error("Claim: expected false for already claimed task")
	}

	if err := repo.Delete(task.Name); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ = repo.Get(task.Name)
	if got != nil {
		t.Error("Get after delete: expected nil")
	}
}
