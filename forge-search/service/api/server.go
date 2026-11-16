// Package api exposes the search library over gRPC and generated REST
// routes: indexes of forge-checkouts checkouts, and searches with or
// without them.
package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-search/library/checkout"
	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-search/library/sqlite"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/tap"
)

// RequestBytes is the largest request, gRPC or REST. Requests hold names,
// IDs and query text only.
const RequestBytes = 64 << 10

// DefaultMaxIndexes is the default for Config.MaxIndexes.
const DefaultMaxIndexes = 32

// ErrInvalid means New was given an incomplete configuration.
var ErrInvalid = errors.New("api: invalid configuration")

// Request errors, each mapped to one status code.
var (
	errBadRequest  = errors.New("api: invalid request")
	errBusy        = errors.New("api: index is being changed")
	errTooMany     = errors.New("api: too many indexes")
	errNotReady    = errors.New("api: checkout is not ready")
	errNotReadable = errors.New("api: checkout is not readable here")
	errUnavailable = errors.New("api: checkouts service unavailable")
	errJournal     = errors.New("api: cannot journal a checkout")
)

// Config holds what a Server needs.
type Config struct {
	// Engine holds the indexes.
	Engine *search.Engine

	// Checkpoint, when set, runs after an index is deleted so that its
	// content leaves the storage files, as sqlite.Store.Checkpoint does.
	Checkpoint func(context.Context) error

	// Checkouts resolves checkout IDs, and creates and removes the
	// checkouts of repository references.
	Checkouts Checkouts

	// Journal records the checkouts the server created until it removes
	// them.
	Journal Journal

	// Root is where checkout directories are read.
	Root Root

	// Token is the service token callers must present.
	Token forgeauth.Credentials

	// Timeout bounds each RPC but IndexCheckout, IndexRepository and
	// SearchRepository; IndexTimeout bounds those. A SearchRepository of an
	// index searches within Timeout.
	Timeout, IndexTimeout time.Duration

	// MaxIndexes is the most indexes IndexCheckout creates. Zero uses
	// DefaultMaxIndexes.
	MaxIndexes int

	// Now returns the time recorded with an indexing. Nil uses time.Now.
	Now func() time.Time
}

// Server authenticates every RPC, including direct grpc-gateway dispatch.
// It never logs requests or responses and returns only a status code on
// failure.
type Server struct {
	pb.UnimplementedSearchServiceServer
	config Config

	// mu guards busy, the indexes an RPC is changing, and orders index
	// creation against the MaxIndexes check.
	// It also guards live, the temporary checkouts calls are using.
	mu   sync.Mutex
	busy map[string]bool
	live map[string]bool

	// slots holds one value per temporary checkout, and sweeping is held
	// by the one running Sweep.
	slots    chan struct{}
	sweeping sync.Mutex
}

// New returns a server for config.
func New(config Config) (*Server, error) {
	valid := config.Engine != nil && config.Checkouts != nil && config.Journal != nil && config.Token != nil &&
		config.Timeout > 0 && config.IndexTimeout > 0 && config.MaxIndexes >= 0 && config.Root.valid()
	if !valid {
		return nil, ErrInvalid
	}

	if config.MaxIndexes == 0 {
		config.MaxIndexes = DefaultMaxIndexes
	}
	if config.Now == nil {
		config.Now = time.Now
	}

	return &Server{
		config: config,
		busy:   map[string]bool{},
		live:   map[string]bool{},
		slots:  make(chan struct{}, MaxTemporaryCheckouts),
	}, nil
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

// REST returns the generated routes. Only Authorization is forwarded,
// errors never echo input, and responses are not cached. The token is
// checked before the body is read, and bodies are capped at RequestBytes.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	mux := servicekit.NewMux(true)
	if err := pb.RegisterSearchServiceHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}

	fail := func(w http.ResponseWriter, r *http.Request, err error) {
		_, marshaler := runtime.MarshalerForRequest(mux, r)
		runtime.HTTPError(r.Context(), mux, marshaler, w, r, err)
	}

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
		mux.ServeHTTP(w, r)
	}), nil
}

// authorize checks the Authorization value of gRPC metadata.
func (s *Server) authorize(ctx context.Context, md metadata.MD) error {
	return servicekit.Authorize(metadata.NewIncomingContext(ctx, md), s.config.Token)
}

// call authorizes and runs fn within timeout.
func call[T any](s *Server, ctx context.Context, timeout time.Duration, fn func(context.Context, *search.Engine) (*T, error)) (*T, error) {
	return servicekit.Call(ctx, servicekit.RPC[*search.Engine]{
		Timeout: timeout,
		Token:   s.config.Token,
		Client:  func(context.Context) (*search.Engine, error) { return s.config.Engine, nil },
		Code:    code,
	}, fn)
}

// reserve marks an index as being changed until release is called. An
// index already being changed returns errBusy.
func (s *Server) reserve(index string) (release func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.busy[index] {
		return nil, errBusy
	}
	s.busy[index] = true

	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.busy, index)
	}, nil
}

// code maps context, library and request errors to a status code.
func code(err error) codes.Code {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return servicekit.ForgeCode(err)
	case errors.Is(err, errBadRequest), errors.Is(err, search.ErrInvalid), errors.Is(err, ErrRejected):
		return codes.InvalidArgument
	case errors.Is(err, ErrNotFound), errors.Is(err, search.ErrNotFound):
		return codes.NotFound
	case errors.Is(err, search.ErrExists):
		return codes.AlreadyExists
	case errors.Is(err, errNotReady), errors.Is(err, errNotReadable), errors.Is(err, checkout.ErrUnreadable),
		errors.Is(err, ErrConflict):
		return codes.FailedPrecondition
	case errors.Is(err, errBusy), errors.Is(err, checkout.ErrChanged):
		return codes.Aborted
	case errors.Is(err, errTooMany), errors.Is(err, errCheckoutsBusy), errors.Is(err, ErrTooLarge),
		errors.Is(err, search.ErrTooLarge),
		errors.Is(err, search.ErrFull),
		errors.Is(err, sqlite.ErrDiskFull):
		return codes.ResourceExhausted
	case errors.Is(err, errUnavailable), errors.Is(err, sqlite.ErrReadOnly):
		return codes.Unavailable
	}

	return codes.Internal
}
