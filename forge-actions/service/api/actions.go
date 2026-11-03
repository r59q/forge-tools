package api

import (
	"context"
	"math"

	"github.com/r59q/forge-tools/forge-actions/library/actions"
	pb "github.com/r59q/forge-tools/forge-actions/service/actionspb"
)

// ListSources returns source identities and adapter capabilities.
func (s *Server) ListSources(ctx context.Context, _ *pb.ListSourcesRequest) (*pb.ListSourcesResponse, error) {
	return call(s, ctx, func(_ context.Context, c *actions.Client) (*pb.ListSourcesResponse, error) {
		out := &pb.ListSourcesResponse{}
		for _, v := range c.Sources() {
			a := v.Capabilities
			out.Sources = append(out.Sources, &pb.Source{
				Id:   v.ID,
				Kind: string(v.Kind),
				Capabilities: &pb.Capabilities{
					Definitions: a.Definitions,
					Steps:       a.Steps,
					Stages:      a.Stages,
					JobLogs:     a.JobLogs,
					Artifacts:   a.Artifacts,
				},
			})
		}

		return out, nil
	})
}

// ListPipelines returns one filtered page of executions.
func (s *Server) ListPipelines(ctx context.Context, r *pb.ListPipelinesRequest) (*pb.ListPipelinesResponse, error) {
	return call(s, ctx, func(ctx context.Context, c *actions.Client) (*pb.ListPipelinesResponse, error) {
		v, err := c.ListPipelines(ctx, actions.ListOptions{
			RepositoryRef: actions.RepositoryRef{Source: r.GetSource(), Repository: r.GetRepository()},
			Page:          int(r.GetPage()),
			Commit:        r.GetCommit(),
			Branch:        r.GetBranch(),
			DefinitionID:  r.GetDefinitionId(),
			Phase:         r.GetPhase(),
			Result:        r.GetResult(),
		})
		out := &pb.ListPipelinesResponse{NextPage: clamp32(v.NextPage)}
		for _, p := range v.Pipelines {
			out.Pipelines = append(out.Pipelines, pb.FromPipeline(p))
		}

		return out, err
	})
}

// GetPipeline reads the corresponding library resource.
func (s *Server) GetPipeline(ctx context.Context, r *pb.GetPipelineRequest) (*pb.GetPipelineResponse, error) {
	return call(s, ctx, func(ctx context.Context, c *actions.Client) (*pb.GetPipelineResponse, error) {
		v, err := c.GetPipeline(ctx, ref(r))
		return &pb.GetPipelineResponse{Pipeline: pb.FromPipeline(v)}, err
	})
}

// GetJob reads the corresponding library resource.
func (s *Server) GetJob(ctx context.Context, r *pb.GetJobRequest) (*pb.GetJobResponse, error) {
	return call(s, ctx, func(ctx context.Context, c *actions.Client) (*pb.GetJobResponse, error) {
		v, err := c.GetJob(ctx, ref(r))
		return &pb.GetJobResponse{Job: pb.FromJob(v)}, err
	})
}

// ReadJobLog reads the corresponding library resource.
func (s *Server) ReadJobLog(ctx context.Context, r *pb.ReadJobLogRequest) (*pb.ReadJobLogResponse, error) {
	return call(s, ctx, func(ctx context.Context, c *actions.Client) (*pb.ReadJobLogResponse, error) {
		v, err := c.ReadJobLog(ctx, ref(r))
		return &pb.ReadJobLogResponse{Log: pb.FromLog(v)}, err
	})
}

// ListJobs reads the bounded library listing.
func (s *Server) ListJobs(ctx context.Context, r *pb.ListJobsRequest) (*pb.ListJobsResponse, error) {
	return call(s, ctx, func(ctx context.Context, c *actions.Client) (*pb.ListJobsResponse, error) {
		values, err := c.ListJobs(ctx, ref(r))
		out := &pb.ListJobsResponse{}
		for _, v := range values {
			out.Jobs = append(out.Jobs, pb.FromJob(v))
		}

		return out, err
	})
}

// ListArtifacts reads the bounded library listing.
func (s *Server) ListArtifacts(ctx context.Context, r *pb.ListArtifactsRequest) (*pb.ListArtifactsResponse, error) {
	return call(s, ctx, func(ctx context.Context, c *actions.Client) (*pb.ListArtifactsResponse, error) {
		values, err := c.ListArtifacts(ctx, ref(r))
		out := &pb.ListArtifactsResponse{}
		for _, v := range values {
			out.Artifacts = append(out.Artifacts, pb.FromArtifact(v))
		}

		return out, err
	})
}

// ListDefinitions reads the bounded library listing.
func (s *Server) ListDefinitions(ctx context.Context, r *pb.ListDefinitionsRequest) (*pb.ListDefinitionsResponse, error) {
	return call(s, ctx, func(ctx context.Context, c *actions.Client) (*pb.ListDefinitionsResponse, error) {
		values, err := c.ListDefinitions(ctx, actions.RepositoryRef{Source: r.GetSource(), Repository: r.GetRepository()})
		out := &pb.ListDefinitionsResponse{}
		for _, v := range values {
			out.Definitions = append(out.Definitions, pb.FromDefinition(v))
		}

		return out, err
	})
}

// DownloadArtifact returns bounded archive bytes without extracting them.
func (s *Server) DownloadArtifact(ctx context.Context, r *pb.DownloadArtifactRequest) (*pb.DownloadArtifactResponse, error) {
	return call(s, ctx, func(ctx context.Context, c *actions.Client) (*pb.DownloadArtifactResponse, error) {
		v, err := c.DownloadArtifact(ctx, ref(r))
		return &pb.DownloadArtifactResponse{Data: v}, err
	})
}

// clamp32 converts n to int32, clamping it to the int32 range. The library
// bounds the values it converts, so clamping never changes them.
func clamp32(n int) int32 {
	return int32(min(max(n, math.MinInt32), math.MaxInt32))
}

// refRequest is a request that identifies a resource by its native ID.
type refRequest interface {
	GetSource() string
	GetRepository() string
	GetId() string
}

// ref converts a request to the library reference it names.
func ref(r refRequest) actions.Ref {
	return actions.Ref{
		RepositoryRef: actions.RepositoryRef{Source: r.GetSource(), Repository: r.GetRepository()},
		ID:            r.GetId(),
	}
}
