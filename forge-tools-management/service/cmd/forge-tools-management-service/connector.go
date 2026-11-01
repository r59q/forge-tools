package main

import (
	"context"
	"crypto/tls"
	"errors"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	brokerpb "github.com/r59q/forge-tools/forge-connector/library/brokerpb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// remoteConnector calls the forge-connector broker admin API. It implements
// management.ConnectorAdmin.
type remoteConnector struct {
	client   brokerpb.BrokerClient
	token    forgeauth.Credentials
	operator forgeauth.Credentials
}

func (s settings) connector(ctx context.Context, operator forgeauth.Credentials) (*remoteConnector, func(), error) {
	if s.connectorAddress == "" {
		return nil, func() {}, nil
	}
	var token forgeauth.Credentials = forgeauth.EnvToken(s.connectorTokenEnv)
	if s.connectorTokenFile != "" {
		token = forgeauth.FileToken(s.connectorTokenFile)
	}
	if _, err := secondToken(ctx, "connector admin", token, operator); err != nil {
		return nil, nil, err
	}
	transport := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	if s.connectorInsecure {
		transport = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(s.connectorAddress, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, nil, errors.New("cannot initialize connector connection")
	}
	return &remoteConnector{client: brokerpb.NewBrokerClient(conn), token: token, operator: operator}, func() { _ = conn.Close() }, nil
}

// invoke sends the admin token, rereading it for rotation, and maps status
// codes back to broker errors. Responses are never logged.
func invoke[T any](ctx context.Context, c *remoteConnector, fn func(context.Context, brokerpb.BrokerClient) (T, error)) (T, error) {
	var zero T
	token, err := secondToken(ctx, "connector admin", c.token, c.operator)
	if err != nil {
		return zero, err
	}
	return send(ctx, c, token, fn)
}

// send calls the broker with token as bearer credential.
func send[T any](ctx context.Context, c *remoteConnector, token string, fn func(context.Context, brokerpb.BrokerClient) (T, error)) (T, error) {
	var zero T
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
	result, err := fn(ctx, c.client)
	switch status.Code(err) {
	case codes.OK:
		return result, nil
	case codes.Unauthenticated:
		return zero, broker.ErrUnauthorized
	case codes.Canceled:
		return zero, context.Canceled
	case codes.DeadlineExceeded:
		return zero, context.DeadlineExceeded
	case codes.InvalidArgument:
		return zero, broker.ErrInvalid
	case codes.NotFound:
		return zero, broker.ErrNotFound
	case codes.FailedPrecondition:
		return zero, broker.ErrConflict
	}
	return zero, errors.New("connector unavailable")
}

func forge(f *brokerpb.Forge) broker.Forge {
	return broker.Forge{ID: broker.ID(f.GetId()), Kind: forgeauth.Kind(f.GetKind()), BaseURL: f.GetBaseUrl(), Name: f.GetName()}
}

func connection(c *brokerpb.Connection) broker.Connection {
	i := c.GetIdentity()
	out := broker.Connection{
		ID: broker.ID(c.GetId()), ForgeID: broker.ID(c.GetForgeId()), Name: c.GetName(), Status: broker.Status(c.GetStatus()),
		Identity: forgeauth.Identity{ID: i.GetId(), Login: i.GetLogin(), Name: i.GetName(), Email: i.GetEmail()},
	}
	if c.GetCheckedAt() != nil {
		out.CheckedAt = c.GetCheckedAt().AsTime()
	}
	if c.GetCreatedAt() != nil {
		out.CreatedAt = c.GetCreatedAt().AsTime()
	}
	return out
}

func client(c *brokerpb.Client) broker.Client {
	out := broker.Client{ID: broker.ID(c.GetId()), Name: c.GetName(), CanIssueTokens: c.GetCanIssueTokens(), AllConnections: c.GetAllConnections()}
	for _, id := range c.GetConnectionIds() {
		out.Connections = append(out.Connections, broker.ID(id))
	}
	return out
}

func (c *remoteConnector) ListForges(ctx context.Context) ([]broker.Forge, error) {
	r, err := invoke(ctx, c, func(ctx context.Context, b brokerpb.BrokerClient) (*brokerpb.ListForgesResponse, error) {
		return b.ListForges(ctx, &emptypb.Empty{})
	})
	var out []broker.Forge
	for _, f := range r.GetForges() {
		out = append(out, forge(f))
	}
	return out, err
}

func (c *remoteConnector) CreateForge(ctx context.Context, kind forgeauth.Kind, baseURL, name string) (broker.Forge, error) {
	r, err := invoke(ctx, c, func(ctx context.Context, b brokerpb.BrokerClient) (*brokerpb.Forge, error) {
		return b.CreateForge(ctx, &brokerpb.CreateForgeRequest{Kind: string(kind), BaseUrl: baseURL, Name: name})
	})
	return forge(r), err
}

func (c *remoteConnector) DeleteForge(ctx context.Context, id broker.ID) error {
	_, err := invoke(ctx, c, func(ctx context.Context, b brokerpb.BrokerClient) (*emptypb.Empty, error) {
		return b.DeleteForge(ctx, &brokerpb.ForgeRequest{Id: string(id)})
	})
	return err
}

func (c *remoteConnector) ListConnections(ctx context.Context) ([]broker.Connection, error) {
	r, err := invoke(ctx, c, func(ctx context.Context, b brokerpb.BrokerClient) (*brokerpb.ListConnectionsResponse, error) {
		return b.ListConnections(ctx, &emptypb.Empty{})
	})
	var out []broker.Connection
	for _, conn := range r.GetConnections() {
		out = append(out, connection(conn))
	}
	return out, err
}

func (c *remoteConnector) CreateConnection(ctx context.Context, forgeID broker.ID, name string, credential forgeauth.StaticToken) (broker.Connection, error) {
	r, err := invoke(ctx, c, func(ctx context.Context, b brokerpb.BrokerClient) (*brokerpb.Connection, error) {
		return b.CreateConnection(ctx, &brokerpb.CreateConnectionRequest{ForgeId: string(forgeID), Name: name, Credential: string(credential)})
	})
	return connection(r), err
}

func (c *remoteConnector) TestConnection(ctx context.Context, id broker.ID) (broker.Connection, error) {
	r, err := invoke(ctx, c, func(ctx context.Context, b brokerpb.BrokerClient) (*brokerpb.Connection, error) {
		return b.TestConnection(ctx, &brokerpb.ConnectionRequest{Id: string(id)})
	})
	return connection(r), err
}

func (c *remoteConnector) RotateCredential(ctx context.Context, id broker.ID, credential forgeauth.StaticToken) (broker.Connection, error) {
	r, err := invoke(ctx, c, func(ctx context.Context, b brokerpb.BrokerClient) (*brokerpb.Connection, error) {
		return b.RotateCredential(ctx, &brokerpb.RotateCredentialRequest{Id: string(id), Credential: string(credential)})
	})
	return connection(r), err
}

func (c *remoteConnector) DeleteConnection(ctx context.Context, id broker.ID) error {
	_, err := invoke(ctx, c, func(ctx context.Context, b brokerpb.BrokerClient) (*emptypb.Empty, error) {
		return b.DeleteConnection(ctx, &brokerpb.ConnectionRequest{Id: string(id)})
	})
	return err
}

func (c *remoteConnector) ListClients(ctx context.Context) ([]broker.Client, error) {
	r, err := invoke(ctx, c, func(ctx context.Context, b brokerpb.BrokerClient) (*brokerpb.ListClientsResponse, error) {
		return b.ListClients(ctx, &emptypb.Empty{})
	})
	var out []broker.Client
	for _, cl := range r.GetClients() {
		out = append(out, client(cl))
	}
	return out, err
}

func (c *remoteConnector) RegisterClient(ctx context.Context, cl broker.Client, token forgeauth.StaticToken) (broker.Client, error) {
	ids := make([]string, len(cl.Connections))
	for i, id := range cl.Connections {
		ids[i] = string(id)
	}
	r, err := invoke(ctx, c, func(ctx context.Context, b brokerpb.BrokerClient) (*brokerpb.CreateClientResponse, error) {
		return b.CreateClient(ctx, &brokerpb.CreateClientRequest{
			Name: cl.Name, ConnectionIds: ids, AllConnections: cl.AllConnections, CanIssueTokens: cl.CanIssueTokens, Token: string(token),
		})
	})
	return client(r.GetClient()), err
}

// ClientConnections authenticates with token itself, not the admin token.
func (c *remoteConnector) ClientConnections(ctx context.Context, token forgeauth.StaticToken) ([]broker.ClientConnection, error) {
	r, err := send(ctx, c, string(token), func(ctx context.Context, b brokerpb.BrokerClient) (*brokerpb.ListClientConnectionsResponse, error) {
		return b.ListClientConnections(ctx, &emptypb.Empty{})
	})
	var out []broker.ClientConnection
	for _, conn := range r.GetConnections() {
		out = append(out, broker.ClientConnection{ID: broker.ID(conn.GetId()), Name: conn.GetName(), Kind: forgeauth.Kind(conn.GetKind()), Status: broker.Status(conn.GetStatus())})
	}
	return out, err
}

func (c *remoteConnector) DeleteClient(ctx context.Context, id broker.ID) error {
	_, err := invoke(ctx, c, func(ctx context.Context, b brokerpb.BrokerClient) (*emptypb.Empty, error) {
		return b.DeleteClient(ctx, &brokerpb.ClientRequest{Id: string(id)})
	})
	return err
}
