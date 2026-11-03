// Package api exposes actions over gRPC and generated REST routes.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/r59q/forge-tools/forge-actions/library/actions"
	pb "github.com/r59q/forge-tools/forge-actions/service/actionspb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc/codes"
)

// ErrInvalid means New was given nil sources or credential source, or a
// nonpositive timeout.
var ErrInvalid = errors.New("api: invalid configuration")

// Server authenticates every RPC, including direct grpc-gateway dispatch.
// It never logs requests, responses, credentials or upstream errors, and
// returns only a status code to callers.
type Server struct {
	pb.UnimplementedActionsServiceServer
	sources Sources
	token   forgeauth.Credentials
	timeout time.Duration
}

// Sources returns a client for the current sources. The server calls it once
// per RPC, so sources may change while it runs. Errors map to Unavailable
// unless they wrap a context error.
type Sources func(ctx context.Context) (*actions.Client, error)

// Fixed returns Sources that always use client.
func Fixed(client *actions.Client) Sources {
	return func(context.Context) (*actions.Client, error) { return client, nil }
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

// REST registers generated routes. Authorization is the sole forwarded header;
// Grpc-Metadata-* headers cannot inject credentials. Errors never echo input
// or forge responses. Responses are not cached.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	mux := servicekit.NewMux(true)
	if err := pb.RegisterActionsServiceHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}

	return servicekit.Secure(mux), nil
}

// call runs one authorized unary RPC within the timeout.
func call[T any](s *Server, ctx context.Context, fn func(context.Context, *actions.Client) (*T, error)) (*T, error) {
	return servicekit.Call(ctx, servicekit.RPC[*actions.Client]{
		Timeout: s.timeout,
		Token:   s.token,
		Client:  s.sources,
		Code:    code,
	}, fn)
}

// rpcError maps library and forge errors to a bare status.
func rpcError(err error) error {
	return servicekit.Status(code(err))
}

// code maps library errors, then context and forge errors, to a status code.
func code(err error) codes.Code {
	switch {
	case errors.Is(err, actions.ErrInvalid), errors.Is(err, actions.ErrUnknownSource):
		return codes.InvalidArgument
	case errors.Is(err, actions.ErrUnsupported):
		return codes.Unimplemented
	case errors.Is(err, actions.ErrTooLarge):
		return codes.ResourceExhausted
	}

	return servicekit.ForgeCode(err)
}
