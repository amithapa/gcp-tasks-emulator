package grpcserver

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/queues"
	"cloud-tasks-emulator/internal/tasks"
	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Server struct {
	cloudtaskspb.UnimplementedCloudTasksServer
	db  *db.DB
	cfg *config.Config
}

func New(database *db.DB, cfg *config.Config) *Server {
	return &Server{db: database, cfg: cfg}
}

func (s *Server) Run(ctx context.Context, addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	grpcServer := grpc.NewServer()
	cloudtaskspb.RegisterCloudTasksServer(grpcServer, s)
	go func() {
		<-ctx.Done()
		grpcServer.GracefulStop()
	}()
	return grpcServer.Serve(lis)
}

func parseParent(parent string) (project, location string, err error) {
	parts := strings.Split(parent, "/")
	if len(parts) < 4 || parts[0] != "projects" || parts[2] != "locations" {
		return "", "", fmt.Errorf("invalid parent: %s", parent)
	}
	return parts[1], parts[3], nil
}

func parseTaskParent(parent string) (project, location, queue string, err error) {
	parts := strings.Split(parent, "/")
	if len(parts) < 6 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "queues" {
		return "", "", "", fmt.Errorf("invalid parent: %s", parent)
	}
	return parts[1], parts[3], parts[5], nil
}

func (s *Server) ListQueues(ctx context.Context, req *cloudtaskspb.ListQueuesRequest) (*cloudtaskspb.ListQueuesResponse, error) {
	project, location, err := parseParent(req.GetParent())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if project == "" {
		project = s.cfg.DefaultProject
	}
	if location == "" {
		location = s.cfg.DefaultLocation
	}

	repo := queues.NewRepository(s.db.Conn())
	list, err := repo.List(project, location)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	pbQueues := make([]*cloudtaskspb.Queue, len(list))
	for i, q := range list {
		pbQueues[i] = queueToProto(q)
	}
	return &cloudtaskspb.ListQueuesResponse{Queues: pbQueues}, nil
}

func (s *Server) GetQueue(ctx context.Context, req *cloudtaskspb.GetQueueRequest) (*cloudtaskspb.Queue, error) {
	repo := queues.NewRepository(s.db.Conn())
	q, err := repo.Get(req.GetName())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if q == nil {
		return nil, status.Error(codes.NotFound, "queue not found")
	}
	return queueToProto(q), nil
}

func (s *Server) CreateQueue(ctx context.Context, req *cloudtaskspb.CreateQueueRequest) (*cloudtaskspb.Queue, error) {
	project, location, err := parseParent(req.GetParent())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if project == "" {
		project = s.cfg.DefaultProject
	}
	if location == "" {
		location = s.cfg.DefaultLocation
	}
	if req.GetQueue() == nil {
		return nil, status.Error(codes.InvalidArgument, "queue is required")
	}

	pq := req.GetQueue()
	name := strings.TrimPrefix(pq.GetName(), "projects/")
	parts := strings.Split(name, "/")
	queueName := parts[len(parts)-1]
	if queueName == "" {
		return nil, status.Error(codes.InvalidArgument, "queue name is required")
	}

	q := &queues.Queue{Project: project, Location: location, Name: queueName}
	if pq.GetRateLimits() != nil {
		q.RateLimits = &queues.RateLimits{
			MaxDispatchesPerSecond:  int(pq.GetRateLimits().GetMaxDispatchesPerSecond()),
			MaxConcurrentDispatches: int(pq.GetRateLimits().GetMaxConcurrentDispatches()),
		}
	}

	repo := queues.NewRepository(s.db.Conn())
	if err := repo.Create(q); err != nil {
		return nil, status.Error(codes.AlreadyExists, err.Error())
	}
	return queueToProto(q), nil
}

