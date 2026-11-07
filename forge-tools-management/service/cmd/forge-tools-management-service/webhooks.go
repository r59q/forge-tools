package main

import (
	"context"
	"errors"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	pb "github.com/r59q/forge-tools/forge-reactor/service/reactorpb"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// remoteReactor manages webhook sources and reads events through the
// forge-reactor API.
type remoteReactor struct {
	client          pb.ReactorServiceClient
	token, operator forgeauth.Credentials
}

// reactorClient connects to forge-reactor, or returns nil if unset.
func (s settings) reactorClient(ctx context.Context, operator forgeauth.Credentials) (*remoteReactor, func(), error) {
	if s.reactorAddress == "" {
		return nil, func() {}, nil
	}

	var token forgeauth.Credentials = forgeauth.EnvToken(s.reactorTokenEnv)
	if s.reactorTokenFile != "" {
		token = forgeauth.FileToken(s.reactorTokenFile)
	}
	if _, err := secondToken(ctx, "reactor service", token, operator); err != nil {
		return nil, nil, err
	}

	conn, err := dial(s.reactorAddress, s.reactorInsecure)
	if err != nil {
		return nil, nil, errors.New("cannot initialize reactor connection")
	}

	manager := &remoteReactor{client: pb.NewReactorServiceClient(conn), token: token, operator: operator}

	return manager, func() { _ = conn.Close() }, nil
}

// context adds the reactor service token, reread for rotation.
func (r *remoteReactor) context(ctx context.Context) (context.Context, error) {
	token, err := secondToken(ctx, "reactor service", r.token, r.operator)
	if err != nil {
		return nil, err
	}

	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), nil
}

// webhookBackendError maps reactor status codes to management errors.
func webhookBackendError(err error) error {
	switch status.Code(err) {
	case codes.InvalidArgument:
		return management.ErrInvalid
	case codes.NotFound:
		return management.ErrNotFound
	case codes.AlreadyExists:
		return management.ErrExists
	case codes.FailedPrecondition:
		return management.ErrRegistration
	default:
		return management.ErrUnavailable
	}
}

// ListWebhooks lists every source.
func (r *remoteReactor) ListWebhooks(ctx context.Context) ([]management.WebhookSource, error) {
	ctx, err := r.context(ctx)
	if err != nil {
		return nil, err
	}

	v, err := r.client.ListWebhookSources(ctx, &pb.ListWebhookSourcesRequest{})
	if err != nil {
		return nil, webhookBackendError(err)
	}

	out := make([]management.WebhookSource, 0, len(v.GetSources()))
	for _, src := range v.GetSources() {
		out = append(out, toWebhook(src))
	}

	return out, nil
}

// CreateWebhook creates a managed source.
func (r *remoteReactor) CreateWebhook(ctx context.Context, w management.NewWebhook) (management.WebhookSecret, error) {
	ctx, err := r.context(ctx)
	if err != nil {
		return management.WebhookSecret{}, err
	}

	v, err := r.client.CreateWebhookSource(ctx, &pb.CreateWebhookSourceRequest{
		Id:           w.ID,
		Forge:        string(w.Forge),
		Connection:   w.Connection,
		Repositories: w.Repositories,
		Register:     w.Register,
	})
	if err != nil {
		return management.WebhookSecret{}, webhookBackendError(err)
	}

	return management.WebhookSecret{Source: toWebhook(v.GetSource()), Secret: v.GetSecret()}, nil
}

// RotateWebhook gives a managed source a new secret.
func (r *remoteReactor) RotateWebhook(ctx context.Context, id string) (management.WebhookSecret, error) {
	ctx, err := r.context(ctx)
	if err != nil {
		return management.WebhookSecret{}, err
	}

	v, err := r.client.RotateWebhookSecret(ctx, &pb.RotateWebhookSecretRequest{Id: id})
	if err != nil {
		return management.WebhookSecret{}, webhookBackendError(err)
	}

	return management.WebhookSecret{
		Source: toWebhook(v.GetSource()),
		Secret: v.GetSecret(),
		Failed: v.GetFailedRepositories(),
	}, nil
}

// DeleteWebhook removes a managed source.
func (r *remoteReactor) DeleteWebhook(ctx context.Context, id string) ([]string, error) {
	ctx, err := r.context(ctx)
	if err != nil {
		return nil, err
	}

	v, err := r.client.DeleteWebhookSource(ctx, &pb.DeleteWebhookSourceRequest{Id: id})
	if err != nil {
		return nil, webhookBackendError(err)
	}

	return v.GetFailedRepositories(), nil
}

// toWebhook converts a reactor source.
func toWebhook(src *pb.WebhookSource) management.WebhookSource {
	out := management.WebhookSource{
		ID:           src.GetId(),
		Forge:        forgeauth.Kind(src.GetForge()),
		Connection:   src.GetConnection(),
		Repositories: src.GetRepositories(),
		Managed:      src.GetManaged(),
		Registered:   src.GetRegistered(),
		URL:          src.GetUrl(),
	}
	if src.GetCreatedAt() != nil {
		out.CreatedAt = src.GetCreatedAt().AsTime()
	}

	return out
}
