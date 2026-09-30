package grpcserver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"cloud-tasks-emulator/internal/config"
	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/queues"
	"cloud-tasks-emulator/internal/tasks"
	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"cloud.google.com/go/scheduler/apiv1/schedulerpb"
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
	schedulerpb.RegisterCloudSchedulerServer(grpcServer, NewSchedulerServer(s.db))
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
	list, listErr := repo.List(project, location)
	if listErr != nil {
		return nil, status.Error(codes.Internal, listErr.Error())
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
	if err := queues.ValidateName(queueName); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
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
		return nil, queueErr(err)
	}
	return queueToProto(q), nil
}

// queueErr maps repository errors to gRPC status errors.
func queueErr(err error) error {
	switch {
	case errors.Is(err, queues.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, queues.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func taskErr(err error) error {
	switch {
	case errors.Is(err, tasks.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, tasks.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func (s *Server) DeleteQueue(ctx context.Context, req *cloudtaskspb.DeleteQueueRequest) (*emptypb.Empty, error) {
	repo := queues.NewRepository(s.db.Conn())
	if err := repo.Delete(req.GetName()); err != nil {
		return nil, queueErr(err)
	}
	return &emptypb.Empty{}, nil
}

const (
	defaultPageSize = 1000
	maxPageSize     = 1000
)

func (s *Server) ListTasks(ctx context.Context, req *cloudtaskspb.ListTasksRequest) (*cloudtaskspb.ListTasksResponse, error) {
	pageSize := defaultPageSize
	if req.GetPageSize() < 0 {
		return nil, status.Error(codes.InvalidArgument, "page_size must not be negative")
	}
	if req.GetPageSize() > 0 {
		pageSize = min(int(req.GetPageSize()), maxPageSize)
	}
	offset := 0
	if tok := req.GetPageToken(); tok != "" {
		n, err := strconv.Atoi(tok)
		if err != nil || n < 0 {
			return nil, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		offset = n
	}

	repo := tasks.NewRepository(s.db.Conn())
	// Fetch one extra row to know whether another page exists.
	list, err := repo.ListPage(req.GetParent(), "", pageSize+1, offset)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	resp := &cloudtaskspb.ListTasksResponse{}
	if len(list) > pageSize {
		list = list[:pageSize]
		resp.NextPageToken = strconv.Itoa(offset + pageSize)
	}
	resp.Tasks = make([]*cloudtaskspb.Task, len(list))
	for i, t := range list {
		resp.Tasks[i] = taskToProto(t)
	}
	return resp, nil
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
	queueID := "projects/" + project + "/locations/" + location + "/queues/" + queueName

	// Validate the request fully before touching the database, so a bad
	// request never auto-creates a queue.
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
	if err := tasks.ValidateURL(httpReq.GetUrl()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
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
	if d := pt.GetDispatchDeadline(); d != nil {
		if d.AsDuration() <= 0 {
			return nil, status.Error(codes.InvalidArgument, "dispatch_deadline must be positive")
		}
		dispatchDeadline = int(math.Ceil(d.AsDuration().Seconds()))
	}

	headers := make(map[string]string)
	for k, v := range httpReq.GetHeaders() {
		headers[k] = v
	}

	taskID := uuid.New().String()
	if name := pt.GetName(); name != "" {
		if strings.Contains(name, "/") {
			prefix := queueID + "/tasks/"
			if !strings.HasPrefix(name, prefix) {
				return nil, status.Error(codes.InvalidArgument, "task.name must belong to queue "+queueID)
			}
			name = strings.TrimPrefix(name, prefix)
		}
		if err := tasks.ValidateID(name); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		taskID = name
	}
	taskName := queueID + "/tasks/" + taskID

	queueRepo := queues.NewRepository(s.db.Conn())
	q, err := queueRepo.Get(queueID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if q == nil {
		if !s.cfg.AutoCreateQueues {
			return nil, status.Error(codes.NotFound, "queue not found")
		}
		if err := queues.ValidateName(queueName); err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		q = &queues.Queue{Project: project, Location: location, Name: queueName}
		if err := queueRepo.Create(q); err != nil && !errors.Is(err, queues.ErrAlreadyExists) {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

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
		return nil, taskErr(err)
	}
	if stored, err := repo.Get(taskName); err == nil && stored != nil {
		t = stored
	}
	return taskToProto(t), nil
}

func (s *Server) DeleteTask(ctx context.Context, req *cloudtaskspb.DeleteTaskRequest) (*emptypb.Empty, error) {
	repo := tasks.NewRepository(s.db.Conn())
	if err := repo.Delete(req.GetName()); err != nil {
		return nil, taskErr(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) RunTask(ctx context.Context, req *cloudtaskspb.RunTaskRequest) (*cloudtaskspb.Task, error) {
	repo := tasks.NewRepository(s.db.Conn())
	if err := repo.SetNextAttemptNow(req.GetName()); err != nil {
		return nil, taskErr(err)
	}
	t, err := repo.Get(req.GetName())
	if err != nil || t == nil {
		return nil, status.Error(codes.NotFound, "task not found")
	}
	return taskToProto(t), nil
}

// UpdateQueue updates a queue's rate limits (the only mutable setting the
// emulator honours).
func (s *Server) UpdateQueue(ctx context.Context, req *cloudtaskspb.UpdateQueueRequest) (*cloudtaskspb.Queue, error) {
	pq := req.GetQueue()
	if pq == nil || pq.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "queue.name is required")
	}
	repo := queues.NewRepository(s.db.Conn())
	if pq.GetRateLimits() != nil {
		apply := len(req.GetUpdateMask().GetPaths()) == 0
		for _, p := range req.GetUpdateMask().GetPaths() {
			if strings.HasPrefix(p, "rate_limits") {
				apply = true
			}
		}
		if apply {
			rl := queues.RateLimits{
				MaxDispatchesPerSecond:  int(pq.GetRateLimits().GetMaxDispatchesPerSecond()),
				MaxConcurrentDispatches: int(pq.GetRateLimits().GetMaxConcurrentDispatches()),
			}
			if err := repo.UpdateRateLimits(pq.GetName(), rl); err != nil {
				return nil, queueErr(err)
			}
		}
	}
	return s.getQueueProto(repo, pq.GetName())
}

func (s *Server) getQueueProto(repo *queues.Repository, name string) (*cloudtaskspb.Queue, error) {
	q, err := repo.Get(name)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if q == nil {
		return nil, status.Error(codes.NotFound, "queue not found")
	}
	return queueToProto(q), nil
}

// PurgeQueue deletes all tasks in the queue.
func (s *Server) PurgeQueue(ctx context.Context, req *cloudtaskspb.PurgeQueueRequest) (*cloudtaskspb.Queue, error) {
	repo := queues.NewRepository(s.db.Conn())
	if _, err := repo.Purge(req.GetName()); err != nil {
		return nil, queueErr(err)
	}
	return s.getQueueProto(repo, req.GetName())
}

// PauseQueue stops dispatching the queue's tasks until it is resumed.
func (s *Server) PauseQueue(ctx context.Context, req *cloudtaskspb.PauseQueueRequest) (*cloudtaskspb.Queue, error) {
	repo := queues.NewRepository(s.db.Conn())
	if err := repo.SetState(req.GetName(), queues.StatePaused); err != nil {
		return nil, queueErr(err)
	}
	return s.getQueueProto(repo, req.GetName())
}

func (s *Server) ResumeQueue(ctx context.Context, req *cloudtaskspb.ResumeQueueRequest) (*cloudtaskspb.Queue, error) {
	repo := queues.NewRepository(s.db.Conn())
	if err := repo.SetState(req.GetName(), queues.StateRunning); err != nil {
		return nil, queueErr(err)
	}
	return s.getQueueProto(repo, req.GetName())
}

func queueToProto(q *queues.Queue) *cloudtaskspb.Queue {
	state := cloudtaskspb.Queue_RUNNING
	if q.Paused() {
		state = cloudtaskspb.Queue_PAUSED
	}
	pq := &cloudtaskspb.Queue{
		Name:  q.ResourceName(q.Project, q.Location),
		State: state,
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
	responses := t.Attempts()
	if t.Status == tasks.StatusRunning {
		responses--
	}
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
		DispatchCount:    int32(t.Attempts()),
		ResponseCount:    int32(responses),
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
