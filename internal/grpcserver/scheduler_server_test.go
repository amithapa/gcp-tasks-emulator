package grpcserver

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"cloud-tasks-emulator/internal/db"
	scheduler "cloud.google.com/go/scheduler/apiv1"
	"cloud.google.com/go/scheduler/apiv1/schedulerpb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func TestSchedulerGRPCClient(t *testing.T) {
	database, err := db.New(filepath.Join(t.TempDir(), "g.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	schedulerpb.RegisterCloudSchedulerServer(srv, NewSchedulerServer(database))
	go srv.Serve(lis)
	defer srv.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := scheduler.NewCloudSchedulerClient(ctx,
		option.WithEndpoint(lis.Addr().String()),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	hit := make(chan struct{}, 4)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit <- struct{}{} }))
	defer target.Close()

	parent := "projects/p/locations/l"
	name := parent + "/jobs/j1"
	job, err := client.CreateJob(ctx, &schedulerpb.CreateJobRequest{Parent: parent, Job: &schedulerpb.Job{
		Name: name, Schedule: "*/5 * * * *", TimeZone: "Europe/Paris",
		Target: &schedulerpb.Job_HttpTarget{HttpTarget: &schedulerpb.HttpTarget{Uri: target.URL, HttpMethod: schedulerpb.HttpMethod_POST}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if job.Name != name || job.State != schedulerpb.Job_ENABLED || job.ScheduleTime == nil || job.GetHttpTarget().GetUri() != target.URL {
		t.Errorf("created: %v", job)
	}

	_, err = client.CreateJob(ctx, &schedulerpb.CreateJobRequest{Parent: parent, Job: &schedulerpb.Job{
		Name: parent + "/jobs/bad", Schedule: "garbage",
		Target: &schedulerpb.Job_HttpTarget{HttpTarget: &schedulerpb.HttpTarget{Uri: target.URL}},
	}})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("bad schedule: %v", err)
	}
	if _, err := client.CreateJob(ctx, &schedulerpb.CreateJobRequest{Parent: parent, Job: job}); status.Code(err) != codes.AlreadyExists {
		t.Errorf("dup: %v", err)
	}
	if _, err := client.GetJob(ctx, &schedulerpb.GetJobRequest{Name: parent + "/jobs/none"}); status.Code(err) != codes.NotFound {
		t.Errorf("get missing: %v", err)
	}

	it := client.ListJobs(ctx, &schedulerpb.ListJobsRequest{Parent: parent})
	if j, err := it.Next(); err != nil || j.Name != name {
		t.Errorf("list: %v %v", j, err)
	}

	upd, err := client.UpdateJob(ctx, &schedulerpb.UpdateJobRequest{
		Job:        &schedulerpb.Job{Name: name, Description: "hello", Schedule: "@hourly"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"description"}},
	})
	if err != nil || upd.Description != "hello" || upd.Schedule != "*/5 * * * *" {
		t.Errorf("update: %v %v", upd, err)
	}

	p, err := client.PauseJob(ctx, &schedulerpb.PauseJobRequest{Name: name})
	if err != nil || p.State != schedulerpb.Job_PAUSED {
		t.Errorf("pause: %v %v", p, err)
	}
	r, err := client.ResumeJob(ctx, &schedulerpb.ResumeJobRequest{Name: name})
	if err != nil || r.State != schedulerpb.Job_ENABLED {
		t.Errorf("resume: %v %v", r, err)
	}
	if _, err := client.RunJob(ctx, &schedulerpb.RunJobRequest{Name: name}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hit:
	case <-time.After(3 * time.Second):
		t.Fatal("RunJob did not hit target")
	}
	if err := client.DeleteJob(ctx, &schedulerpb.DeleteJobRequest{Name: name}); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteJob(ctx, &schedulerpb.DeleteJobRequest{Name: name}); status.Code(err) != codes.NotFound {
		t.Errorf("delete again: %v", err)
	}
}
