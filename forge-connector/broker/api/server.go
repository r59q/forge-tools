// Package api exposes the broker Manager over gRPC and generated REST routes.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/r59q/forge-tools/forge-connector/library/broker"
	pb "github.com/r59q/forge-tools/forge-connector/library/brokerpb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server authenticates every RPC, including direct grpc-gateway dispatch.
// It never logs requests, responses, credentials or upstream errors.
type Server struct {
	pb.UnimplementedBrokerServer
	manager *broker.Manager
	admin   forgeauth.Credentials
	timeout time.Duration
}

// New constructs a service with a required admin source and request deadline.
// Runtime configuration should use forgeauth.FileToken or forgeauth.EnvToken.
func New(manager *broker.Manager, admin forgeauth.Credentials, timeout time.Duration) (*Server, error) {
	if manager == nil || admin == nil || timeout <= 0 {
		return nil, broker.ErrInvalid
	}
	return &Server{manager: manager, admin: admin, timeout: timeout}, nil
}

// REST registers generated routes. Authorization is the sole forwarded header;
// Grpc-Metadata-* headers cannot inject credentials. Errors never echo input.
// Responses disable caching, including the two explicit token-returning RPCs.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	mux := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: false}}),
		runtime.WithIncomingHeaderMatcher(func(string) (string, bool) { return "", false }),
		runtime.WithErrorHandler(func(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
			code := status.Code(err)
			if code == codes.Unknown {
				code = codes.Internal
			}
			runtime.DefaultHTTPErrorHandler(ctx, mux, marshaler, w, r, status.Error(code, code.String()))
		}),
	)
	if err := pb.RegisterBrokerHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		mux.ServeHTTP(w, r)
	}), nil
}

func (s *Server) authorize(ctx context.Context, admin bool) (forgeauth.StaticToken, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	h := http.Header{}
	for _, value := range md.Get("authorization") {
		h.Add("Authorization", value)
	}
	token, ok := forgeauth.AuthorizationToken(h, "Bearer")
	if !ok {
		return "", status.Error(codes.Unauthenticated, "authentication required")
	}
	if admin {
		expected, err := s.admin.Token(ctx)
		if err != nil || expected == "" {
			return "", status.Error(codes.Unauthenticated, "authentication required")
		}
		a, b := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(expected))
		if subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
			return "", status.Error(codes.Unauthenticated, "authentication required")
		}
	}
	return forgeauth.StaticToken(token), nil
}
func call[T any](s *Server, ctx context.Context, admin bool, fn func(context.Context, forgeauth.StaticToken) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	token, err := s.authorize(ctx, admin)
	if err != nil {
		return zero, err
	}
	result, err := fn(ctx, token)
	if err != nil {
		return zero, rpcError(err)
	}
	return result, nil
}
func rpcError(err error) error {
	code := codes.Internal
	switch {
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		code = codes.DeadlineExceeded
	case errors.Is(err, broker.ErrUnauthorized):
		code = codes.Unauthenticated
	case errors.Is(err, broker.ErrForbidden):
		code = codes.PermissionDenied
	case errors.Is(err, broker.ErrInvalid):
		code = codes.InvalidArgument
	case errors.Is(err, broker.ErrNotFound):
		code = codes.NotFound
	case errors.Is(err, broker.ErrConflict):
		code = codes.FailedPrecondition
	case errors.Is(err, broker.ErrInvalidCredential):
		code = codes.InvalidArgument
	case errors.Is(err, broker.ErrUnavailable):
		code = codes.FailedPrecondition
	}
	return status.Error(code, code.String())
}
func forge(f broker.Forge) *pb.Forge {
	return &pb.Forge{Id: string(f.ID), Kind: string(f.Kind), BaseUrl: f.BaseURL, Name: f.Name}
}
func connection(c broker.Connection) *pb.Connection {
	return &pb.Connection{Id: string(c.ID), ForgeId: string(c.ForgeID), Name: c.Name, Identity: &pb.Identity{Id: c.Identity.ID, Login: c.Identity.Login, Name: c.Identity.Name, Email: c.Identity.Email}, Status: string(c.Status), CheckedAt: timestamppb.New(c.CheckedAt), CreatedAt: timestamppb.New(c.CreatedAt)}
}
func client(c broker.Client) *pb.Client {
	ids := make([]string, len(c.Connections))
	for i, id := range c.Connections {
		ids[i] = string(id)
	}
	return &pb.Client{Id: string(c.ID), Name: c.Name, ConnectionIds: ids, CanIssueTokens: c.CanIssueTokens}
}

