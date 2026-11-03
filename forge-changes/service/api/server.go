// Package api exposes changes over gRPC and generated REST routes.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/r59q/forge-tools/forge-changes/library/changes"
	pb "github.com/r59q/forge-tools/forge-changes/service/changespb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc/codes"
)

// ErrInvalid means New was given nil sources or credential source, or a
// nonpositive timeout.
var ErrInvalid = errors.New("api: invalid configuration")

// errNoClient means Sources returned neither a client nor an error.
var errNoClient = errors.New("api: no client")

// Server authenticates every RPC, including direct grpc-gateway dispatch.
// It never logs requests, responses, credentials or upstream errors, and
// returns only a status code to callers.
type Server struct {
	pb.UnimplementedChangesServiceServer
	sources Sources
	token   forgeauth.Credentials
	timeout time.Duration
}

// jscpd:ignore-start - each service wraps its own library client.

// Sources returns a client for the current sources. The server calls it once
// per RPC, so sources may change while it runs. Errors map to Unavailable
// unless they wrap a context error.
type Sources func(ctx context.Context) (*changes.Client, error)

// Fixed returns Sources that always use client.
func Fixed(client *changes.Client) Sources {
	return func(context.Context) (*changes.Client, error) { return client, nil }
}

// New constructs a service over sources. Callers must present the token from
// token as Authorization: Bearer. Each RPC runs for at most timeout.
// Runtime configuration should use forgeauth.FileToken or forgeauth.EnvToken.
func New(sources Sources, token forgeauth.Credentials, timeout time.Duration) (*Server, error) {
	if sources == nil || token == nil || timeout <= 0 {
		return nil, ErrInvalid
	}

	return &Server{sources: sources, token: token, timeout: timeout}, nil
}

// jscpd:ignore-end

// REST registers generated routes. Authorization is the sole forwarded header;
// Grpc-Metadata-* headers cannot inject credentials. Errors never echo input
// or forge responses. Responses are not cached.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	mux := servicekit.NewMux(false)
	if err := pb.RegisterChangesServiceHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}

	return servicekit.Secure(mux), nil
}

// call runs one authorized unary RPC within the timeout.
func call[T any](s *Server, ctx context.Context, fn func(context.Context, *changes.Client) (*T, error)) (*T, error) {
	return servicekit.Call(ctx, servicekit.RPC[*changes.Client]{
		Timeout: s.timeout,
		Token:   s.token,
		Client:  s.client,
		Code:    code,
	}, fn)
}

// client returns the client for the current sources. A nil client is an
// error, so calls never run without one.
func (s *Server) client(ctx context.Context) (*changes.Client, error) {
	client, err := s.sources(ctx)
	if err == nil && client == nil {
		return nil, errNoClient
	}

	return client, err
}

// rpcError maps library and forge errors to a bare status.
func rpcError(err error) error {
	return servicekit.Status(code(err))
}

// code maps context errors, then library errors, then forge errors, to a
// status code.
func code(err error) codes.Code {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return servicekit.ForgeCode(err)
	case errors.Is(err, changes.ErrInvalid), errors.Is(err, changes.ErrUnknownSource):
		return codes.InvalidArgument
	case errors.Is(err, changes.ErrUnsupported):
		return codes.Unimplemented
	case errors.Is(err, changes.ErrTooLarge):
		return codes.ResourceExhausted
	case errors.Is(err, changes.ErrHeadChanged), errors.Is(err, changes.ErrMergeRejected):
		return codes.FailedPrecondition
	}

	return servicekit.ForgeCode(err)
}
