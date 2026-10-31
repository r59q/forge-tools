package forgeauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// Identity is the user a session is authenticated as.
type Identity struct {
	// ID is the forge's user ID.
	ID string
	// Login is the user name, such as "octocat".
	Login string
	// Name is the display name, if set.
	Name string
	// Email is the public or primary email, if visible.
	Email string
}

// Provider opens sessions for one kind of forge and knows its auth header.
type Provider interface {
	// Kind returns the forge kind.
	Kind() Kind
	// Open opens a session. See OpenSession for the rules.
	Open(ctx context.Context, conn Connection, creds Credentials) (*Session, error)
	// Whoami returns the user the session is authenticated as.
	Whoami(ctx context.Context, s *Session) (Identity, error)
	// SetToken sets the forge's auth header in h.
	SetToken(h http.Header, token string)
	// TokenFromHeader reads a token from an incoming request's headers, in
	// the formats the forge accepts.
	TokenFromHeader(h http.Header) (string, bool)
}

// ErrUnknownKind is returned by a Registry for kinds with no provider.
var ErrUnknownKind = errors.New("forgeauth: unknown forge kind")

// Registry maps forge kinds to providers. Register providers explicitly;
// nothing is registered by default. The zero value is empty and ready to
// use. A Registry is safe for concurrent use.
type Registry struct {
	mu        sync.RWMutex
	providers map[Kind]Provider
}

// Register adds p. It fails if p is nil or its kind is already registered.
func (r *Registry) Register(p Provider) error {
	if p == nil {
		return errors.New("forgeauth: nil provider")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[p.Kind()]; ok {
		return fmt.Errorf("forgeauth: provider for %q already registered", p.Kind())
	}
	if r.providers == nil {
		r.providers = map[Kind]Provider{}
	}
	r.providers[p.Kind()] = p
	return nil
}

// Provider returns the provider for kind, or an error wrapping
// ErrUnknownKind.
func (r *Registry) Provider(kind Kind) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[kind]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}
	return p, nil
}

// Kinds returns the registered kinds, sorted.
func (r *Registry) Kinds() []Kind {
	r.mu.RLock()
	defer r.mu.RUnlock()
	kinds := make([]Kind, 0, len(r.providers))
	for k := range r.providers {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	return kinds
}

// Open opens a session with the provider for conn.Kind.
func (r *Registry) Open(ctx context.Context, conn Connection, creds Credentials) (*Session, error) {
	p, err := r.Provider(conn.Kind)
	if err != nil {
		return nil, err
	}
	return p.Open(ctx, conn, creds)
}

// Whoami returns the identity for s with the provider for s.Kind.
func (r *Registry) Whoami(ctx context.Context, s *Session) (Identity, error) {
	p, err := r.Provider(s.Kind)
	if err != nil {
		return Identity{}, err
	}
	return p.Whoami(ctx, s)
}

// CheckSession returns an error if s is nil, has no client, or is not of
// the given kind. Providers call it at the start of Whoami.
func CheckSession(s *Session, kind Kind) error {
	switch {
	case s == nil || s.Client == nil:
		return errors.New("forgeauth: session not open")
	case s.Kind != kind:
		return fmt.Errorf("forgeauth: session kind %q does not match provider %q", s.Kind, kind)
	}
	return nil
}

// AuthorizationToken reads a token from the Authorization header, such as
// "Bearer abc". The scheme must match one of schemes, ignoring case. It
// returns false if the header is missing, repeated, or malformed.
func AuthorizationToken(h http.Header, schemes ...string) (string, bool) {
	vals := h.Values("Authorization")
	if len(vals) != 1 {
		return "", false
	}
	scheme, tok, ok := strings.Cut(strings.TrimSpace(vals[0]), " ")
	if !ok {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	if checkToken(tok) != nil {
		return "", false
	}
	for _, s := range schemes {
		if strings.EqualFold(scheme, s) {
			return tok, true
		}
	}
	return "", false
}

// HeaderToken reads a token from a header that holds only the token, such
// as GitLab's PRIVATE-TOKEN. It returns false if the header is missing,
// repeated, or malformed.
func HeaderToken(h http.Header, name string) (string, bool) {
	vals := h.Values(name)
	if len(vals) != 1 {
		return "", false
	}
	tok := strings.TrimSpace(vals[0])
	if checkToken(tok) != nil {
		return "", false
	}
	return tok, true
}
