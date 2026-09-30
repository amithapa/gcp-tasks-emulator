package tasks

import (
	"errors"
	"fmt"
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

func newTestRepo(t *testing.T) (*Repository, *db.DB) {
	t.Helper()
	tmp, err := os.CreateTemp("", "tasks_test_*.db")
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
	database.Conn().Exec("INSERT INTO queues (id, project, location, name) VALUES ('q1', 'p', 'l', 'q1')")
	return NewRepository(database.Conn()), database
}

func mkTask(name string, at time.Time) *Task {
	return &Task{
		Name: name, QueueID: "q1", HTTPMethod: "POST", URL: "http://localhost/x",
		ScheduleTime: at, NextAttemptAt: at, Status: StatusPending, MaxRetries: 3, DispatchDeadline: 30,
	}
}

func TestCreateDuplicateReturnsAlreadyExists(t *testing.T) {
	repo, _ := newTestRepo(t)
	if err := repo.Create(mkTask("q1/tasks/a", time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(mkTask("q1/tasks/a", time.Now())); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate create: got %v, want ErrAlreadyExists", err)
	}
	if err := repo.Delete("q1/tasks/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing: got %v, want ErrNotFound", err)
	}
}

func TestRecoverRunning(t *testing.T) {
	repo, _ := newTestRepo(t)
	repo.Create(mkTask("q1/tasks/a", time.Now()))
	if ok, _ := repo.Claim("q1/tasks/a"); !ok {
		t.Fatal("claim failed")
	}
	n, err := repo.RecoverRunning()
	if err != nil || n != 1 {
		t.Fatalf("RecoverRunning = %d, %v", n, err)
	}
	got, _ := repo.Get("q1/tasks/a")
	if got.Status != StatusPending {
		t.Fatalf("status = %s, want PENDING", got.Status)
	}
}

func TestSetNextAttemptNowRequeuesFailed(t *testing.T) {
	repo, _ := newTestRepo(t)
	repo.Create(mkTask("q1/tasks/a", time.Now().Add(time.Hour)))
	repo.UpdateStatus("q1/tasks/a", StatusFailed, "boom")

	if err := repo.SetNextAttemptNow("q1/tasks/a"); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Get("q1/tasks/a")
	if got.Status != StatusPending || got.LastError != "" || got.RetryCount != 0 {
		t.Fatalf("after run: %+v", got)
	}
	pending, _ := repo.ListPending(10)
	if len(pending) != 1 {
		t.Fatalf("ListPending = %d, want 1 (run-now task must be due)", len(pending))
	}
	if err := repo.SetNextAttemptNow("q1/tasks/none"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("run missing: got %v", err)
	}
}

func TestListPendingHonoursScheduleAndPausedQueue(t *testing.T) {
	repo, database := newTestRepo(t)
	repo.Create(mkTask("q1/tasks/due", time.Now().Add(-time.Second)))
	repo.Create(mkTask("q1/tasks/future", time.Now().Add(1500*time.Millisecond)))

	pending, _ := repo.ListPending(10)
	if len(pending) != 1 || pending[0].Name != "q1/tasks/due" {
		t.Fatalf("pending = %v", pending)
	}

	database.Conn().Exec("UPDATE queues SET state = 'PAUSED' WHERE id = 'q1'")
	pending, _ = repo.ListPending(10)
	if len(pending) != 0 {
		t.Fatalf("paused queue tasks listed as pending: %d", len(pending))
	}
}

func TestSubSecondScheduleIsRespected(t *testing.T) {
	repo, _ := newTestRepo(t)
	// Scheduled 800ms ahead: with whole-second storage this was truncated
	// and became due immediately.
	at := time.Now().Add(800 * time.Millisecond)
	if at.Nanosecond()/1e6 < 100 {
		at = at.Add(200 * time.Millisecond)
	}
	repo.Create(mkTask("q1/tasks/soon", at))
	if pending, _ := repo.ListPending(10); len(pending) != 0 {
		t.Fatalf("task due %v early", time.Until(at))
	}
	time.Sleep(time.Until(at) + 20*time.Millisecond)
	if pending, _ := repo.ListPending(10); len(pending) != 1 {
		t.Fatal("task not due after schedule time")
	}
}

func TestListPageAndCounts(t *testing.T) {
	repo, _ := newTestRepo(t)
	for i := 0; i < 5; i++ {
		repo.Create(mkTask(fmt.Sprintf("q1/tasks/t%d", i), time.Now()))
	}
	repo.UpdateStatus("q1/tasks/t0", StatusFailed, "x")

	page1, _ := repo.ListPage("q1", "", 2, 0)
	page3, _ := repo.ListPage("q1", "", 2, 4)
	if len(page1) != 2 || len(page3) != 1 {
		t.Fatalf("pages: %d, %d", len(page1), len(page3))
	}
	// Newest first, deterministic even within the same second.
	if page1[0].Name != "q1/tasks/t4" || page3[0].Name != "q1/tasks/t0" {
		t.Fatalf("order: %s ... %s", page1[0].Name, page3[0].Name)
	}
	if n, _ := repo.Count("q1", StatusFailed); n != 1 {
		t.Fatalf("failed count = %d", n)
	}
	counts, _ := repo.CountsByQueue()
	if counts["q1"][StatusPending] != 4 || counts["q1"][StatusFailed] != 1 {
		t.Fatalf("counts = %v", counts)
	}
}
