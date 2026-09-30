package grpcserver

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

const tasksParent = "projects/p/locations/l"

func newTasksServer(t *testing.T) *Server {
	t.Helper()
	database, err := db.New(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	cfg, _ := config.Load()
	return New(database, cfg)
}

func httpTask(name string) *cloudtaskspb.Task {
	return &cloudtaskspb.Task{
		Name: name,
		MessageType: &cloudtaskspb.Task_HttpRequest{HttpRequest: &cloudtaskspb.HttpRequest{
			Url: "http://localhost:1/x", Body: []byte("hi"),
		}},
	}
}

func TestGRPCCreateTaskDuplicateIsAlreadyExists(t *testing.T) {
	s := newTasksServer(t)
	ctx := context.Background()
	req := &cloudtaskspb.CreateTaskRequest{Parent: tasksParent + "/queues/q", Task: httpTask(tasksParent + "/queues/q/tasks/dup")}
	if _, err := s.CreateTask(ctx, req); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateTask(ctx, req)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("code = %v, want AlreadyExists (%v)", status.Code(err), err)
	}
}

func TestGRPCCreateQueueDuplicateAndValidation(t *testing.T) {
	s := newTasksServer(t)
	ctx := context.Background()
	req := &cloudtaskspb.CreateQueueRequest{Parent: tasksParent, Queue: &cloudtaskspb.Queue{Name: tasksParent + "/queues/q"}}
	if _, err := s.CreateQueue(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateQueue(ctx, req); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate: %v", err)
	}
	bad := &cloudtaskspb.CreateQueueRequest{Parent: tasksParent, Queue: &cloudtaskspb.Queue{Name: tasksParent + "/queues/bad name"}}
	if _, err := s.CreateQueue(ctx, bad); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid name: %v", err)
	}
	if _, err := s.DeleteQueue(ctx, &cloudtaskspb.DeleteQueueRequest{Name: tasksParent + "/queues/none"}); status.Code(err) != codes.NotFound {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestGRPCDispatchDeadlineHonoured(t *testing.T) {
	s := newTasksServer(t)
	task := httpTask("")
	task.DispatchDeadline = durationpb.New(5 * time.Second)
	got, err := s.CreateTask(context.Background(), &cloudtaskspb.CreateTaskRequest{Parent: tasksParent + "/queues/q", Task: task})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetDispatchDeadline().AsDuration() != 5*time.Second {
		t.Fatalf("deadline = %v, want 5s (was silently replaced by 30s)", got.GetDispatchDeadline().AsDuration())
	}
	task.DispatchDeadline = durationpb.New(-time.Second)
	if _, err := s.CreateTask(context.Background(), &cloudtaskspb.CreateTaskRequest{Parent: tasksParent + "/queues/q", Task: task}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("negative deadline: %v", err)
	}
}

func TestGRPCInvalidTaskDoesNotAutoCreateQueue(t *testing.T) {
	s := newTasksServer(t)
	task := httpTask("")
	task.GetHttpRequest().Url = "ftp://nope"
	if _, err := s.CreateTask(context.Background(), &cloudtaskspb.CreateTaskRequest{Parent: tasksParent + "/queues/ghost", Task: task}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v", err)
	}
	if _, err := s.GetQueue(context.Background(), &cloudtaskspb.GetQueueRequest{Name: tasksParent + "/queues/ghost"}); status.Code(err) != codes.NotFound {
		t.Fatalf("queue auto-created by invalid request: %v", err)
	}
}

func TestGRPCListTasksPagination(t *testing.T) {
	s := newTasksServer(t)
	ctx := context.Background()
	parent := tasksParent + "/queues/q"
	for i := 0; i < 5; i++ {
		if _, err := s.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: parent, Task: httpTask("")}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	token := ""
	for i := 0; i < 10; i++ {
		resp, err := s.ListTasks(ctx, &cloudtaskspb.ListTasksRequest{Parent: parent, PageSize: 2, PageToken: token})
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Tasks) > 2 {
			t.Fatalf("page has %d tasks, page_size 2", len(resp.Tasks))
		}
		for _, tk := range resp.Tasks {
			seen[tk.Name] = true
		}
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d unique tasks, want 5", len(seen))
	}
}

func TestGRPCPauseResumePurgeUpdate(t *testing.T) {
	s := newTasksServer(t)
	ctx := context.Background()
	qname := tasksParent + "/queues/q"
	s.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: qname, Task: httpTask("")})

	q, err := s.PauseQueue(ctx, &cloudtaskspb.PauseQueueRequest{Name: qname})
	if err != nil || q.State != cloudtaskspb.Queue_PAUSED {
		t.Fatalf("pause: %v %v", q, err)
	}
	got, _ := s.GetQueue(ctx, &cloudtaskspb.GetQueueRequest{Name: qname})
	if got.State != cloudtaskspb.Queue_PAUSED {
		t.Fatalf("GetQueue state = %v", got.State)
	}
	q, err = s.ResumeQueue(ctx, &cloudtaskspb.ResumeQueueRequest{Name: qname})
	if err != nil || q.State != cloudtaskspb.Queue_RUNNING {
		t.Fatalf("resume: %v %v", q, err)
	}

	q, err = s.UpdateQueue(ctx, &cloudtaskspb.UpdateQueueRequest{
		Queue:      &cloudtaskspb.Queue{Name: qname, RateLimits: &cloudtaskspb.RateLimits{MaxConcurrentDispatches: 3}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"rate_limits.max_concurrent_dispatches"}},
	})
	if err != nil || q.RateLimits.MaxConcurrentDispatches != 3 {
		t.Fatalf("update: %v %v", q, err)
	}

	if _, err := s.PurgeQueue(ctx, &cloudtaskspb.PurgeQueueRequest{Name: qname}); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListTasks(ctx, &cloudtaskspb.ListTasksRequest{Parent: qname})
	if len(list.Tasks) != 0 {
		t.Fatalf("tasks after purge: %d", len(list.Tasks))
	}
	if _, err := s.PauseQueue(ctx, &cloudtaskspb.PauseQueueRequest{Name: tasksParent + "/queues/none"}); status.Code(err) != codes.NotFound {
		t.Fatalf("pause missing: %v", err)
	}
}

func TestGRPCRunAndDeleteMissingTask(t *testing.T) {
	s := newTasksServer(t)
	name := tasksParent + "/queues/q/tasks/none"
	if _, err := s.RunTask(context.Background(), &cloudtaskspb.RunTaskRequest{Name: name}); status.Code(err) != codes.NotFound {
		t.Fatalf("run missing: %v", err)
	}
	if _, err := s.DeleteTask(context.Background(), &cloudtaskspb.DeleteTaskRequest{Name: name}); status.Code(err) != codes.NotFound {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestGRPCDispatchCountForPendingTask(t *testing.T) {
	s := newTasksServer(t)
	got, err := s.CreateTask(context.Background(), &cloudtaskspb.CreateTaskRequest{Parent: tasksParent + "/queues/q", Task: httpTask("")})
	if err != nil {
		t.Fatal(err)
	}
	if got.DispatchCount != 0 {
		t.Fatalf("dispatch_count of a never-dispatched task = %d, want 0", got.DispatchCount)
	}
}
