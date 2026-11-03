package main

import (
	"context"
	"errors"
	"math"

	"github.com/r59q/forge-tools/forge-actions/library/actions"
	pb "github.com/r59q/forge-tools/forge-actions/service/actionspb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type remotePipelines struct {
	client          pb.ActionsServiceClient
	token, operator forgeauth.Credentials
}

// pipelineReader connects to forge-actions, or returns nil if unset.
func (s settings) pipelineReader(ctx context.Context, operator forgeauth.Credentials) (*remotePipelines, func(), error) {
	if s.actionsAddress == "" {
		return nil, func() {}, nil
	}

	var token forgeauth.Credentials = forgeauth.EnvToken(s.actionsTokenEnv)
	if s.actionsTokenFile != "" {
		token = forgeauth.FileToken(s.actionsTokenFile)
	}
	if _, err := secondToken(ctx, "actions service", token, operator); err != nil {
		return nil, nil, err
	}

	conn, err := dial(s.actionsAddress, s.actionsInsecure)
	if err != nil {
		return nil, nil, errors.New("cannot initialize actions connection")
	}

	reader := &remotePipelines{client: pb.NewActionsServiceClient(conn), token: token, operator: operator}

	return reader, func() { _ = conn.Close() }, nil
}

// clamp32 converts n to int32, clamping it to the int32 range. Callers bound
// their values, so clamping only keeps out of range values out of range.
func clamp32(n int) int32 {
	return int32(min(max(n, math.MinInt32), math.MaxInt32))
}

func (r *remotePipelines) context(ctx context.Context) (context.Context, error) {
	token, err := secondToken(ctx, "actions service", r.token, r.operator)
	if err != nil {
		return nil, err
	}

	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), nil
}

func pipelineBackendError(err error) error {
	if err == nil {
		return nil
	}

	switch status.Code(err) {
	case codes.InvalidArgument:
		return actions.ErrInvalid
	case codes.NotFound:
		return forgehttp.ErrNotFound
	case codes.Unimplemented:
		return actions.ErrUnsupported
	default:
		return management.ErrUnavailable
	}
}

func (r *remotePipelines) ListPipelines(ctx context.Context, o actions.ListOptions) (actions.PipelinePage, error) {
	ctx, err := r.context(ctx)
	if err != nil {
		return actions.PipelinePage{}, err
	}

	v, err := r.client.ListPipelines(ctx, &pb.ListPipelinesRequest{
		Source:       o.Source,
		Repository:   o.Repository,
		Page:         clamp32(o.Page),
		Commit:       o.Commit,
		Branch:       o.Branch,
		DefinitionId: o.DefinitionID,
		Phase:        o.Phase,
		Result:       o.Result,
	})
	if err != nil {
		return actions.PipelinePage{}, pipelineBackendError(err)
	}

	out := actions.PipelinePage{NextPage: int(v.NextPage)}
	for _, p := range v.Pipelines {
		out.Pipelines = append(out.Pipelines, pb.ToPipeline(p))
	}

	return out, nil
}

func (r *remotePipelines) GetPipeline(ctx context.Context, ref actions.Ref) (actions.Pipeline, error) {
	ctx, err := r.context(ctx)
	if err != nil {
		return actions.Pipeline{}, err
	}

	v, err := r.client.GetPipeline(ctx, &pb.GetPipelineRequest{Source: ref.Source, Repository: ref.Repository, Id: ref.ID})

	return pb.ToPipeline(v.GetPipeline()), pipelineBackendError(err)
}

func (r *remotePipelines) GetJob(ctx context.Context, ref actions.Ref) (actions.Job, error) {
	ctx, err := r.context(ctx)
	if err != nil {
		return actions.Job{}, err
	}

	v, err := r.client.GetJob(ctx, &pb.GetJobRequest{Source: ref.Source, Repository: ref.Repository, Id: ref.ID})

	return pb.ToJob(v.GetJob()), pipelineBackendError(err)
}

func (r *remotePipelines) ReadJobLog(ctx context.Context, ref actions.Ref) (actions.Log, error) {
	ctx, err := r.context(ctx)
	if err != nil {
		return actions.Log{}, err
	}

	v, err := r.client.ReadJobLog(ctx, &pb.ReadJobLogRequest{Source: ref.Source, Repository: ref.Repository, Id: ref.ID})

	return pb.ToLog(v.GetLog()), pipelineBackendError(err)
}

func (r *remotePipelines) ListJobs(ctx context.Context, ref actions.Ref) ([]actions.Job, error) {
	ctx, err := r.context(ctx)
	if err != nil {
		return nil, err
	}

	v, err := r.client.ListJobs(ctx, &pb.ListJobsRequest{Source: ref.Source, Repository: ref.Repository, Id: ref.ID})
	if err != nil {
		return nil, pipelineBackendError(err)
	}

	out := make([]actions.Job, 0, len(v.Jobs))
	for _, j := range v.Jobs {
		out = append(out, pb.ToJob(j))
	}

	return out, nil
}
