package queues

import (
	"errors"
	"os"
	"testing"

	"cloud-tasks-emulator/internal/db"
)

func TestQueueRepository(t *testing.T) {
	tmp, err := os.CreateTemp("", "queues_test_*.db")
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

	repo := NewRepository(database.Conn())

	q := &Queue{Project: "p", Location: "l", Name: "test-queue"}
	if err := repo.Create(q); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(q.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.Name != "test-queue" {
		t.Errorf("Get: got %v", got)
	}

	list, err := repo.List("p", "l")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("List: got %d queues", len(list))
	}

	if err := repo.Delete(q.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ = repo.Get(q.ID)
	if got != nil {
		t.Error("Get after delete: expected nil")
	}
}

func newTestRepo(t *testing.T) (*Repository, *db.DB) {
	t.Helper()
	tmp, err := os.CreateTemp("", "queues_test_*.db")
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
	return NewRepository(database.Conn()), database
}

func countTasks(t *testing.T, database *db.DB, queueID string) int {
	t.Helper()
	var n int
	if err := database.Conn().QueryRow("SELECT COUNT(*) FROM tasks WHERE queue_id = ?", queueID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func seedTask(database *db.DB, queueID, id string) {
	database.Conn().Exec(
		`INSERT INTO tasks (id, queue_id, url, schedule_time, next_attempt_at) VALUES (?, ?, 'http://x', '2020-01-01 00:00:00', '2020-01-01 00:00:00')`,
		id, queueID)
}

func TestDuplicateCreateReturnsErrAlreadyExists(t *testing.T) {
	repo, _ := newTestRepo(t)
	if err := repo.Create(&Queue{Project: "p", Location: "l", Name: "q"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(&Queue{Project: "p", Location: "l", Name: "q"}); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("got %v, want ErrAlreadyExists", err)
	}
	if err := repo.Delete("projects/p/locations/l/queues/nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestDeleteQueueDeletesItsTasks(t *testing.T) {
	repo, database := newTestRepo(t)
	q1 := &Queue{Project: "p", Location: "l", Name: "q1"}
	q2 := &Queue{Project: "p", Location: "l", Name: "q2"}
	repo.Create(q1)
	repo.Create(q2)
	seedTask(database, q1.ID, q1.ID+"/tasks/a")
	seedTask(database, q2.ID, q2.ID+"/tasks/b")

	if err := repo.Delete(q1.ID); err != nil {
		t.Fatal(err)
	}
	if n := countTasks(t, database, q1.ID); n != 0 {
		t.Fatalf("orphaned tasks after queue delete: %d", n)
	}
	if n := countTasks(t, database, q2.ID); n != 1 {
		t.Fatalf("other queue's tasks affected: %d", n)
	}
}

func TestPauseResumePurge(t *testing.T) {
	repo, database := newTestRepo(t)
	q := &Queue{Project: "p", Location: "l", Name: "q"}
	repo.Create(q)
	seedTask(database, q.ID, q.ID+"/tasks/a")
	seedTask(database, q.ID, q.ID+"/tasks/b")

	if err := repo.SetState(q.ID, StatePaused); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Get(q.ID); !got.Paused() {
		t.Fatalf("state = %s, want PAUSED", got.State)
	}
	if err := repo.SetState(q.ID, StateRunning); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Get(q.ID); got.Paused() {
		t.Fatal("still paused after resume")
	}

	n, err := repo.Purge(q.ID)
	if err != nil || n != 2 {
		t.Fatalf("Purge = %d, %v", n, err)
	}
	if got, _ := repo.Get(q.ID); got == nil {
		t.Fatal("purge must keep the queue")
	}
	if _, err := repo.Purge("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("purge missing: %v", err)
	}
	if err := repo.SetState("nope", StatePaused); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pause missing: %v", err)
	}
}

func TestUpdateRateLimits(t *testing.T) {
	repo, _ := newTestRepo(t)
	q := &Queue{Project: "p", Location: "l", Name: "q"}
	repo.Create(q)
	if err := repo.UpdateRateLimits(q.ID, RateLimits{MaxConcurrentDispatches: 2}); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Get(q.ID)
	if got.RateLimits.MaxConcurrentDispatches != 2 || got.RateLimits.MaxDispatchesPerSecond != DefaultMaxDispatchesPerSecond {
		t.Fatalf("rate limits = %+v", got.RateLimits)
	}
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"a", "my-queue_1"} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a/b", "has space", "<script>"} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
