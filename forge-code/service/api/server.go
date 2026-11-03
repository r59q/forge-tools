// Package api exposes repository content over gRPC, generated REST routes
// and a raw download route.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/code"
	pb "github.com/r59q/forge-tools/forge-code/service/codepb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/tap"
)

// Size limits.
const (
	// MaxRecvMsgSize is the gRPC receive limit the server needs for
	// CreateCommit: 64 MiB of content plus overhead.
	MaxRecvMsgSize = 72 << 20
	// MaxFileBytes caps the content of GetFile and GetBlob.
	MaxFileBytes = 4 << 20
	// ChunkBytes caps the data of one streamed message.
	ChunkBytes = 64 << 10

	maxBodyBytes   = 1 << 20  // REST request bodies
	maxCommitBytes = 96 << 20 // POST /v1/commits: 64 MiB as base64 JSON
	maxStreams     = 100      // concurrent streams per gRPC connection
)

// Config defaults.
const (
	// DefaultStreamTimeout limits ReadFile, ReadBlob and GET /v1/raw.
	DefaultStreamTimeout = 10 * time.Minute
	// DefaultMaxInFlight is the default per-source in-flight limit.
	DefaultMaxInFlight = 8
)

// ErrInvalid means New was given nil sources or credential source, a
// nonpositive timeout, or a negative stream timeout or in-flight limit.
var ErrInvalid = errors.New("api: invalid configuration")

// errFileTooLarge is a GetFile or GetBlob over MaxFileBytes.
var errFileTooLarge = fmt.Errorf("%w: content over %d bytes; use a stream", code.ErrTooLarge, MaxFileBytes)

// Config configures a Server.
type Config struct {
	// Timeout limits each unary RPC. It is required.
	Timeout time.Duration
	// StreamTimeout limits each stream and raw download. Zero means
	// DefaultStreamTimeout.
	StreamTimeout time.Duration
	// MaxInFlight caps the concurrent requests per source; more are
	// RESOURCE_EXHAUSTED. Zero means DefaultMaxInFlight.
	MaxInFlight int
}

// Server authenticates every RPC and raw download, including direct
// grpc-gateway dispatch. It never logs requests, responses, credentials or
// upstream errors, and returns only a status code to callers.
type Server struct {
	pb.UnimplementedCodeServiceServer
	sources       Sources
	token         forgeauth.Credentials
	timeout       time.Duration
	streamTimeout time.Duration
	inFlight      *limiter
}

// Sources returns a client for the current sources. The server calls it once
// per request, so sources may change while it runs. Errors map to
// Unavailable unless they wrap a context error.
type Sources func(ctx context.Context) (*code.Client, error)

// Fixed returns Sources that always use client.
func Fixed(client *code.Client) Sources {
	return func(context.Context) (*code.Client, error) { return client, nil }
}

// New constructs a service over sources. Callers must present the token from
// token as Authorization: Bearer. Runtime configuration should use
// forgeauth.FileToken or forgeauth.EnvToken.
func New(sources Sources, token forgeauth.Credentials, c Config) (*Server, error) {
	if sources == nil || token == nil || c.Timeout <= 0 || c.StreamTimeout < 0 || c.MaxInFlight < 0 {
		return nil, ErrInvalid
	}
	if c.StreamTimeout == 0 {
		c.StreamTimeout = DefaultStreamTimeout
	}
	if c.MaxInFlight == 0 {
		c.MaxInFlight = DefaultMaxInFlight
	}

	return &Server{
		sources:       sources,
		token:         token,
		timeout:       c.Timeout,
		streamTimeout: c.StreamTimeout,
		inFlight:      &limiter{max: c.MaxInFlight, n: map[string]int{}},
	}, nil
}

// check checks the Authorization header of a REST request.
func (s *Server) check(ctx context.Context, h http.Header) error {
	return s.checkMD(ctx, metadata.MD{"authorization": h.Values("Authorization")})
}

// authorize checks the Authorization metadata of an RPC.
func (s *Server) authorize(ctx context.Context) error {
	return servicekit.Authorize(ctx, s.token)
}

// checkMD checks the Authorization value of gRPC metadata.
func (s *Server) checkMD(ctx context.Context, md metadata.MD) error {
	return servicekit.Authorize(metadata.NewIncomingContext(ctx, md), s.token)
}

// GRPCOptions returns the options a gRPC server for s needs: the
// MaxRecvMsgSize limit, 100 concurrent streams per connection, and a token
// check that rejects callers before their request message is read. gRPC
// runs that check on the connection goroutine, so the token source must be
// fast. RPCs check the token again.
func (s *Server) GRPCOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.MaxRecvMsgSize(MaxRecvMsgSize),
		grpc.MaxConcurrentStreams(maxStreams),
		grpc.InTapHandle(func(ctx context.Context, info *tap.Info) (context.Context, error) {
			return ctx, s.checkMD(ctx, info.Header)
		}),
	}
}
