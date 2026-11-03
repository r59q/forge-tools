package api

import (
	"context"
	"math"

	"github.com/r59q/forge-tools/forge-actions/library/actions"
	apb "github.com/r59q/forge-tools/forge-actions/service/actionspb"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
)

// ListPipelines returns one page and the configured state.
func (s *Server) ListPipelines(ctx context.Context, r *pb.ListPipelinesRequest) (*pb.ListPipelinesResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ListPipelinesResponse, error) {
		v, err := s.client.ListPipelines(ctx, actions.ListOptions{
			RepositoryRef: actions.RepositoryRef{Source: r.GetSource(), Repository: r.GetRepository()},
			Page:          int(r.GetPage()),
			Commit:        r.GetCommit(),
			Branch:        r.GetBranch(),
			DefinitionID:  r.GetDefinitionId(),
			Phase:         r.GetPhase(),
			Result:        r.GetResult(),
		})
		out := &pb.ListPipelinesResponse{Configured: v.Configured, NextPage: clamp32(v.Page.NextPage)}
		for _, p := range v.Page.Pipelines {
			out.Pipelines = append(out.Pipelines, apb.FromPipeline(p))
		}

		return out, err
	})
}

// GetPipeline reads the corresponding pipeline resource.
func (s *Server) GetPipeline(ctx context.Context, r *pb.GetPipelineRequest) (*pb.GetPipelineResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.GetPipelineResponse, error) {
		v, err := s.client.GetPipeline(ctx, toRef(r))
		return &pb.GetPipelineResponse{Pipeline: apb.FromPipeline(v)}, err
	})
}

// GetJob reads the corresponding pipeline resource.
func (s *Server) GetJob(ctx context.Context, r *pb.GetJobRequest) (*pb.GetJobResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.GetJobResponse, error) {
		v, err := s.client.GetJob(ctx, toRef(r))
		return &pb.GetJobResponse{Job: apb.FromJob(v)}, err
	})
}

// ReadJobLog reads the corresponding pipeline resource.
func (s *Server) ReadJobLog(ctx context.Context, r *pb.ReadJobLogRequest) (*pb.ReadJobLogResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ReadJobLogResponse, error) {
		v, err := s.client.ReadJobLog(ctx, toRef(r))
		return &pb.ReadJobLogResponse{Log: apb.FromLog(v)}, err
	})
}

// ListJobs returns latest jobs for a pipeline.
func (s *Server) ListJobs(ctx context.Context, r *pb.ListJobsRequest) (*pb.ListJobsResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ListJobsResponse, error) {
		values, err := s.client.ListJobs(ctx, toRef(r))
		out := &pb.ListJobsResponse{}
		for _, v := range values {
			out.Jobs = append(out.Jobs, apb.FromJob(v))
		}

		return out, err
	})
}

// refRequest is a request that identifies a pipeline or job.
type refRequest interface {
	GetSource() string
	GetRepository() string
	GetId() string
}

// clamp32 converts n to int32, clamping it to the int32 range. The library
// bounds the values it converts, so clamping never changes them.
func clamp32(n int) int32 {
	return int32(min(max(n, math.MinInt32), math.MaxInt32))
}

func toRef(r refRequest) actions.Ref {
	return actions.Ref{
		RepositoryRef: actions.RepositoryRef{Source: r.GetSource(), Repository: r.GetRepository()},
		ID:            r.GetId(),
	}
}
