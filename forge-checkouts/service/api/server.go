// Package api exposes checkouts over gRPC and generated REST routes.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/r59q/forge-tools/forge-checkouts/library/checkouts"
	pb "github.com/r59q/forge-tools/forge-checkouts/service/checkoutspb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

// ErrInvalid means New was given a nil manager or credential source, or a
// nonpositive timeout.
var ErrInvalid = errors.New("api: invalid configuration")

// Server authenticates every RPC, including direct grpc-gateway dispatch.
// It never logs requests, responses, credentials or upstream errors, and
// returns only a status code to callers.
type Server struct {
	pb.UnimplementedCheckoutsServiceServer
	manager *checkouts.Manager
	token   forgeauth.Credentials
	timeout time.Duration
}

// New exposes manager using a rotating bearer credential and per-request timeout.
func New(manager *checkouts.Manager, token forgeauth.Credentials, timeout time.Duration) (*Server, error) {
	if manager == nil || token == nil || timeout <= 0 {
		return nil, ErrInvalid
	}

	return &Server{manager: manager, token: token, timeout: timeout}, nil
}

// REST registers generated routes. Authorization is the sole forwarded header;
// Grpc-Metadata-* headers cannot inject credentials. Errors never echo input
// or forge responses. Responses are not cached.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	mux := servicekit.NewMux(false)
	if err := pb.RegisterCheckoutsServiceHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}

	return servicekit.Secure(s.authenticated(mux)), nil
}

// authenticated rejects REST requests without exactly one valid
// Authorization header before they reach next.
func (s *Server) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		ctx := metadata.NewIncomingContext(r.Context(), metadata.Pairs("authorization", r.Header.Get("Authorization")))
		if len(values) != 1 || servicekit.Authorize(ctx, s.token) != nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(w, r)
	})
}

// call authorizes an RPC and runs fn within the timeout.
func call[T any](s *Server, ctx context.Context, fn func(context.Context) (*T, error)) (*T, error) {
	rpc := servicekit.RPC[*checkouts.Manager]{
		Timeout: s.timeout,
		Token:   s.token,
		Client:  func(context.Context) (*checkouts.Manager, error) { return s.manager, nil },
		Code:    code,
	}

	return servicekit.Call(ctx, rpc, func(ctx context.Context, _ *checkouts.Manager) (*T, error) {
		return fn(ctx)
	})
}

// code maps context errors, then library errors, then forge errors to a
// status code.
func code(err error) codes.Code {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return servicekit.ForgeCode(err)
	case errors.Is(err, checkouts.ErrInvalid):
		return codes.InvalidArgument
	case errors.Is(err, checkouts.ErrTooLarge):
		return codes.ResourceExhausted
	case errors.Is(err, checkouts.ErrConflict):
		return codes.FailedPrecondition
	case errors.Is(err, checkouts.ErrNotFound):
		return codes.NotFound
	}

	return servicekit.ForgeCode(err)
}