func (s *Server) DeleteQueue(ctx context.Context, req *cloudtaskspb.DeleteQueueRequest) (*emptypb.Empty, error) {
	repo := queues.NewRepository(s.db.Conn())
	if err := repo.Delete(req.GetName()); err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) ListTasks(ctx context.Context, req *cloudtaskspb.ListTasksRequest) (*cloudtaskspb.ListTasksResponse, error) {
	repo := tasks.NewRepository(s.db.Conn())
	list, err := repo.List(req.GetParent(), "")
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	pbTasks := make([]*cloudtaskspb.Task, len(list))
	for i, t := range list {
		pbTasks[i] = taskToProto(t)
	}
	return &cloudtaskspb.ListTasksResponse{Tasks: pbTasks}, nil
}

func (s *Server) GetTask(ctx context.Context, req *cloudtaskspb.GetTaskRequest) (*cloudtaskspb.Task, error) {
	repo := tasks.NewRepository(s.db.Conn())
	t, err := repo.Get(req.GetName())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if t == nil {
		return nil, status.Error(codes.NotFound, "task not found")
	}
	return taskToProto(t), nil
}

func (s *Server) CreateTask(ctx context.Context, req *cloudtaskspb.CreateTaskRequest) (*cloudtaskspb.Task, error) {
	parent := req.GetParent()
	if parent == "" {
		return nil, status.Error(codes.InvalidArgument, "parent is required")
	}
	project, location, queueName, err := parseTaskParent(parent)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if project == "" {
		project = s.cfg.DefaultProject
	}
	if location == "" {
		location = s.cfg.DefaultLocation
	}

	queueID := "projects/" + project + "/locations/" + location + "/queues/" + queueName
	queueRepo := queues.NewRepository(s.db.Conn())
	q, err := queueRepo.Get(queueID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if q == nil {
		if s.cfg.AutoCreateQueues {
			q = &queues.Queue{Project: project, Location: location, Name: queueName}
			if err := queueRepo.Create(q); err != nil {
				return nil, status.Error(codes.Internal, err.Error())
			}
		} else {
			return nil, status.Error(codes.NotFound, "queue not found")
		}
	}

	pt := req.GetTask()
	if pt == nil {
		return nil, status.Error(codes.InvalidArgument, "task is required")
	}

	httpReq := pt.GetHttpRequest()
	if httpReq == nil {
		return nil, status.Error(codes.InvalidArgument, "task.http_request is required")
	}
	if httpReq.GetUrl() == "" {
		return nil, status.Error(codes.InvalidArgument, "task.http_request.url is required")
	}

	method := "POST"
	switch httpReq.GetHttpMethod() {
	case cloudtaskspb.HttpMethod_GET:
		method = "GET"
	case cloudtaskspb.HttpMethod_PUT:
		method = "PUT"
	case cloudtaskspb.HttpMethod_DELETE:
		method = "DELETE"
	case cloudtaskspb.HttpMethod_PATCH:
		method = "PATCH"
	case cloudtaskspb.HttpMethod_HEAD:
		method = "HEAD"
	case cloudtaskspb.HttpMethod_OPTIONS:
		method = "OPTIONS"
	}

	scheduleTime := time.Now()
	if pt.GetScheduleTime() != nil {
		scheduleTime = pt.GetScheduleTime().AsTime()
	}

	dispatchDeadline := 30
	if pt.GetDispatchDeadline() != nil {
		dispatchDeadline = int(pt.GetDispatchDeadline().Seconds)
	}
	if dispatchDeadline < 15 {
		dispatchDeadline = 30
	}

	headers := make(map[string]string)
	for k, v := range httpReq.GetHeaders() {
		headers[k] = v
	}

	taskID := uuid.New().String()
	if pt.GetName() != "" {
		parts := strings.Split(pt.GetName(), "/")
		if len(parts) >= 2 {
			taskID = parts[len(parts)-1]
		}
	}
	taskName := queueID + "/tasks/" + taskID

	t := &tasks.Task{
		ID:               taskName,
		Name:             taskName,
		QueueID:          queueID,
		HTTPMethod:       method,
		URL:              httpReq.GetUrl(),
		Headers:          headers,
		Body:             httpReq.GetBody(),
		ScheduleTime:     scheduleTime,
		DispatchDeadline: dispatchDeadline,
		Status:           tasks.StatusPending,
		RetryCount:       0,
		MaxRetries:       s.cfg.DefaultMaxRetries,
		NextAttemptAt:    scheduleTime,
	}

	repo := tasks.NewRepository(s.db.Conn())
	if err := repo.Create(t); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return taskToProto(t), nil
}

func (s *Server) DeleteTask(ctx context.Context, req *cloudtaskspb.DeleteTaskRequest) (*emptypb.Empty, error) {
	repo := tasks.NewRepository(s.db.Conn())
	if err := repo.Delete(req.GetName()); err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) RunTask(ctx context.Context, req *cloudtaskspb.RunTaskRequest) (*cloudtaskspb.Task, error) {
	repo := tasks.NewRepository(s.db.Conn())
	if err := repo.SetNextAttemptNow(req.GetName()); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	t, err := repo.Get(req.GetName())
	if err != nil || t == nil {
		return nil, status.Error(codes.NotFound, "task not found")
	}
	return taskToProto(t), nil
}

func (s *Server) UpdateQueue(context.Context, *cloudtaskspb.UpdateQueueRequest) (*cloudtaskspb.Queue, error) {
	return nil, status.Error(codes.Unimplemented, "UpdateQueue not supported")
}

func (s *Server) PurgeQueue(context.Context, *cloudtaskspb.PurgeQueueRequest) (*cloudtaskspb.Queue, error) {
	return nil, status.Error(codes.Unimplemented, "PurgeQueue not supported")
}

func (s *Server) PauseQueue(context.Context, *cloudtaskspb.PauseQueueRequest) (*cloudtaskspb.Queue, error) {
	return nil, status.Error(codes.Unimplemented, "PauseQueue not supported")
}

func (s *Server) ResumeQueue(context.Context, *cloudtaskspb.ResumeQueueRequest) (*cloudtaskspb.Queue, error) {
	return nil, status.Error(codes.Unimplemented, "ResumeQueue not supported")
}

func queueToProto(q *queues.Queue) *cloudtaskspb.Queue {
	pq := &cloudtaskspb.Queue{
		Name:  q.ResourceName(q.Project, q.Location),
		State: cloudtaskspb.Queue_RUNNING,
	}
	if q.RateLimits != nil {
		pq.RateLimits = &cloudtaskspb.RateLimits{
			MaxDispatchesPerSecond:  float64(q.RateLimits.MaxDispatchesPerSecond),
			MaxConcurrentDispatches: int32(q.RateLimits.MaxConcurrentDispatches),
		}
	}
	return pq
}

func taskToProto(t *tasks.Task) *cloudtaskspb.Task {
	pt := &cloudtaskspb.Task{
		Name: t.Name,
		MessageType: &cloudtaskspb.Task_HttpRequest{
			HttpRequest: &cloudtaskspb.HttpRequest{
				Url:        t.URL,
				Headers:    t.Headers,
				Body:       t.Body,
				HttpMethod: httpMethodToProto(t.HTTPMethod),
			},
		},
		ScheduleTime:     timestamppb.New(t.ScheduleTime),
		CreateTime:       timestamppb.New(t.CreatedAt),
		DispatchDeadline: durationpb.New(time.Duration(t.DispatchDeadline) * time.Second),
		DispatchCount:    int32(t.RetryCount + 1),
		ResponseCount:    int32(t.RetryCount),
	}
	return pt
}

func httpMethodToProto(m string) cloudtaskspb.HttpMethod {
	switch m {
	case "GET":
		return cloudtaskspb.HttpMethod_GET
	case "PUT":
		return cloudtaskspb.HttpMethod_PUT
	case "DELETE":
		return cloudtaskspb.HttpMethod_DELETE
	case "PATCH":
		return cloudtaskspb.HttpMethod_PATCH
	case "HEAD":
		return cloudtaskspb.HttpMethod_HEAD
	case "OPTIONS":
		return cloudtaskspb.HttpMethod_OPTIONS
	default:
		return cloudtaskspb.HttpMethod_POST
	}
}
