package api

import (
	"context"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	"github.com/r59q/forge-tools/forge-reactor/library/sources"
	pb "github.com/r59q/forge-tools/forge-reactor/service/reactorpb"
)

// CreateWebhookSource adds a managed source and returns its secret.
func (s *Server) CreateWebhookSource(ctx context.Context, req *pb.CreateWebhookSourceRequest) (*pb.CreateWebhookSourceResponse, error) {
	return call(s, ctx, func(ctx context.Context, _ *reactor.Reactor) (*pb.CreateWebhookSourceResponse, error) {
		src, err := s.sources.managed.Create(ctx, sources.Request{
			ID:           req.GetId(),
			Forge:        forgeauth.Kind(req.GetForge()),
			Connection:   req.GetConnection(),
			Repositories: req.GetRepositories(),
			Register:     req.GetRegister(),
		})
		if err != nil {
			return nil, err
		}

		return &pb.CreateWebhookSourceResponse{Source: s.sources.toManaged(src), Secret: src.Secret}, nil
	})
}

// ListWebhookSources lists every source without secrets.
func (s *Server) ListWebhookSources(ctx context.Context, _ *pb.ListWebhookSourcesRequest) (*pb.ListWebhookSourcesResponse, error) {
	return call(s, ctx, func(ctx context.Context, _ *reactor.Reactor) (*pb.ListWebhookSourcesResponse, error) {
		list, err := s.sources.list(ctx)
		if err != nil {
			return nil, err
		}

		return &pb.ListWebhookSourcesResponse{Sources: list}, nil
	})
}

// RotateWebhookSecret replaces a managed source's secret.
func (s *Server) RotateWebhookSecret(ctx context.Context, req *pb.RotateWebhookSecretRequest) (*pb.RotateWebhookSecretResponse, error) {
	return call(s, ctx, func(ctx context.Context, _ *reactor.Reactor) (*pb.RotateWebhookSecretResponse, error) {
		src, failed, err := s.sources.managed.Rotate(ctx, req.GetId())
		if err != nil {
			return nil, err
		}

		return &pb.RotateWebhookSecretResponse{
			Source:             s.sources.toManaged(src),
			Secret:             src.Secret,
			FailedRepositories: failed,
		}, nil
	})
}

// DeleteWebhookSource removes a managed source and its registered hooks.
func (s *Server) DeleteWebhookSource(ctx context.Context, req *pb.DeleteWebhookSourceRequest) (*pb.DeleteWebhookSourceResponse, error) {
	return call(s, ctx, func(ctx context.Context, _ *reactor.Reactor) (*pb.DeleteWebhookSourceResponse, error) {
		failed, err := s.sources.managed.Delete(ctx, req.GetId())
		if err != nil {
			return nil, err
		}

		return &pb.DeleteWebhookSourceResponse{FailedRepositories: failed}, nil
	})
}
