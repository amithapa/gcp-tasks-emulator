package grpcserver

import (
	"context"
	"errors"

	"cloud-tasks-emulator/internal/db"
	"cloud-tasks-emulator/internal/scheduler"
	"cloud.google.com/go/scheduler/apiv1/schedulerpb"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SchedulerServer implements google.cloud.scheduler.v1.CloudScheduler.
type SchedulerServer struct {
	schedulerpb.UnimplementedCloudSchedulerServer
	svc *scheduler.Service
}

func NewSchedulerServer(database *db.DB) *SchedulerServer {
	return &SchedulerServer{svc: scheduler.ForDB(database.Conn())}
}

func schedErr(err error) error {
	var ve *scheduler.ValidationError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ve):
		return status.Error(codes.InvalidArgument, ve.Msg)
	case errors.Is(err, scheduler.ErrNotFound):
		return status.Error(codes.NotFound, "job not found")
	case errors.Is(err, scheduler.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, "job already exists")
	}
	return status.Error(codes.Internal, err.Error())
}

func jobIDFromName(name string) (string, error) {
	p, l, j, err := scheduler.ParseName(name)
	if err != nil {
		return "", schedErr(err)
	}
	return scheduler.ResourceName(p, l, j), nil
}

func (s *SchedulerServer) ListJobs(ctx context.Context, req *schedulerpb.ListJobsRequest) (*schedulerpb.ListJobsResponse, error) {
	project, location, err := parseParent(req.GetParent())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	list, err := s.svc.List(project, location)
	if err != nil {
		return nil, schedErr(err)
	}
	resp := &schedulerpb.ListJobsResponse{}
	for _, j := range list {
		resp.Jobs = append(resp.Jobs, jobToProto(j))
	}
	return resp, nil
}

func (s *SchedulerServer) GetJob(ctx context.Context, req *schedulerpb.GetJobRequest) (*schedulerpb.Job, error) {
	id, err := jobIDFromName(req.GetName())
	if err != nil {
		return nil, err
	}
	j, err := s.svc.Get(id)
	if err != nil {
		return nil, schedErr(err)
	}
	return jobToProto(j), nil
}

func (s *SchedulerServer) CreateJob(ctx context.Context, req *schedulerpb.CreateJobRequest) (*schedulerpb.Job, error) {
	project, location, err := parseParent(req.GetParent())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	pj := req.GetJob()
	if pj == nil {
		return nil, status.Error(codes.InvalidArgument, "job is required")
	}
	if pj.GetHttpTarget() == nil {
		return nil, status.Error(codes.InvalidArgument, "only http_target jobs are supported")
	}
	p, l, name, nerr := scheduler.ParseName(pj.GetName())
	if nerr != nil {
		return nil, schedErr(nerr)
	}
	if p != project || l != location {
		return nil, status.Error(codes.InvalidArgument, "job name does not match the parent project/location")
	}
	j := jobFromProto(pj)
	j.Project, j.Location, j.Name = project, location, name
	switch pj.GetState() {
	case schedulerpb.Job_PAUSED:
		j.State = scheduler.StatePaused
	}
	created, err := s.svc.Create(j)
	if err != nil {
		return nil, schedErr(err)
	}
	return jobToProto(created), nil
}

func (s *SchedulerServer) UpdateJob(ctx context.Context, req *schedulerpb.UpdateJobRequest) (*schedulerpb.Job, error) {
	pj := req.GetJob()
	if pj == nil {
		return nil, status.Error(codes.InvalidArgument, "job is required")
	}
	id, err := jobIDFromName(pj.GetName())
	if err != nil {
		return nil, err
	}
	var paths []string
	if m := req.GetUpdateMask(); m != nil {
		paths = m.GetPaths()
	}
	j, err := s.svc.Update(id, jobFromProto(pj), paths)
	if err != nil {
		return nil, schedErr(err)
	}
	return jobToProto(j), nil
}

func (s *SchedulerServer) DeleteJob(ctx context.Context, req *schedulerpb.DeleteJobRequest) (*emptypb.Empty, error) {
	id, err := jobIDFromName(req.GetName())
	if err != nil {
		return nil, err
	}
	if err := s.svc.Delete(id); err != nil {
		return nil, schedErr(err)
	}
	return &emptypb.Empty{}, nil
}

