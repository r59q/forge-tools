// Package api exposes the symbols library over gRPC and generated REST
// routes.
package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-symbols/library/navigation"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
	pb "github.com/r59q/forge-tools/forge-symbols/service/symbolspb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/tap"
)

// Request framing allowed beyond the content: per file for its path and
// fields, and per request for the target.
const (
	fileFraming    = 1 << 10
	requestFraming = 64 << 10
)

// ErrInvalid means New was given no analyzer or token, or a nonpositive
// timeout.
var ErrInvalid = errors.New("api: invalid configuration")

// errBadRequest is returned for invalid request fields.
var errBadRequest = errors.New("api: invalid request")

// Server authenticates every RPC, including direct grpc-gateway dispatch.
// It never logs requests or responses, keeps nothing between requests and
// returns only a status code on failure.
type Server struct {
	pb.UnimplementedSymbolsServiceServer
	analyzer *symbols.Analyzer
	token    forgeauth.Credentials
	timeout  time.Duration
}

// New returns a server over analyzer. Callers must present the token from
// token as Authorization: Bearer. Each RPC runs for at most timeout.
func New(analyzer *symbols.Analyzer, token forgeauth.Credentials, timeout time.Duration) (*Server, error) {
	if analyzer == nil || token == nil || timeout <= 0 {
		return nil, ErrInvalid
	}

	return &Server{analyzer: analyzer, token: token, timeout: timeout}, nil
}

// MessageBytes is the largest gRPC request: the analyzer's TotalBytes of
// content plus framing for Files files.
func (s *Server) MessageBytes() int {
	limits := s.analyzer.Limits()

	return limits.TotalBytes + framing(limits)
}

// BodyBytes is the largest REST body: MessageBytes with the content as
// base64.
func (s *Server) BodyBytes() int64 {
	limits := s.analyzer.Limits()

	return int64(base64.StdEncoding.EncodedLen(limits.TotalBytes) + framing(limits))
}

func framing(limits symbols.Limits) int {
	return limits.Files*fileFraming + requestFraming
}

// GRPCOptions returns the options a gRPC server for s needs: a receive
// limit of MessageBytes, and a token check that rejects callers before
// their request is read. RPCs check the token again.
func (s *Server) GRPCOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(s.MessageBytes()),
		grpc.InTapHandle(func(ctx context.Context, info *tap.Info) (context.Context, error) {
			return ctx, s.authorize(ctx, info.Header)
		}),
	}
}

// REST returns the generated routes. Only Authorization is forwarded,
// errors never echo input, and responses are not cached. The token is
// checked before the body is read, and bodies are capped at BodyBytes.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	mux := servicekit.NewMux(true)
	if err := pb.RegisterSymbolsServiceHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}

	limit := s.BodyBytes()
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
		if r.ContentLength > limit {
			fail(w, r, servicekit.Status(codes.ResourceExhausted))
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, limit)
		mux.ServeHTTP(w, r)
	}), nil
}

// authorize checks the Authorization value of gRPC metadata.
func (s *Server) authorize(ctx context.Context, md metadata.MD) error {
	return servicekit.Authorize(metadata.NewIncomingContext(ctx, md), s.token)
}

// call authorizes and runs fn within the timeout.
func call[T any](s *Server, ctx context.Context, fn func(context.Context, *symbols.Analyzer) (*T, error)) (*T, error) {
	return servicekit.Call(ctx, servicekit.RPC[*symbols.Analyzer]{
		Timeout: s.timeout,
		Token:   s.token,
		Client:  func(context.Context) (*symbols.Analyzer, error) { return s.analyzer, nil },
		Code:    code,
	}, fn)
}

// code maps context and library errors to a status code.
func code(err error) codes.Code {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return servicekit.ForgeCode(err)
	case errors.Is(err, symbols.ErrUnsupported):
		return codes.Unimplemented
	case errors.Is(err, symbols.ErrTooLarge), errors.Is(err, symbols.ErrMemory):
		return codes.ResourceExhausted
	case errors.Is(err, symbols.ErrTimeout):
		return codes.DeadlineExceeded
	case errors.Is(err, navigation.ErrInvalidTarget), errors.Is(err, errBadRequest):
		return codes.InvalidArgument
	case errors.Is(err, navigation.ErrNotFound):
		return codes.NotFound
	}

	return codes.Internal
}
