// Package api exposes workspaces over gRPC and generated REST routes.
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
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-workspaces/library/workspaces"
	pb "github.com/r59q/forge-tools/forge-workspaces/service/workspacespb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// ErrInvalid means New was given a nil manager or credential source, or a
// nonpositive timeout.
var ErrInvalid = errors.New("api: invalid configuration")

// Server authenticates every RPC, including direct grpc-gateway dispatch.
// It never logs requests, responses, credentials or upstream errors, and
// returns only a status code to callers.
type Server struct {
	pb.UnimplementedWorkspacesServiceServer
	manager *workspaces.Manager
	token   forgeauth.Credentials
	timeout time.Duration
}

// New exposes manager using a rotating bearer credential and per-request timeout.
func New(manager *workspaces.Manager, token forgeauth.Credentials, timeout time.Duration) (*Server, error) {
	if manager == nil || token == nil || timeout <= 0 {
		return nil, ErrInvalid
	}
	return &Server{manager: manager, token: token, timeout: timeout}, nil
}

// REST registers generated routes. Authorization is the sole forwarded header;
// Grpc-Metadata-* headers cannot inject credentials. Errors never echo input
// or forge responses. Responses are not cached.
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
	if err := pb.RegisterWorkspacesServiceHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		ctx := metadata.NewIncomingContext(r.Context(), metadata.Pairs("authorization", r.Header.Get("Authorization")))
		if len(r.Header.Values("Authorization")) != 1 || s.authorize(ctx) != nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}

func (s *Server) authorize(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	h := http.Header{}
	for _, value := range md.Get("authorization") {
		h.Add("Authorization", value)
	}
	token, ok := forgeauth.AuthorizationToken(h, "Bearer")
	if !ok {
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	expected, err := s.token.Token(ctx)
	if err != nil || expected == "" {
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	a, b := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(expected))
	if subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	return nil
}

func call[T any](s *Server, ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.authorize(ctx); err != nil {
		return zero, err
	}
	result, err := fn(ctx)
	if err != nil {
		return zero, rpcError(err)
	}
	return result, nil
}

// rpcError maps library and forge errors to a bare status code. A forge
// rejecting the service's own credentials is Unavailable, not
// Unauthenticated, so callers do not mistake it for a bad service token.
func rpcError(err error) error {
	code := codes.Unavailable
	var forgeErr *forgehttp.Error
	switch {
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		code = codes.DeadlineExceeded
	case errors.Is(err, workspaces.ErrInvalid):
		code = codes.InvalidArgument
	case errors.Is(err, workspaces.ErrTooLarge):
		code = codes.ResourceExhausted
	case errors.Is(err, workspaces.ErrConflict):
		code = codes.FailedPrecondition
	case errors.Is(err, forgehttp.ErrNotFound), errors.Is(err, workspaces.ErrNotFound):
		code = codes.NotFound
	case errors.Is(err, forgehttp.ErrForbidden):
		code = codes.PermissionDenied
	case errors.Is(err, forgehttp.ErrRateLimited):
		code = codes.ResourceExhausted
	case errors.Is(err, forgehttp.ErrConflict):
		code = codes.FailedPrecondition
	case errors.As(err, &forgeErr) && (forgeErr.StatusCode == http.StatusBadRequest || forgeErr.StatusCode == http.StatusUnprocessableEntity):
		// The connector rejected the request arguments.
		code = codes.InvalidArgument
	}
	return status.Error(code, code.String())
}