func (s *SchedulerServer) PauseJob(ctx context.Context, req *schedulerpb.PauseJobRequest) (*schedulerpb.Job, error) {
	id, err := jobIDFromName(req.GetName())
	if err != nil {
		return nil, err
	}
	j, err := s.svc.Pause(id)
	if err != nil {
		return nil, schedErr(err)
	}
	return jobToProto(j), nil
}

func (s *SchedulerServer) ResumeJob(ctx context.Context, req *schedulerpb.ResumeJobRequest) (*schedulerpb.Job, error) {
	id, err := jobIDFromName(req.GetName())
	if err != nil {
		return nil, err
	}
	j, err := s.svc.Resume(id)
	if err != nil {
		return nil, schedErr(err)
	}
	return jobToProto(j), nil
}

func (s *SchedulerServer) RunJob(ctx context.Context, req *schedulerpb.RunJobRequest) (*schedulerpb.Job, error) {
	id, err := jobIDFromName(req.GetName())
	if err != nil {
		return nil, err
	}
	j, err := s.svc.Run(id)
	if err != nil {
		return nil, schedErr(err)
	}
	return jobToProto(j), nil
}

func jobFromProto(pj *schedulerpb.Job) *scheduler.Job {
	j := &scheduler.Job{
		Description:     pj.GetDescription(),
		Schedule:        pj.GetSchedule(),
		TimeZone:        pj.GetTimeZone(),
		AttemptDeadline: pj.GetAttemptDeadline().AsDuration(),
	}
	if t := pj.GetHttpTarget(); t != nil {
		j.URL = t.GetUri()
		j.Headers = t.GetHeaders()
		j.Body = t.GetBody()
		if t.GetHttpMethod() != schedulerpb.HttpMethod_HTTP_METHOD_UNSPECIFIED {
			j.HTTPMethod = t.GetHttpMethod().String()
		}
	}
	if rc := pj.GetRetryConfig(); rc != nil {
		j.Retry = scheduler.RetryConfig{
			RetryCount:       int(rc.GetRetryCount()),
			MaxRetryDuration: rc.GetMaxRetryDuration().AsDuration(),
			MinBackoff:       rc.GetMinBackoffDuration().AsDuration(),
			MaxBackoff:       rc.GetMaxBackoffDuration().AsDuration(),
			MaxDoublings:     int(rc.GetMaxDoublings()),
		}
	}
	return j
}

func jobToProto(j *scheduler.Job) *schedulerpb.Job {
	state := schedulerpb.Job_ENABLED
	if j.State == scheduler.StatePaused {
		state = schedulerpb.Job_PAUSED
	}
	pj := &schedulerpb.Job{
		Name:        j.ID,
		Description: j.Description,
		Schedule:    j.Schedule,
		TimeZone:    j.TimeZone,
		State:       state,
		Target: &schedulerpb.Job_HttpTarget{HttpTarget: &schedulerpb.HttpTarget{
			Uri:        j.URL,
			HttpMethod: schedulerpb.HttpMethod(schedulerpb.HttpMethod_value[j.HTTPMethod]),
			Headers:    j.Headers,
			Body:       j.Body,
		}},
		RetryConfig: &schedulerpb.RetryConfig{
			RetryCount:         int32(j.Retry.RetryCount),
			MaxRetryDuration:   durationpb.New(j.Retry.MaxRetryDuration),
			MinBackoffDuration: durationpb.New(j.Retry.MinBackoff),
			MaxBackoffDuration: durationpb.New(j.Retry.MaxBackoff),
			MaxDoublings:       int32(j.Retry.MaxDoublings),
		},
		AttemptDeadline: durationpb.New(j.AttemptDeadline),
		UserUpdateTime:  timestamppb.New(j.UpdatedAt),
	}
	if !j.NextRunAt.IsZero() {
		pj.ScheduleTime = timestamppb.New(j.NextRunAt)
	}
	if !j.LastAttemptAt.IsZero() {
		pj.LastAttemptTime = timestamppb.New(j.LastAttemptAt)
		pj.Status = &rpcstatus.Status{Code: int32(j.LastStatusCode), Message: j.LastError}
	}
	return pj
}
