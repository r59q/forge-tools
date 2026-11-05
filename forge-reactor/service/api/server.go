// Package api exposes a reactor over gRPC and generated REST routes, and
// receives native forge webhooks over HTTP.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	pb "github.com/r59q/forge-tools/forge-reactor/service/reactorpb"
	"google.golang.org/grpc/codes"
)

// ErrInvalid means New was given no reactor or token, or a nonpositive
// timeout.
var ErrInvalid = errors.New("api: invalid configuration")

// Server authenticates every RPC, including direct grpc-gateway dispatch.
// It never logs requests, responses or credentials, and returns only a
// status code on failure.
type Server struct {
	pb.UnimplementedReactorServiceServer
	reactor *reactor.Reactor
	token   forgeauth.Credentials
	timeout time.Duration
}

// New returns a server over r. Callers must present the token from token as
// Authorization: Bearer. Each RPC runs for at most timeout.
func New(r *reactor.Reactor, token forgeauth.Credentials, timeout time.Duration) (*Server, error) {
	if r == nil || token == nil || timeout <= 0 {
		return nil, ErrInvalid
	}

	return &Server{reactor: r, token: token, timeout: timeout}, nil
}

// REST returns the generated routes behind servicekit.Secure. Only
// Authorization is forwarded, and errors never echo input.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	mux := servicekit.NewMux(false)
	if err := pb.RegisterReactorServiceHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}

	return servicekit.Secure(mux), nil
}

// Routes serves webhooks under /webhooks/ and everything else from rest.
func Routes(rest, webhooks http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/webhooks/", webhooks)
	mux.Handle("/", rest)

	return mux
}

// call authorizes and runs fn on the reactor within the timeout.
func call[T any](s *Server, ctx context.Context, fn func(context.Context, *reactor.Reactor) (*T, error)) (*T, error) {
	return servicekit.Call(ctx, servicekit.RPC[*reactor.Reactor]{
		Timeout: s.timeout,
		Token:   s.token,
		Client:  func(context.Context) (*reactor.Reactor, error) { return s.reactor, nil },
		Code:    code,
	}, fn)
}

// code maps context and reactor errors to a status code. A full delivery
// backlog is Unavailable, so callers retry later.
func code(err error) codes.Code {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return servicekit.ForgeCode(err)
	case errors.Is(err, reactor.ErrInvalid):
		return codes.InvalidArgument
	case errors.Is(err, reactor.ErrNotFound):
		return codes.NotFound
	case errors.Is(err, reactor.ErrExists):
		return codes.AlreadyExists
	case errors.Is(err, reactor.ErrOutOfRange):
		return codes.OutOfRange
	case errors.Is(err, reactor.ErrBusy):
		return codes.Unavailable
	}

	return codes.Internal
}
