package servicekit

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// errUnauthenticated is the only error a failed authorization returns.
var errUnauthenticated = status.Error(codes.Unauthenticated, "authentication required")

// Authorize checks the RPC's Authorization: Bearer metadata against token
// in constant time.
func Authorize(ctx context.Context, token forgeauth.Credentials) error {
	md, _ := metadata.FromIncomingContext(ctx)
	h := http.Header{}
	for _, value := range md.Get("authorization") {
		h.Add("Authorization", value)
	}

	got, ok := forgeauth.AuthorizationToken(h, "Bearer")
	if !ok {
		return errUnauthenticated
	}

	expected, err := token.Token(ctx)
	if err != nil || expected == "" {
		return errUnauthenticated
	}

	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(expected))
	if subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
		return errUnauthenticated
	}

	return nil
}

// RPC holds what Call needs to run a unary RPC on a client of type C.
type RPC[C any] struct {
	// Timeout bounds each call.
	Timeout time.Duration
	// Token is the service token callers must present.
	Token forgeauth.Credentials
	// Client returns the client for one call. Its errors are Unavailable
	// unless the call's context ended.
	Client func(context.Context) (C, error)
	// Code maps fn's errors to a status code, such as with ForgeCode.
	Code func(error) codes.Code
}

// Call authorizes and runs fn within the timeout. Errors are bare status
// codes, so callers never see library or forge messages.
func Call[C, T any](ctx context.Context, rpc RPC[C], fn func(context.Context, C) (*T, error)) (*T, error) {
	ctx, cancel := context.WithTimeout(ctx, rpc.Timeout)
	defer cancel()

	if err := Authorize(ctx, rpc.Token); err != nil {
		return nil, err
	}

	client, err := rpc.Client(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, Status(rpc.Code(ctx.Err()))
		}

		return nil, Status(codes.Unavailable)
	}

	result, err := fn(ctx, client)
	if err != nil {
		return nil, Status(rpc.Code(err))
	}

	return result, nil
}

// Status returns an error with code and its name as the only message.
func Status(code codes.Code) error {
	return status.Error(code, code.String())
}

// ForgeCode maps context and forge errors to a status code. Anything else
// is Unavailable. A forge rejecting the service's own credentials is
// Unavailable, not Unauthenticated, so callers do not mistake it for a bad
// service token.
func ForgeCode(err error) codes.Code {
	var forgeErr *forgehttp.Error
	switch {
	case errors.Is(err, context.Canceled):
		return codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return codes.DeadlineExceeded
	case errors.Is(err, forgehttp.ErrNotFound):
		return codes.NotFound
	case errors.Is(err, forgehttp.ErrForbidden):
		return codes.PermissionDenied
	case errors.Is(err, forgehttp.ErrRateLimited):
		return codes.ResourceExhausted
	case errors.Is(err, forgehttp.ErrConflict):
		return codes.FailedPrecondition
	case errors.As(err, &forgeErr) && invalidRequest(forgeErr.StatusCode):
		return codes.InvalidArgument
	}

	return codes.Unavailable
}

// invalidRequest reports whether a forge status means it rejected the
// request's content.
func invalidRequest(statusCode int) bool {
	return statusCode == http.StatusBadRequest || statusCode == http.StatusUnprocessableEntity
}
