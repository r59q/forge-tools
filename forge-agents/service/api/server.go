// Package api exposes the forge-agents manager over gRPC, generated REST
// routes and an NDJSON event stream, and creates checkout workspaces with
// forge-checkouts.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/agent"
	"github.com/r59q/forge-tools/forge-agents/library/workspace"
	pb "github.com/r59q/forge-tools/forge-agents/service/agentspb"
	"github.com/r59q/forge-tools/forge-agents/service/store"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/tap"
)

// RequestBytes is the largest request, gRPC or REST. Requests hold IDs,
// settings and message text.
const RequestBytes = 1 << 20

// PageBytes bounds a ListEvents page. A page holds at least one event,
// which the manager bounds to well below this.
const PageBytes = 4 << 20

// ErrInvalid means New was given an incomplete configuration.
var ErrInvalid = errors.New("api: invalid configuration")

// errBadRequest is a request the server rejects before the manager sees it.
var errBadRequest = errors.New("api: invalid request")

// Config holds what a Server needs.
type Config struct {
	// Manager runs the agents.
	Manager *agent.Manager

	// Token is the service token callers must present.
	Token forgeauth.Credentials

	// Timeout bounds each unary RPC. Turns run on after the RPC that
	// started them returns.
	Timeout time.Duration

	// StreamTimeout bounds each event stream; callers resume after it.
	StreamTimeout time.Duration
}

// Server authenticates every RPC, including direct grpc-gateway dispatch.
// It never logs requests or responses and returns only a status code on
// failure.
type Server struct {
	pb.UnimplementedAgentsServiceServer
	config Config
}

// New returns a server for config.
func New(config Config) (*Server, error) {
	if config.Manager == nil || config.Token == nil || config.Timeout <= 0 || config.StreamTimeout <= 0 {
		return nil, ErrInvalid
	}
	return &Server{config: config}, nil
}

// GRPCOptions returns the options a gRPC server for s needs: a receive
// limit of RequestBytes, and a token check that rejects callers before
// their request is read. RPCs check the token again.
func (s *Server) GRPCOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(RequestBytes),
		grpc.InTapHandle(func(ctx context.Context, info *tap.Info) (context.Context, error) {
			return ctx, s.authorize(ctx, info.Header)
		}),
	}
}

// REST returns the generated routes and GET /v1/agents/{id}/stream. Only
// Authorization is forwarded, errors never echo input, and responses are
// not cached. The token is checked before the body is read, and bodies are
// capped at RequestBytes.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	mux := servicekit.NewMux(true)
	if err := pb.RegisterAgentsServiceHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}
	fail := func(w http.ResponseWriter, r *http.Request, err error) {
		_, marshaler := runtime.MarshalerForRequest(mux, r)
		runtime.HTTPError(r.Context(), mux, marshaler, w, r, err)
	}
	routes := http.NewServeMux()
	routes.Handle("/", mux)
	routes.HandleFunc("GET /v1/agents/{id}/stream", func(w http.ResponseWriter, r *http.Request) { s.stream(w, r, fail) })

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if err := s.authorize(r.Context(), metadata.MD{"authorization": r.Header.Values("Authorization")}); err != nil {
			fail(w, r, err)
			return
		}
		if r.ContentLength > RequestBytes {
			fail(w, r, servicekit.Status(codes.ResourceExhausted))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, RequestBytes)
		routes.ServeHTTP(w, r)
	}), nil
}

// authorize checks the Authorization value of gRPC metadata.
func (s *Server) authorize(ctx context.Context, md metadata.MD) error {
	return servicekit.Authorize(metadata.NewIncomingContext(ctx, md), s.config.Token)
}

// call authorizes and runs fn within the timeout.
func call[T any](ctx context.Context, s *Server, fn func(context.Context, *agent.Manager) (*T, error)) (*T, error) {
	return servicekit.Call(ctx, servicekit.RPC[*agent.Manager]{
		Timeout: s.config.Timeout,
		Token:   s.config.Token,
		Client:  func(context.Context) (*agent.Manager, error) { return s.config.Manager, nil },
		Code:    code,
	}, fn)
}

// code maps context, library and request errors to a status code.
func code(err error) codes.Code {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return servicekit.ForgeCode(err)
	case errors.Is(err, errBadRequest), errors.Is(err, agent.ErrInvalid), errors.Is(err, workspace.ErrInvalid),
		errors.Is(err, adapter.ErrInvalid):
		return codes.InvalidArgument
	case errors.Is(err, agent.ErrNotFound), errors.Is(err, ErrCheckoutNotFound):
		return codes.NotFound
	case errors.Is(err, agent.ErrState), errors.Is(err, workspace.ErrUnavailable), errors.Is(err, ErrCheckoutConflict):
		return codes.FailedPrecondition
	case errors.Is(err, agent.ErrLimit), errors.Is(err, agent.ErrTranscriptFull), errors.Is(err, ErrCheckoutTooLarge):
		return codes.ResourceExhausted
	case errors.Is(err, ErrCheckoutsUnavailable), errors.Is(err, store.ErrStorage):
		return codes.Unavailable
	}
	return codes.Internal
}
