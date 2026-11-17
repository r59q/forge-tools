package api

import (
	"context"

	"github.com/r59q/forge-tools/forge-agents/library/agent"
	pb "github.com/r59q/forge-tools/forge-agents/service/agentspb"
	"google.golang.org/protobuf/proto"
)

// CreateAgent prepares a workspace and records a new agent.
func (s *Server) CreateAgent(ctx context.Context, req *pb.CreateAgentRequest) (*pb.CreateAgentResponse, error) {
	return call(ctx, s, func(ctx context.Context, m *agent.Manager) (*pb.CreateAgentResponse, error) {
		spec, err := specOf(req)
		if err != nil {
			return nil, err
		}
		a, err := m.Create(ctx, spec)
		if err != nil {
			return nil, err
		}
		return &pb.CreateAgentResponse{Agent: agentPB(a)}, nil
	})
}

// GetAgent reads one agent.
func (s *Server) GetAgent(ctx context.Context, req *pb.GetAgentRequest) (*pb.GetAgentResponse, error) {
	return call(ctx, s, func(ctx context.Context, m *agent.Manager) (*pb.GetAgentResponse, error) {
		a, err := m.Get(ctx, req.GetId())
		if err != nil {
			return nil, err
		}
		return &pb.GetAgentResponse{Agent: agentPB(a)}, nil
	})
}

// ListAgents lists agents oldest first.
func (s *Server) ListAgents(ctx context.Context, req *pb.ListAgentsRequest) (*pb.ListAgentsResponse, error) {
	return call(ctx, s, func(ctx context.Context, m *agent.Manager) (*pb.ListAgentsResponse, error) {
		agents, err := m.List(ctx, agent.Filter{
			State:      agent.State(req.GetState()),
			Parent:     req.GetParent(),
			Repository: repositoryOf(req.GetRepository()),
		})
		if err != nil {
			return nil, err
		}
		response := &pb.ListAgentsResponse{Agents: make([]*pb.Agent, 0, len(agents))}
		for _, a := range agents {
			response.Agents = append(response.Agents, agentPB(a))
		}
		return response, nil
	})
}

// SendMessage starts a turn.
func (s *Server) SendMessage(ctx context.Context, req *pb.SendMessageRequest) (*pb.SendMessageResponse, error) {
	return call(ctx, s, func(ctx context.Context, m *agent.Manager) (*pb.SendMessageResponse, error) {
		a, err := m.Send(ctx, req.GetId(), req.GetText())
		if err != nil {
			return nil, err
		}
		return &pb.SendMessageResponse{Agent: agentPB(a)}, nil
	})
}

// AnswerInput answers a pending input request.
func (s *Server) AnswerInput(ctx context.Context, req *pb.AnswerInputRequest) (*pb.AnswerInputResponse, error) {
	return call(ctx, s, func(ctx context.Context, m *agent.Manager) (*pb.AnswerInputResponse, error) {
		a, err := m.Answer(ctx, req.GetId(), req.GetRequestId(), req.GetText(), req.GetAllow())
		if err != nil {
			return nil, err
		}
		return &pb.AnswerInputResponse{Agent: agentPB(a)}, nil
	})
}

// CancelTurn kills the running client.
func (s *Server) CancelTurn(ctx context.Context, req *pb.CancelTurnRequest) (*pb.CancelTurnResponse, error) {
	return call(ctx, s, func(ctx context.Context, m *agent.Manager) (*pb.CancelTurnResponse, error) {
		a, err := m.Cancel(ctx, req.GetId())
		if err != nil {
			return nil, err
		}
		return &pb.CancelTurnResponse{Agent: agentPB(a)}, nil
	})
}

// StopAgent stops an agent for good.
func (s *Server) StopAgent(ctx context.Context, req *pb.StopAgentRequest) (*pb.StopAgentResponse, error) {
	return call(ctx, s, func(ctx context.Context, m *agent.Manager) (*pb.StopAgentResponse, error) {
		a, err := m.Stop(ctx, req.GetId())
		if err != nil {
			return nil, err
		}
		return &pb.StopAgentResponse{Agent: agentPB(a)}, nil
	})
}

// DeleteAgent removes an agent.
func (s *Server) DeleteAgent(ctx context.Context, req *pb.DeleteAgentRequest) (*pb.DeleteAgentResponse, error) {
	return call(ctx, s, func(ctx context.Context, m *agent.Manager) (*pb.DeleteAgentResponse, error) {
		if err := m.Delete(ctx, req.GetId(), req.GetRemoveCheckout()); err != nil {
			return nil, err
		}
		return &pb.DeleteAgentResponse{}, nil
	})
}

// ListEvents reads a transcript page of at most PageBytes.
func (s *Server) ListEvents(ctx context.Context, req *pb.ListEventsRequest) (*pb.ListEventsResponse, error) {
	return call(ctx, s, func(ctx context.Context, m *agent.Manager) (*pb.ListEventsResponse, error) {
		events, err := m.Transcript(ctx, req.GetId(), req.GetAfter(), int(req.GetLimit()))
		if err != nil {
			return nil, err
		}
		response := &pb.ListEventsResponse{Events: make([]*pb.Event, 0, len(events))}
		size := 0
		for _, e := range events {
			event := eventPB(e)
			size += proto.Size(event)
			if size > PageBytes && len(response.Events) > 0 {
				break
			}
			response.Events = append(response.Events, event)
		}
		return response, nil
	})
}

// GetTurnOutput reads a turn's raw client output.
func (s *Server) GetTurnOutput(ctx context.Context, req *pb.GetTurnOutputRequest) (*pb.GetTurnOutputResponse, error) {
	return call(ctx, s, func(ctx context.Context, m *agent.Manager) (*pb.GetTurnOutputResponse, error) {
		o, err := m.Output(ctx, req.GetId(), int(req.GetTurn()))
		if err != nil {
			return nil, err
		}
		return &pb.GetTurnOutputResponse{Output: &pb.TurnOutput{Stdout: o.Stdout, Stderr: o.Stderr, Truncated: o.Truncated}}, nil
	})
}
