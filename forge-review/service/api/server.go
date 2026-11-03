// Package api exposes review snapshots and feedback over gRPC and generated REST routes.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-review/library/review"
	pb "github.com/r59q/forge-tools/forge-review/service/reviewpb"
	"google.golang.org/grpc/codes"
)

// ErrInvalid means New was given nil storage or credential source, or a
// nonpositive timeout.
var ErrInvalid = errors.New("api: invalid configuration")

// Server authenticates every RPC, including direct grpc-gateway dispatch.
// It never logs requests, responses, credentials or upstream errors, and
// returns only a status code to callers.
type Server struct {
	pb.UnimplementedReviewServiceServer
	sources Sources
	store   review.Store
	token   forgeauth.Credentials
	timeout time.Duration
}

// Sources returns current connector sessions. It is called only for connected
// operations; standalone reviews remain usable during connector outages. Errors map to Unavailable
// unless they wrap a context error.
type Sources func(ctx context.Context) ([]review.Source, error)

// New constructs a service over storage and optional connected sources. Callers must present the token from
// token as Authorization: Bearer. Each RPC runs for at most timeout.
// Runtime configuration should use forgeauth.FileToken or forgeauth.EnvToken.
func New(store review.Store, sources Sources, token forgeauth.Credentials, timeout time.Duration) (*Server, error) {
	if store == nil || token == nil || timeout <= 0 {
		return nil, ErrInvalid
	}

	return &Server{
		store:   store,
		sources: sources,
		token:   token,
		timeout: timeout,
	}, nil
}

// maxBody caps REST request bodies, which may hold large patches.
const maxBody = 20 << 20

// REST registers generated routes. Authorization is the sole forwarded header;
// Grpc-Metadata-* headers cannot inject credentials. Errors never echo input
// or forge responses. Responses are not cached.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	mux := servicekit.NewMux(false)
	if err := pb.RegisterReviewServiceHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}

	return secure(mux), nil
}

// secure is servicekit.Secure with a body cap that fits patches.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

// call authorizes an RPC, builds a client and runs fn within the timeout.
// Connected calls also read the current sources.
func call[T any](s *Server, ctx context.Context, connected bool, fn func(context.Context, *review.Client) (*T, error)) (*T, error) {
	rpc := servicekit.RPC[*Server]{
		Timeout: s.timeout,
		Token:   s.token,
		Client:  func(context.Context) (*Server, error) { return s, nil },
		Code:    code,
	}

	return servicekit.Call(ctx, rpc, func(ctx context.Context, s *Server) (*T, error) {
		client, err := s.client(ctx, connected)
		if err != nil {
			return nil, err
		}

		return fn(ctx, client)
	})
}

// client builds a review client, with the current sources when connected.
func (s *Server) client(ctx context.Context, connected bool) (*review.Client, error) {
	if !connected {
		return review.New(s.store)
	}

	if s.sources == nil {
		return nil, review.ErrUnsupported
	}

	sources, err := s.sources(ctx)
	if err != nil {
		return nil, err
	}

	return review.New(s.store, sources...)
}

// code maps context errors, then library errors, then forge errors to a
// status code.
func code(err error) codes.Code {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return servicekit.ForgeCode(err)
	case errors.Is(err, review.ErrInvalid):
		return codes.InvalidArgument
	case errors.Is(err, review.ErrUnsupported):
		return codes.Unimplemented
	case errors.Is(err, review.ErrTooLarge):
		return codes.ResourceExhausted
	case errors.Is(err, review.ErrConflict):
		return codes.FailedPrecondition
	case errors.Is(err, review.ErrNotFound):
		return codes.NotFound
	}

	return servicekit.ForgeCode(err)
}
