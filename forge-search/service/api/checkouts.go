package api

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	checkoutspb "github.com/r59q/forge-tools/forge-checkouts/service/checkoutspb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ErrNotFound is returned by Checkouts for a checkout that does not exist.
var ErrNotFound = errors.New("api: checkout not found")

// Checkout is what the server needs to know of a forge-checkouts checkout.
type Checkout struct {
	ID, Revision, State, Location string
}

// Checkouts looks up checkouts by ID. Errors other than ErrNotFound and
// context errors are reported as Unavailable.
type Checkouts interface {
	Checkout(ctx context.Context, id string) (Checkout, error)
}

// Root is the directory that holds checkout directories, each named by its
// checkout ID, such as the copies directory of forge-checkouts.
type Root struct {
	// Local is where this service reads it: an absolute, clean path.
	Local string

	// Reported is the same directory as forge-checkouts names it in
	// checkout locations, when it is mounted elsewhere. Empty uses Local.
	Reported string
}

func (r Root) valid() bool {
	return absolute(r.Local) && (r.Reported == "" || absolute(r.Reported))
}

func absolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

// open opens a checkout's directory. Its location must be the directory
// named by its ID below the reported root, and the directory is opened
// below the local root, so no other directory is read.
func (r Root) open(c Checkout) (*os.Root, error) {
	reported := r.Reported
	if reported == "" {
		reported = r.Local
	}
	if !validCheckoutID(c.ID) || c.Location != filepath.Join(reported, c.ID) {
		return nil, errNotReadable
	}

	parent, err := os.OpenRoot(r.Local)
	if err != nil {
		return nil, errNotReadable
	}
	defer func() { _ = parent.Close() }()

	root, err := parent.OpenRoot(c.ID)
	if err != nil {
		return nil, errNotReadable
	}
	return root, nil
}

// validCheckoutID reports whether id has the form of a forge-checkouts ID:
// 32 lowercase hex digits.
func validCheckoutID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && id == strings.ToLower(id)
}

// readable reports whether a checkout's files can be read: it is ready, or
// retained after a failed removal.
func readable(state string) bool {
	return state == "ready" || state == "retained"
}

// openCheckout looks up a checkout and opens its directory. The caller
// closes the root.
func (s *Server) openCheckout(ctx context.Context, id string) (Checkout, *os.Root, error) {
	if !validCheckoutID(id) {
		return Checkout{}, nil, errBadRequest
	}

	c, err := s.config.Checkouts.Checkout(ctx, id)
	switch {
	case ctx.Err() != nil:
		return Checkout{}, nil, ctx.Err()
	case errors.Is(err, ErrNotFound):
		return Checkout{}, nil, ErrNotFound
	case err != nil || c.ID != id:
		return Checkout{}, nil, errUnavailable
	case !readable(c.State):
		return Checkout{}, nil, errNotReady
	}

	root, err := s.config.Root.open(c)
	if err != nil {
		return Checkout{}, nil, err
	}
	return c, root, nil
}

// Remote looks up checkouts with the forge-checkouts gRPC API.
type Remote struct {
	client checkoutspb.CheckoutsServiceClient
	token  forgeauth.Credentials
}

// NewRemote returns checkouts looked up through client, authenticated with
// the token from token. The token is read on every call, so it can rotate.
func NewRemote(client checkoutspb.CheckoutsServiceClient, token forgeauth.Credentials) *Remote {
	return &Remote{client: client, token: token}
}

// Checkout returns the checkout with id. Errors never hold the token or the
// response of forge-checkouts.
func (r *Remote) Checkout(ctx context.Context, id string) (Checkout, error) {
	token, err := r.token.Token(ctx)
	if err != nil {
		return Checkout{}, errUnavailable
	}

	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
	response, err := r.client.GetCheckout(ctx, &checkoutspb.GetCheckoutRequest{Id: id})
	switch {
	case ctx.Err() != nil:
		return Checkout{}, ctx.Err()
	case status.Code(err) == codes.NotFound:
		return Checkout{}, ErrNotFound
	case err != nil:
		return Checkout{}, errUnavailable
	}

	c := response.GetCheckout()
	return Checkout{ID: c.GetId(), Revision: c.GetRevision(), State: c.GetState(), Location: c.GetLocation()}, nil
}
