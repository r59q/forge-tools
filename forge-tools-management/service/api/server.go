// Package api exposes management over REST, gRPC and a server-rendered web UI.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
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

// ListRepositories returns the configured state and complete repository list.
// Backend credential rejection is Unavailable, never Unauthenticated.
func (s *Server) ListRepositories(ctx context.Context, _ *emptypb.Empty) (*pb.ListRepositoriesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	listing, err := s.client.ListRepositories(ctx)
	if err != nil {
		code := codes.Unavailable
		if errors.Is(err, context.Canceled) {
			code = codes.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			code = codes.DeadlineExceeded
		}
		return nil, status.Error(code, code.String())
	}
	out := &pb.ListRepositoriesResponse{Configured: listing.Configured}
	for _, r := range listing.Repositories {
		out.Repositories = append(out.Repositories, &pb.Repository{
			SourceId: r.Source.ID, Forge: string(r.Source.Kind), Host: r.Source.Host,
			Path: r.Path, Description: r.Description, Visibility: string(r.Visibility),
		})
	}
	return out, nil
}

// HTTP registers the generated REST endpoint and the web UI. REST accepts
// Bearer auth; the UI uses HTTP Basic with username operator and the operator
// token as password. Authorization is the sole credential header accepted.
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if r.URL.Path == "/" || r.URL.Path == "/style.css" {
			s.web(w, r)
			return
		}
		if r.URL.Path == "/v1/repositories" && r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rest.ServeHTTP(w, r)
	}), nil
}
