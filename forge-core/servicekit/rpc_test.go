package servicekit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type staticToken string

func (t staticToken) Token(context.Context) (string, error) { return string(t), nil }

func withBearer(ctx context.Context, token string) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
}

func TestAuthorize(t *testing.T) {
	tests := []struct {
		name  string
		ctx   context.Context
		token staticToken
		ok    bool
	}{
		{"match", withBearer(t.Context(), "s3cret"), "s3cret", true},
		{"mismatch", withBearer(t.Context(), "guess"), "s3cret", false},
		{"missing", t.Context(), "s3cret", false},
		{"no expected token", withBearer(t.Context(), ""), "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Authorize(tt.ctx, tt.token)
			if tt.ok != (err == nil) {
				t.Fatalf("got %v", err)
			}

			if err != nil && status.Code(err) != codes.Unauthenticated {
				t.Errorf("code = %v", status.Code(err))
			}
		})
	}
}

type client struct{ name string }

func testRPC(clientErr error) RPC[*client] {
	return RPC[*client]{
		Timeout: time.Second,
		Token:   staticToken("s3cret"),
		Client: func(context.Context) (*client, error) {
			return &client{name: "c"}, clientErr
		},
		Code: ForgeCode,
	}
}

func TestCall(t *testing.T) {
	ctx := withBearer(t.Context(), "s3cret")

	got, err := Call(ctx, testRPC(nil), func(_ context.Context, c *client) (*string, error) {
		return &c.name, nil
	})
	if err != nil || *got != "c" {
		t.Fatalf("got %v, %v", got, err)
	}

	_, err = Call(ctx, testRPC(nil), func(context.Context, *client) (*string, error) {
		return nil, fmt.Errorf("upstream said: %w", forgehttp.ErrNotFound)
	})
	if status.Code(err) != codes.NotFound || status.Convert(err).Message() != "NotFound" {
		t.Errorf("got %v", err)
	}
}

func TestCallRejectsBeforeClient(t *testing.T) {
	rpc := testRPC(nil)
	rpc.Client = func(context.Context) (*client, error) {
		t.Fatal("client requested without authorization")

		return nil, nil
	}

	_, err := Call(t.Context(), rpc, func(context.Context, *client) (*string, error) { return nil, nil })
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("got %v", err)
	}
}

func TestCallClientError(t *testing.T) {
	ctx := withBearer(t.Context(), "s3cret")

	_, err := Call(ctx, testRPC(errors.New("connector down")), func(context.Context, *client) (*string, error) {
		return nil, nil
	})
	if status.Code(err) != codes.Unavailable {
		t.Errorf("got %v", err)
	}
}

func TestForgeCode(t *testing.T) {
	tests := []struct {
		err  error
		want codes.Code
	}{
		{context.Canceled, codes.Canceled},
		{context.DeadlineExceeded, codes.DeadlineExceeded},
		{forgehttp.ErrNotFound, codes.NotFound},
		{forgehttp.ErrForbidden, codes.PermissionDenied},
		{forgehttp.ErrRateLimited, codes.ResourceExhausted},
		{forgehttp.ErrConflict, codes.FailedPrecondition},
		{&forgehttp.Error{StatusCode: http.StatusUnprocessableEntity}, codes.InvalidArgument},
		{&forgehttp.Error{StatusCode: http.StatusBadGateway}, codes.Unavailable},
		{errors.New("other"), codes.Unavailable},
	}

	for _, tt := range tests {
		if got := ForgeCode(tt.err); got != tt.want {
			t.Errorf("ForgeCode(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}
