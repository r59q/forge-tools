package api

import (
	"fmt"
	"math"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/agent"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
	"github.com/r59q/forge-tools/forge-agents/library/workspace"
	pb "github.com/r59q/forge-tools/forge-agents/service/agentspb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// specOf returns the agent a request describes.
func specOf(req *pb.CreateAgentRequest) (agent.Spec, error) {
	spec := agent.Spec{
		Client: req.GetClient(),
		Model:  req.GetModel(),
		Effort: req.GetEffort(),
		Workspace: workspace.Spec{
			Kind:       workspace.Kind(req.GetWorkspace().GetKind()),
			Path:       req.GetWorkspace().GetPath(),
			Repository: repositoryOf(req.GetWorkspace().GetRepository()),
			Reference:  req.GetWorkspace().GetReference(),
		},
		Policy: adapter.Policy{Mode: req.GetPolicy().GetMode(), Ask: req.GetPolicy().GetAsk()},
		Limits: agent.Limits{MaxEvents: req.GetLimits().GetMaxEvents()},
		Parent: req.GetParent(),
	}
	if timeout := req.GetLimits().GetTurnTimeout(); timeout != nil {
		if timeout.CheckValid() != nil || timeout.AsDuration() <= 0 {
			return agent.Spec{}, fmt.Errorf("%w: turn timeout", errBadRequest)
		}
		spec.Limits.TurnTimeout = timeout.AsDuration()
	}
	return spec, nil
}

func repositoryOf(r *pb.Repository) workspace.Repository {
	return workspace.Repository{LocalPath: r.GetLocalPath(), Source: r.GetSource(), Path: r.GetPath()}
}

func repositoryPB(r workspace.Repository) *pb.Repository {
	if r.IsZero() {
		return nil
	}
	return &pb.Repository{LocalPath: r.LocalPath, Source: r.Source, Path: r.Path}
}

func agentPB(a agent.Agent) *pb.Agent {
	return &pb.Agent{
		Id:     a.ID,
		Client: a.Client,
		Model:  a.Model,
		Effort: a.Effort,
		Workspace: &pb.Workspace{
			Kind:       string(a.Workspace.Kind),
			Path:       a.Workspace.Path,
			Checkout:   a.Workspace.Checkout,
			Repository: repositoryPB(a.Workspace.Repository),
			Reference:  a.Workspace.Reference,
		},
		Policy:    &pb.Policy{Mode: a.Policy.Mode, Ask: a.Policy.Ask},
		Limits:    &pb.Limits{TurnTimeout: durationpb.New(a.Limits.TurnTimeout), MaxEvents: a.Limits.MaxEvents},
		Parent:    a.Parent,
		State:     string(a.State),
		Session:   a.Session,
		Turns:     int32Of(a.Turns),
		Error:     a.Error,
		CreatedAt: timestamppb.New(a.CreatedAt),
		UpdatedAt: timestamppb.New(a.UpdatedAt),
	}
}

func eventPB(e transcript.Event) *pb.Event {
	event := &pb.Event{
		Seq:         e.Seq,
		Time:        timestamppb.New(e.Time),
		Turn:        int32Of(e.Turn),
		Kind:        string(e.Kind),
		Role:        e.Role,
		Text:        e.Text,
		ToolId:      e.ToolID,
		ToolName:    e.ToolName,
		Input:       e.Input,
		IsError:     e.IsError,
		RequestId:   e.RequestID,
		RequestKind: e.RequestKind,
		Allow:       e.Allow,
		Status:      e.Status,
		Truncated:   e.Truncated,
	}
	if u := e.Usage; u != nil {
		event.Usage = &pb.Usage{
			InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CachedTokens: u.CachedTokens, CostUsd: u.CostUSD,
		}
	}
	return event
}

// int32Of returns n, capped to the int32 range. Turn counts never come
// near it.
func int32Of(n int) int32 {
	return int32(max(min(n, math.MaxInt32), math.MinInt32)) //nolint:gosec // clamped to the int32 range above.
}
