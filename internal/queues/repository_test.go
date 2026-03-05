package queues

import (
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