// CreateForge registers a validated endpoint for the owner.
func (s *Server) CreateForge(ctx context.Context, r *pb.CreateForgeRequest) (*pb.Forge, error) {
	return call(s, ctx, true, func(ctx context.Context, _ forgeauth.StaticToken) (*pb.Forge, error) {
		f, err := s.manager.CreateForge(ctx, forgeauth.Kind(r.GetKind()), r.GetBaseUrl(), r.GetName())
		return forge(f), err
	})
}

// ListForges lists owner endpoint metadata.
func (s *Server) ListForges(ctx context.Context, _ *emptypb.Empty) (*pb.ListForgesResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, _ forgeauth.StaticToken) (*pb.ListForgesResponse, error) {
		values, err := s.manager.ListForges(ctx)
		result := &pb.ListForgesResponse{}
		for _, f := range values {
			result.Forges = append(result.Forges, forge(f))
		}
		return result, err
	})
}

// DeleteForge removes an endpoint with no connections.
func (s *Server) DeleteForge(ctx context.Context, r *pb.ForgeRequest) (*emptypb.Empty, error) {
	return call(s, ctx, true, func(ctx context.Context, _ forgeauth.StaticToken) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, s.manager.DeleteForge(ctx, broker.ID(r.GetId()))
	})
}

// CreateConnection validates and seals a forge credential.
func (s *Server) CreateConnection(ctx context.Context, r *pb.CreateConnectionRequest) (*pb.Connection, error) {
	return call(s, ctx, true, func(ctx context.Context, _ forgeauth.StaticToken) (*pb.Connection, error) {
		c, err := s.manager.CreateConnection(ctx, broker.ID(r.GetForgeId()), r.GetName(), forgeauth.StaticToken(r.GetCredential()))
		return connection(c), err
	})
}

// ListConnections lists owner connection metadata only.
func (s *Server) ListConnections(ctx context.Context, _ *emptypb.Empty) (*pb.ListConnectionsResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, _ forgeauth.StaticToken) (*pb.ListConnectionsResponse, error) {
		values, err := s.manager.ListConnections(ctx)
		result := &pb.ListConnectionsResponse{}
		for _, c := range values {
			result.Connections = append(result.Connections, connection(c))
		}
		return result, err
	})
}

// GetConnection reads owner connection metadata only.
func (s *Server) GetConnection(ctx context.Context, r *pb.ConnectionRequest) (*pb.Connection, error) {
	return call(s, ctx, true, func(ctx context.Context, _ forgeauth.StaticToken) (*pb.Connection, error) {
		c, err := s.manager.GetConnection(ctx, broker.ID(r.GetId()))
		return connection(c), err
	})
}

// TestConnection refreshes credential status and identity using Whoami.
func (s *Server) TestConnection(ctx context.Context, r *pb.ConnectionRequest) (*pb.Connection, error) {
	return call(s, ctx, true, func(ctx context.Context, _ forgeauth.StaticToken) (*pb.Connection, error) {
		c, err := s.manager.TestConnection(ctx, broker.ID(r.GetId()))
		return connection(c), err
	})
}

