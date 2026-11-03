package api

import (
	"context"
	"errors"
	"sync"

	"github.com/r59q/forge-tools/forge-code/library/code"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc/codes"
)

// limiter counts in-flight requests per source.
type limiter struct {
	mu  sync.Mutex
	max int
	n   map[string]int
}

// acquire takes a slot for source, or returns false if all are in use.
// An empty source takes no slot, since the library rejects it.
func (l *limiter) acquire(source string) (release func(), ok bool) {
	if source == "" {
		return func() {}, true
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n[source] >= l.max {
		return nil, false
	}

	l.n[source]++
	var once sync.Once

	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.n[source]--; l.n[source] == 0 {
				delete(l.n, source)
			}
		})
	}, true
}

// begin takes an in-flight slot for source and returns the current
// client. Callers must authorize first and call release when done.
func (s *Server) begin(ctx context.Context, source string) (client *code.Client, release func(), err error) {
	release, ok := s.inFlight.acquire(source)
	if !ok {
		return nil, nil, servicekit.Status(codes.ResourceExhausted)
	}

	client, err = s.sources(ctx)
	if err != nil {
		release()
		if ctx.Err() != nil {
			return nil, nil, rpcError(ctx.Err())
		}

		return nil, nil, servicekit.Status(codes.Unavailable)
	}

	return client, release, nil
}

// rpc does the work of a unary RPC with a client.
type rpc[T any] func(context.Context, *code.Client) (*T, error)

// call runs a unary RPC on source within the request timeout.
func call[T any](s *Server, ctx context.Context, source string, fn rpc[T]) (*T, error) {
	return run(s, ctx, source, false, fn)
}

// write is call for an RPC that writes, where a forge 400 or 422 means a
// rejected argument.
func write[T any](s *Server, ctx context.Context, source string, fn rpc[T]) (*T, error) {
	return run(s, ctx, source, true, fn)
}

// run runs fn on source within the request timeout. writes maps a forge
// 400 or 422 to InvalidArgument.
func run[T any](s *Server, ctx context.Context, source string, writes bool, fn rpc[T]) (*T, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}

	client, release, err := s.begin(ctx, source)
	if err != nil {
		return nil, err
	}

	defer release()
	result, err := fn(ctx, client)
	if err != nil {
		return nil, servicekit.Status(codeOf(err, writes))
	}

	return result, nil
}

// rpcError maps a read error to a bare status.
func rpcError(err error) error {
	return servicekit.Status(codeOf(err, false))
}

// codeOf maps context errors, then library errors, then forge errors, to
// a status code. writes makes a forge 400 or 422 InvalidArgument; for
// reads it is Unavailable.
func codeOf(err error, writes bool) codes.Code {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return servicekit.ForgeCode(err)
	case errors.Is(err, code.ErrInvalid), errors.Is(err, code.ErrUnknownSource):
		return codes.InvalidArgument
	case errors.Is(err, code.ErrStale):
		return codes.Aborted
	case errors.Is(err, code.ErrAmbiguous), errors.Is(err, code.ErrNotFile), errors.Is(err, code.ErrRejected):
		return codes.FailedPrecondition
	case errors.Is(err, code.ErrTooLarge):
		return codes.OutOfRange
	case errors.Is(err, code.ErrUnsupported):
		return codes.Unimplemented
	case errors.Is(err, code.ErrRevisionNotFound), errors.Is(err, code.ErrPathNotFound):
		return codes.NotFound
	}

	c := servicekit.ForgeCode(err)
	if c == codes.InvalidArgument && !writes {
		return codes.Unavailable
	}

	return c
}
