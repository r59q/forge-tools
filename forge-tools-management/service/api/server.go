// Package api exposes management over REST, gRPC and a server-rendered web UI.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server authenticates every management RPC and web request. It does not log
// requests, responses or errors from the backend.
type Server struct {
	pb.UnimplementedManagementServer
	client  *management.Client
	token   forgeauth.Credentials
	timeout time.Duration
}

// New constructs a server with a rotating operator credential source and a
// positive request timeout. This token must differ from backend credentials.
func New(client *management.Client, token forgeauth.Credentials, timeout time.Duration) (*Server, error) {
	if client == nil || token == nil || timeout <= 0 {
		return nil, errors.New("management API: invalid configuration")
	}
	return &Server{client: client, token: token, timeout: timeout}, nil
}

func (s *Server) authorize(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	h := http.Header{}
	for _, v := range md.Get("authorization") {
		h.Add("Authorization", v)
	}
	token, ok := forgeauth.AuthorizationToken(h, "Bearer")
	expected, err := s.token.Token(ctx)
	if ok && err == nil && expected != "" {
		a, b := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(expected))
		if subtle.ConstantTimeCompare(a[:], b[:]) == 1 {
			return nil
		}
	}
	return status.Error(codes.Unauthenticated, "authentication required")
}

// call authorizes the request and runs fn with the request timeout. Errors
// carry only a status code.
func call[T any](s *Server, ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.authorize(ctx); err != nil {
		return zero, err
	}
	result, err := fn(ctx)
	if err != nil {
		code := codes.Unavailable
		switch {
		case errors.Is(err, context.Canceled):
			code = codes.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			code = codes.DeadlineExceeded
		case errors.Is(err, management.ErrNotConfigured):
			code = codes.Unimplemented
		case errors.Is(err, management.ErrInvalid):
			code = codes.InvalidArgument
		case errors.Is(err, management.ErrNotFound):
			code = codes.NotFound
		case errors.Is(err, management.ErrCheckFailed):
			code = codes.FailedPrecondition
		}
		return zero, status.Error(code, code.String())
	}
	return result, nil
}

// ListRepositories returns the configured state and complete repository list.
// Backend credential rejection is Unavailable, never Unauthenticated.
func (s *Server) ListRepositories(ctx context.Context, _ *emptypb.Empty) (*pb.ListRepositoriesResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ListRepositoriesResponse, error) {
		listing, err := s.client.ListRepositories(ctx)
		if err != nil {
			return nil, err
		}
		out := &pb.ListRepositoriesResponse{Configured: listing.Configured}
		for _, r := range listing.Repositories {
			out.Repositories = append(out.Repositories, &pb.Repository{
				SourceId: r.Source.ID, Forge: string(r.Source.Kind), Host: r.Source.Host,
				Path: r.Path, Description: r.Description, Visibility: string(r.Visibility),
			})
		}
		return out, nil
	})
}

// ListConnections returns the configured state and every connection.
func (s *Server) ListConnections(ctx context.Context, _ *emptypb.Empty) (*pb.ListConnectionsResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ListConnectionsResponse, error) {
		list, err := s.client.ListConnections(ctx)
		if err != nil {
			return nil, err
		}
		out := &pb.ListConnectionsResponse{Configured: list.Configured}
		for _, c := range list.Connections {
			out.Connections = append(out.Connections, connection(c))
		}
		return out, nil
	})
}

// AddConnection checks and stores a new forge credential.
func (s *Server) AddConnection(ctx context.Context, r *pb.AddConnectionRequest) (*pb.Connection, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.Connection, error) {
		c, err := s.client.AddConnection(ctx, management.NewConnection{
			Kind: forgeauth.Kind(r.GetForge()), Host: r.GetHost(), Name: r.GetName(), Credential: forgeauth.StaticToken(r.GetCredential()),
		})
		return connection(c), err
	})
}

// TestConnection rechecks a stored credential with its forge.
func (s *Server) TestConnection(ctx context.Context, r *pb.ConnectionRequest) (*pb.Connection, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.Connection, error) {
		c, err := s.client.TestConnection(ctx, r.GetId())
		return connection(c), err
	})
}

// RotateCredential replaces a connection's credential.
func (s *Server) RotateCredential(ctx context.Context, r *pb.RotateCredentialRequest) (*pb.Connection, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.Connection, error) {
		c, err := s.client.RotateCredential(ctx, r.GetId(), forgeauth.StaticToken(r.GetCredential()))
		return connection(c), err
	})
}

// RemoveConnection deletes a connection and its credential.
func (s *Server) RemoveConnection(ctx context.Context, r *pb.ConnectionRequest) (*emptypb.Empty, error) {
	return call(s, ctx, func(ctx context.Context) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, s.client.RemoveConnection(ctx, r.GetId())
	})
}

func connection(c management.Connection) *pb.Connection {
	out := &pb.Connection{
		Id: c.ID, Name: c.Name, Forge: string(c.Kind), BaseUrl: c.BaseURL, Login: c.Login, Status: string(c.Status),
	}
	if !c.CheckedAt.IsZero() {
		out.CheckedAt = timestamppb.New(c.CheckedAt)
	}
	return out
}

// HTTP registers the generated REST endpoints and the web UI. REST accepts
// Bearer auth; the UI uses HTTP Basic with username operator and the operator
// token as password. Authorization is the sole credential header accepted.
// Cross-origin browser requests that change state are rejected.
func (s *Server) HTTP(ctx context.Context) (http.Handler, error) {
	rest := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(func(string) (string, bool) { return "", false }),
		runtime.WithErrorHandler(func(ctx context.Context, mux *runtime.ServeMux, m runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
			code := status.Code(err)
			if code == codes.Unknown {
				code = codes.Internal
			}
			runtime.DefaultHTTPErrorHandler(ctx, mux, m, w, r, status.Error(code, code.String()))
		}),
	)
	if err := pb.RegisterManagementHandlerServer(ctx, rest, s); err != nil {
		return nil, err
	}
	pages := s.pages()
	origins := http.NewCrossOriginProtection()
	routes := origins.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			s.web(pages, w, r)
			return
		}
		if r.URL.Path == "/v1/repositories" && r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rest.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		routes.ServeHTTP(w, r)
	}), nil
}
