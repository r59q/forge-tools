// Package api exposes the pipeprint library over gRPC and generated REST
// routes.
package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-pipeprint/library/compile"
	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
	pb "github.com/r59q/forge-tools/forge-pipeprint/service/pipeprintpb"
	"google.golang.org/grpc/codes"
)

// maxWidth caps the render width.
const maxWidth = 1000

// ErrInvalid means New was given no token or a nonpositive timeout.
var ErrInvalid = errors.New("api: invalid configuration")

// errBadRequest is returned for invalid request fields.
var errBadRequest = errors.New("api: invalid request")

// Server authenticates every RPC, including direct grpc-gateway dispatch.
// It never logs requests or responses. Convert, Graph and Render return
// only a status code on failure; Check returns problems as data.
type Server struct {
	pb.UnimplementedPipeprintServiceServer
	token   forgeauth.Credentials
	timeout time.Duration
}

// New returns a server. Callers must present the token from token as
// Authorization: Bearer. Each RPC runs for at most timeout.
func New(token forgeauth.Credentials, timeout time.Duration) (*Server, error) {
	if token == nil || timeout <= 0 {
		return nil, ErrInvalid
	}

	return &Server{token: token, timeout: timeout}, nil
}

// REST returns the generated routes behind servicekit.Secure. Only
// Authorization is forwarded, and errors never echo input.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	mux := servicekit.NewMux(false)
	if err := pb.RegisterPipeprintServiceHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}

	return servicekit.Secure(mux), nil
}

// call authorizes and runs fn within the timeout.
func call[T any](s *Server, ctx context.Context, fn func() (*T, error)) (*T, error) {
	return servicekit.Call(ctx, servicekit.RPC[struct{}]{
		Timeout: s.timeout,
		Token:   s.token,
		Client:  func(context.Context) (struct{}, error) { return struct{}{}, nil },
		Code:    code,
	}, func(context.Context, struct{}) (*T, error) { return fn() })
}

// code maps context, input and format errors to a status code.
func code(err error) codes.Code {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return servicekit.ForgeCode(err)
	case errors.Is(err, pipeprint.ErrInvalid), errors.Is(err, compile.ErrFormat), errors.Is(err, errBadRequest):
		return codes.InvalidArgument
	}

	return codes.Internal
}

// source reads the request's content and format.
type source interface {
	GetContent() string
	GetFrom() string
}

// from returns the request's format, checking it is known when set.
func from(r source) (compile.Format, error) {
	format := compile.Format(r.GetFrom())
	if format != "" && !format.Known() {
		return "", compile.ErrFormat
	}

	return format, nil
}

// read parses the request's content.
func read(r source) (*pipeprint.Pipeline, error) {
	format, err := from(r)
	if err != nil {
		return nil, err
	}

	p, _, err := compile.Read(format, []byte(r.GetContent()))

	return p, err
}

// ListFormats lists the supported formats.
func (s *Server) ListFormats(ctx context.Context, _ *pb.ListFormatsRequest) (*pb.ListFormatsResponse, error) {
	return call(s, ctx, func() (*pb.ListFormatsResponse, error) {
		out := &pb.ListFormatsResponse{}
		for _, info := range compile.Formats() {
			out.Formats = append(out.Formats, &pb.Format{Format: string(info.Format), Title: info.Title, Path: info.Path})
		}

		return out, nil
	})
}

// Convert compiles content between formats.
func (s *Server) Convert(ctx context.Context, r *pb.ConvertRequest) (*pb.ConvertResponse, error) {
	return call(s, ctx, func() (*pb.ConvertResponse, error) {
		format, err := from(r)
		if err != nil {
			return nil, err
		}

		out, warnings, err := compile.Convert(format, compile.Format(r.GetTo()), []byte(r.GetContent()))
		if err != nil {
			return nil, err
		}

		resp := &pb.ConvertResponse{Content: string(out)}
		for _, w := range warnings {
			resp.Warnings = append(resp.Warnings, &pb.Warning{Path: w.Path, Message: w.Message})
		}

		return resp, nil
	})
}

// Check validates content and lists its problems.
func (s *Server) Check(ctx context.Context, r *pb.CheckRequest) (*pb.CheckResponse, error) {
	return call(s, ctx, func() (*pb.CheckResponse, error) {
		format, err := from(r)
		if err != nil {
			return nil, err
		}

		detected, problems := compile.Check(format, []byte(r.GetContent()))
		resp := &pb.CheckResponse{Valid: len(problems) == 0, Format: string(detected)}
		for _, p := range problems {
			resp.Problems = append(resp.Problems, &pb.Problem{Path: p.Path, Message: p.Message})
		}

		return resp, nil
	})
}

// count converts a count to int32, clamped to its range.
func count(n int) int32 {
	return int32(max(0, min(n, math.MaxInt32)))
}
