package api

import (
	"context"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ListWebhooks returns the configured state and every webhook source.
func (s *Server) ListWebhooks(ctx context.Context, _ *pb.ListWebhooksRequest) (*pb.ListWebhooksResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ListWebhooksResponse, error) {
		list, err := s.client.ListWebhooks(ctx)
		if err != nil {
			return nil, err
		}

		out := &pb.ListWebhooksResponse{Configured: list.Configured}
		for _, w := range list.Sources {
			out.Webhooks = append(out.Webhooks, webhook(w))
		}

		return out, nil
	})
}

// AddWebhook creates a webhook source and returns its secret.
func (s *Server) AddWebhook(ctx context.Context, r *pb.AddWebhookRequest) (*pb.AddWebhookResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.AddWebhookResponse, error) {
		created, err := s.client.AddWebhook(ctx, management.NewWebhook{
			ID:           r.GetId(),
			Forge:        forgeauth.Kind(r.GetForge()),
			Connection:   r.GetConnection(),
			Repositories: r.GetRepositories(),
			Register:     r.GetRegister(),
		})
		if err != nil {
			return nil, err
		}

		return &pb.AddWebhookResponse{Webhook: webhook(created.Source), Secret: created.Secret}, nil
	})
}

// RotateWebhookSecret replaces a managed source's secret.
func (s *Server) RotateWebhookSecret(ctx context.Context, r *pb.RotateWebhookSecretRequest) (*pb.RotateWebhookSecretResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.RotateWebhookSecretResponse, error) {
		rotated, err := s.client.RotateWebhook(ctx, r.GetId())
		if err != nil {
			return nil, err
		}

		return &pb.RotateWebhookSecretResponse{
			Webhook:            webhook(rotated.Source),
			Secret:             rotated.Secret,
			FailedRepositories: rotated.Failed,
		}, nil
	})
}

// RemoveWebhook deletes a managed source and its registered webhooks.
func (s *Server) RemoveWebhook(ctx context.Context, r *pb.RemoveWebhookRequest) (*pb.RemoveWebhookResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.RemoveWebhookResponse, error) {
		failed, err := s.client.RemoveWebhook(ctx, r.GetId())
		if err != nil {
			return nil, err
		}

		return &pb.RemoveWebhookResponse{FailedRepositories: failed}, nil
	})
}

func webhook(w management.WebhookSource) *pb.Webhook {
	out := &pb.Webhook{
		Id:           w.ID,
		Forge:        string(w.Forge),
		Connection:   w.Connection,
		Repositories: w.Repositories,
		Managed:      w.Managed,
		Registered:   w.Registered,
		Url:          w.URL,
	}
	if !w.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(w.CreatedAt)
	}

	return out
}
