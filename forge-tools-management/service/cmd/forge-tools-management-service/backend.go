package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-repositories/library/repositories"
	pb "github.com/r59q/forge-tools/forge-repositories/service/repositoriespb"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

type remoteRepositories struct {
	client   pb.RepositoriesClient
	token    forgeauth.Credentials
	operator forgeauth.Credentials
}

func (s settings) repositoryReader(ctx context.Context, operator forgeauth.Credentials) (management.RepositoryReader, func(), error) {
	if s.repositoriesAddress == "" {
		return nil, func() {}, nil
	}
	var token forgeauth.Credentials = forgeauth.EnvToken(s.repositoriesTokenEnv)
	if s.repositoriesTokenFile != "" {
		token = forgeauth.FileToken(s.repositoriesTokenFile)
	}
	if _, err := secondToken(ctx, "repository service", token, operator); err != nil {
		return nil, nil, err
	}
	transport := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	if s.repositoriesInsecure {
		transport = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(s.repositoriesAddress, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, nil, errors.New("cannot initialize repository connection")
	}
	return &remoteRepositories{client: pb.NewRepositoriesClient(conn), token: token, operator: operator}, func() { _ = conn.Close() }, nil
}

// secondToken rereads a backend credential, including after rotation, and
// checks that the browser's operator token cannot also serve as it. A nil
// operator skips that check.
func secondToken(ctx context.Context, name string, source, operator forgeauth.Credentials) (string, error) {
	token, err := source.Token(ctx)
	if err != nil {
		return "", fmt.Errorf("%s token required", name)
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	if parsed, ok := forgeauth.AuthorizationToken(h, "Bearer"); !ok || parsed != token {
		return "", fmt.Errorf("%s token must be a nonempty bearer token", name)
	}
	if operator == nil {
		return token, nil
	}
	if op, err := operator.Token(ctx); err != nil || op == token {
		return "", fmt.Errorf("operator and %s tokens must be distinct", name)
	}
	return token, nil
}

// List calls only the existing repository listing RPC. Browser metadata is
// never forwarded. The backend controls source selection and forge access.
func (r *remoteRepositories) List(ctx context.Context, _ repositories.ListOptions) ([]repositories.Repository, error) {
	token, err := secondToken(ctx, "repository service", r.token, r.operator)
	if err != nil {
		return nil, err
	}
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
	response, err := r.client.ListRepositories(ctx, &pb.ListRepositoriesRequest{})
	if err != nil {
		return nil, err
	}
	out := make([]repositories.Repository, 0, len(response.GetRepositories()))
	for _, v := range response.GetRepositories() {
		source := v.GetSource()
		out = append(out, repositories.Repository{
			Source: repositories.SourceInfo{ID: source.GetId(), Kind: forgeauth.Kind(source.GetKind()), Host: source.GetHost()},
			Path:   v.GetPath(), Description: v.GetDescription(), Visibility: repositories.Visibility(v.GetVisibility()),
		})
	}
	return out, nil
}
