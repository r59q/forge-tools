package api

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/r59q/forge-tools/forge-agents/library/workspace"
	checkoutspb "github.com/r59q/forge-tools/forge-checkouts/service/checkoutspb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Errors of forge-checkouts, each mapped to one status code. None holds a
// message of forge-checkouts.
var (
	// ErrCheckoutNotFound is a repository, reference or checkout that does
	// not exist.
	ErrCheckoutNotFound = errors.New("api: checkout not found")
	// ErrCheckoutConflict is a checkout forge-checkouts refuses in its
	// current state, such as removing one with uncommitted changes.
	ErrCheckoutConflict = errors.New("api: checkout conflicts")
	// ErrCheckoutTooLarge is a repository over a limit of forge-checkouts.
	ErrCheckoutTooLarge = errors.New("api: repository too large to check out")
	// ErrCheckoutsUnavailable is forge-checkouts failing or unreachable, or
	// a checkout this service cannot use.
	ErrCheckoutsUnavailable = errors.New("api: checkouts service unavailable")
)

// worktree is the forge-checkouts mode of agent workspaces: a linked
// worktree, cheap to create next to the repository's other checkouts.
const worktree = "worktree"

// Root is the forge-checkouts copies directory, which holds checkout
// directories named by their checkout ID.
type Root struct {
	// Local is where this service and its clients reach it: an absolute,
	// clean path.
	Local string

	// Reported is the same directory as forge-checkouts names it in
	// checkout locations, when it is mounted elsewhere. Empty uses Local.
	Reported string
}

// Valid reports whether both paths are absolute and clean.
func (r Root) Valid() bool {
	return absolute(r.Local) && (r.Reported == "" || absolute(r.Reported))
}

func absolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

// Remote creates and removes agent workspaces with the forge-checkouts gRPC
// API. It is the workspace.Checkouts of the service.
type Remote struct {
	client checkoutspb.CheckoutsServiceClient
	token  forgeauth.Credentials
	root   Root
}

var _ workspace.Checkouts = (*Remote)(nil)

// NewRemote returns checkouts made through client, authenticated with the
// token from token, which is read on every call so that it can rotate.
func NewRemote(client checkoutspb.CheckoutsServiceClient, token forgeauth.Credentials, root Root) *Remote {
	return &Remote{client: client, token: token, root: root}
}

// Create checks repository out as a new worktree at the current commit of
// reference; empty is the remote HEAD. The checkout must be ready and in
// the copies directory, named by its ID.
func (r *Remote) Create(ctx context.Context, repository workspace.Repository, reference string) (workspace.Checkout, error) {
	ctx, err := r.authorize(ctx)
	if err != nil {
		return workspace.Checkout{}, err
	}
	response, err := r.client.CreateCheckout(ctx, &checkoutspb.CreateCheckoutRequest{
		Repository: &checkoutspb.Repository{LocalPath: repository.LocalPath, Source: repository.Source, Path: repository.Path},
		Reference:  reference,
		Mode:       worktree,
	})
	if err != nil {
		return workspace.Checkout{}, remoteError(ctx, err)
	}
	c := response.GetCheckout()
	path, ok := r.root.path(c.GetId(), c.GetLocation())
	if !ok || c.GetState() != "ready" {
		return workspace.Checkout{}, ErrCheckoutsUnavailable
	}
	return workspace.Checkout{ID: c.GetId(), Path: path, Reference: c.GetReference()}, nil
}

// Remove removes a checkout without discarding changes, so forge-checkouts
// refuses one with uncommitted changes. A checkout already gone is removed.
func (r *Remote) Remove(ctx context.Context, id string) error {
	if !validCheckoutID(id) {
		return fmt.Errorf("%w: checkout ID", workspace.ErrInvalid)
	}
	ctx, err := r.authorize(ctx)
	if err != nil {
		return err
	}
	_, err = r.client.RemoveCheckout(ctx, &checkoutspb.RemoveCheckoutRequest{Id: id})
	if err = remoteError(ctx, err); err != nil && !errors.Is(err, ErrCheckoutNotFound) {
		return err
	}
	return nil
}

// path returns where this service reaches a checkout. Its location must be
// the directory named by its ID in the reported root.
func (r Root) path(id, location string) (string, bool) {
	reported := r.Reported
	if reported == "" {
		reported = r.Local
	}
	if !validCheckoutID(id) || location != filepath.Join(reported, id) {
		return "", false
	}
	return filepath.Join(r.Local, id), true
}

// validCheckoutID reports whether id has the form of a forge-checkouts ID:
// 32 lowercase hex digits.
func validCheckoutID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && id == strings.ToLower(id)
}

// authorize adds the token to the outgoing metadata of ctx.
func (r *Remote) authorize(ctx context.Context) (context.Context, error) {
	token, err := r.token.Token(ctx)
	if err != nil {
		return nil, ErrCheckoutsUnavailable
	}
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token)), nil
}

// remoteError maps an error of forge-checkouts to one that holds none of
// its message.
func remoteError(ctx context.Context, err error) error {
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	}
	switch status.Code(err) {
	case codes.NotFound:
		return ErrCheckoutNotFound
	case codes.InvalidArgument:
		return fmt.Errorf("%w: rejected by forge-checkouts", workspace.ErrInvalid)
	case codes.ResourceExhausted:
		return ErrCheckoutTooLarge
	case codes.FailedPrecondition:
		return ErrCheckoutConflict
	default:
		return ErrCheckoutsUnavailable
	}
}
