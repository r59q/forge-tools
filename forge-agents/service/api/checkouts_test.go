package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-agents/library/workspace"
	checkoutspb "github.com/r59q/forge-tools/forge-checkouts/service/checkoutspb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const checkoutID = "0123456789abcdef0123456789abcdef"

// fakeCheckouts answers CreateCheckout and RemoveCheckout and records the
// requests and their authorization.
type fakeCheckouts struct {
	checkoutspb.CheckoutsServiceClient

	checkout      *checkoutspb.Checkout
	err           error
	created       *checkoutspb.CreateCheckoutRequest
	removed       *checkoutspb.RemoveCheckoutRequest
	authorization []string
}

func (f *fakeCheckouts) CreateCheckout(
	ctx context.Context, req *checkoutspb.CreateCheckoutRequest, _ ...grpc.CallOption,
) (*checkoutspb.CreateCheckoutResponse, error) {
	f.created = req
	md, _ := metadata.FromOutgoingContext(ctx)
	f.authorization = md.Get("authorization")
	if f.err != nil {
		return nil, f.err
	}
	return &checkoutspb.CreateCheckoutResponse{Checkout: f.checkout}, nil
}

func (f *fakeCheckouts) RemoveCheckout(
	ctx context.Context, req *checkoutspb.RemoveCheckoutRequest, _ ...grpc.CallOption,
) (*checkoutspb.RemoveCheckoutResponse, error) {
	f.removed = req
	md, _ := metadata.FromOutgoingContext(ctx)
	f.authorization = md.Get("authorization")
	if f.err != nil {
		return nil, f.err
	}
	return &checkoutspb.RemoveCheckoutResponse{}, nil
}

func ready(location string) *checkoutspb.Checkout {
	return &checkoutspb.Checkout{Id: checkoutID, Location: location, State: "ready", Reference: "main"}
}

func TestRemoteCreate(t *testing.T) {
	fake := &fakeCheckouts{checkout: ready("/data/copies/" + checkoutID)}
	remote := NewRemote(fake, forgeauth.StaticToken("checkouts-token"), Root{Local: "/checkouts", Reported: "/data/copies"})
	repository := workspace.Repository{Source: "github", Path: "o/r"}
	c, err := remote.Create(t.Context(), repository, "")
	if err != nil {
		t.Fatal(err)
	}
	if c != (workspace.Checkout{ID: checkoutID, Path: "/checkouts/" + checkoutID, Reference: "main"}) {
		t.Errorf("checkout %+v", c)
	}
	req := fake.created
	if req.GetMode() != "worktree" || req.GetReference() != "" || req.GetRepository().GetSource() != "github" || req.GetRepository().GetPath() != "o/r" {
		t.Errorf("request %v", req)
	}
	if len(fake.authorization) != 1 || fake.authorization[0] != "Bearer checkouts-token" {
		t.Errorf("authorization %q", fake.authorization)
	}
}

// TestRemoteCreateUnusable rejects checkouts this service cannot reach.
func TestRemoteCreateUnusable(t *testing.T) {
	root := Root{Local: "/checkouts"}
	for name, checkout := range map[string]*checkoutspb.Checkout{
		"outside the root": ready("/elsewhere/" + checkoutID),
		"nested":           ready("/checkouts/" + checkoutID + "/x"),
		"not ready":        {Id: checkoutID, Location: "/checkouts/" + checkoutID, State: "creating"},
		"bad id":           {Id: "../x", Location: "/checkouts/../x", State: "ready"},
		"upper case id":    {Id: "0123456789ABCDEF0123456789ABCDEF", Location: "/checkouts/0123456789ABCDEF0123456789ABCDEF", State: "ready"},
		"missing":          nil,
	} {
		remote := NewRemote(&fakeCheckouts{checkout: checkout}, forgeauth.StaticToken("t"), root)
		if _, err := remote.Create(t.Context(), workspace.Repository{LocalPath: "/repo"}, ""); !errors.Is(err, ErrCheckoutsUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRemoteErrors(t *testing.T) {
	for c, want := range map[codes.Code]error{
		codes.NotFound:           ErrCheckoutNotFound,
		codes.InvalidArgument:    workspace.ErrInvalid,
		codes.ResourceExhausted:  ErrCheckoutTooLarge,
		codes.FailedPrecondition: ErrCheckoutConflict,
		codes.Internal:           ErrCheckoutsUnavailable,
		codes.Unauthenticated:    ErrCheckoutsUnavailable,
	} {
		fake := &fakeCheckouts{err: status.Error(c, "secret detail")}
		remote := NewRemote(fake, forgeauth.StaticToken("t"), Root{Local: "/checkouts"})
		_, err := remote.Create(t.Context(), workspace.Repository{LocalPath: "/repo"}, "")
		if !errors.Is(err, want) {
			t.Errorf("%v: %v, want %v", c, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "secret detail") {
			t.Errorf("%v: error echoes forge-checkouts: %v", c, err)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	remote := NewRemote(&fakeCheckouts{err: status.Error(codes.Canceled, "x")}, forgeauth.StaticToken("t"), Root{Local: "/checkouts"})
	if _, err := remote.Create(ctx, workspace.Repository{LocalPath: "/repo"}, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestRemoteRemove(t *testing.T) {
	fake := &fakeCheckouts{}
	remote := NewRemote(fake, forgeauth.StaticToken("t"), Root{Local: "/checkouts"})
	if err := remote.Remove(t.Context(), checkoutID); err != nil {
		t.Fatal(err)
	}
	if fake.removed.GetId() != checkoutID || fake.removed.GetDiscard() {
		t.Errorf("request %v", fake.removed)
	}

	fake.err = status.Error(codes.NotFound, "gone")
	if err := remote.Remove(t.Context(), checkoutID); err != nil {
		t.Errorf("already removed: %v", err)
	}
	fake.err = status.Error(codes.FailedPrecondition, "dirty")
	if err := remote.Remove(t.Context(), checkoutID); !errors.Is(err, ErrCheckoutConflict) {
		t.Errorf("dirty: %v", err)
	}

	fake.removed = nil
	if err := remote.Remove(t.Context(), "../x"); !errors.Is(err, workspace.ErrInvalid) || fake.removed != nil {
		t.Errorf("bad id: %v, sent %v", err, fake.removed)
	}
}

func TestRootValid(t *testing.T) {
	for root, want := range map[Root]bool{
		{Local: "/checkouts"}:                    true,
		{Local: "/checkouts", Reported: "/data"}: true,
		{}:                                       false,
		{Local: "checkouts"}:                     false,
		{Local: "/checkouts/"}:                   false,
		{Local: "/checkouts", Reported: "data/x"}: false,
	} {
		if root.Valid() != want {
			t.Errorf("%+v: %v", root, !want)
		}
	}
}