// RotateCredential validates and atomically replaces a forge credential.
func (s *Server) RotateCredential(ctx context.Context, r *pb.RotateCredentialRequest) (*pb.Connection, error) {
	return call(s, ctx, true, func(ctx context.Context, _ forgeauth.StaticToken) (*pb.Connection, error) {
		c, err := s.manager.RotateCredential(ctx, broker.ID(r.GetId()), forgeauth.StaticToken(r.GetCredential()))
		return connection(c), err
	})
}

// DeleteConnection removes connection metadata, ciphertext and client grants.
func (s *Server) DeleteConnection(ctx context.Context, r *pb.ConnectionRequest) (*emptypb.Empty, error) {
	return call(s, ctx, true, func(ctx context.Context, _ forgeauth.StaticToken) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, s.manager.DeleteConnection(ctx, broker.ID(r.GetId()))
	})
}

// CreateClient returns the new client's random token once.
func (s *Server) CreateClient(ctx context.Context, r *pb.CreateClientRequest) (*pb.CreateClientResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, _ forgeauth.StaticToken) (*pb.CreateClientResponse, error) {
		ids := make([]broker.ID, len(r.GetConnectionIds()))
		for i, id := range r.GetConnectionIds() {
			ids[i] = broker.ID(id)
		}
		c, token, err := s.manager.CreateClient(ctx, r.GetName(), ids, r.GetCanIssueTokens())
		return &pb.CreateClientResponse{Client: client(c), Token: string(token)}, err
	})
}

// ListClients lists owner client grants, without hashes or tokens.
func (s *Server) ListClients(ctx context.Context, _ *emptypb.Empty) (*pb.ListClientsResponse, error) {
	return call(s, ctx, true, func(ctx context.Context, _ forgeauth.StaticToken) (*pb.ListClientsResponse, error) {
		values, err := s.manager.ListClients(ctx)
		result := &pb.ListClientsResponse{}
		for _, c := range values {
			result.Clients = append(result.Clients, client(c))
		}
		return result, err
	})
}

// DeleteClient immediately revokes a client token.
func (s *Server) DeleteClient(ctx context.Context, r *pb.ClientRequest) (*emptypb.Empty, error) {
	return call(s, ctx, true, func(ctx context.Context, _ forgeauth.StaticToken) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, s.manager.DeleteClient(ctx, broker.ID(r.GetId()))
	})
}

// IssueToken discloses the forge credential only to a scoped, permitted client.
func (s *Server) IssueToken(ctx context.Context, r *pb.IssueTokenRequest) (*pb.IssueTokenResponse, error) {
	return call(s, ctx, false, func(ctx context.Context, token forgeauth.StaticToken) (*pb.IssueTokenResponse, error) {
		issued, err := s.manager.IssueToken(ctx, token, broker.ID(r.GetConnectionId()))
		return &pb.IssueTokenResponse{Kind: string(issued.Kind), BaseUrl: issued.BaseURL, Token: string(issued.Token)}, err
	})
}

// VerifyClient checks the bearer token's scope and connection health.
func (s *Server) VerifyClient(ctx context.Context, r *pb.VerifyClientRequest) (*pb.Client, error) {
	return call(s, ctx, false, func(ctx context.Context, token forgeauth.StaticToken) (*pb.Client, error) {
		c, err := s.manager.VerifyClient(ctx, token, broker.ID(r.GetConnectionId()))
		return client(c), err
	})
}

// ListClientConnections lists the bearer token's connections, with kind and status.
func (s *Server) ListClientConnections(ctx context.Context, _ *emptypb.Empty) (*pb.ListClientConnectionsResponse, error) {
	return call(s, ctx, false, func(ctx context.Context, token forgeauth.StaticToken) (*pb.ListClientConnectionsResponse, error) {
		values, err := s.manager.ClientConnections(ctx, token)
		result := &pb.ListClientConnectionsResponse{}
		for _, c := range values {
			result.Connections = append(result.Connections, &pb.ClientConnection{Id: string(c.ID), Name: c.Name, Kind: string(c.Kind), Status: string(c.Status)})
		}
		return result, err
	})
}
