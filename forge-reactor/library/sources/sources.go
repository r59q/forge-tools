// Package sources manages webhook sources at runtime. It generates each
// source's secret and can register the webhook on the forge through
// sessions the caller supplies, so it never holds forge credentials.
package sources

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	"github.com/r59q/forge-tools/forge-reactor/library/webhook"
)

var (
	// ErrNotConfigured means registration needs a public URL and sessions.
	ErrNotConfigured = errors.New("sources: registration is not configured")
	// ErrRegistration means the forge refused or failed a hook request.
	ErrRegistration = errors.New("sources: the forge did not register the webhook")
)

// maxRepositories caps the repositories of one source.
const maxRepositories = 100

// Source is a managed webhook source, served at /webhooks/{id}.
type Source struct {
	// ID names the source.
	ID string `json:"id"`
	// Forge is forgeauth.GitHub or forgeauth.Forgejo.
	Forge forgeauth.Kind `json:"forge"`
	// Connection is the forge-connector connection ID its events carry.
	Connection string `json:"connection,omitempty"`
	// Repositories, if set, are the only owner/name paths accepted.
	Repositories []string `json:"repositories,omitempty"`
	// Hooks are the forge hook IDs by repository, for registered sources.
	Hooks map[string]int64 `json:"hooks,omitempty"`
	// Secret is the webhook secret. It never leaves the manager except
	// from Create and Rotate.
	Secret string `json:"secret"`
	// CreatedAt is when the source was created.
	CreatedAt time.Time `json:"createdAt"`
}

// Registered reports whether the source registered hooks on the forge.
func (s Source) Registered() bool { return len(s.Hooks) > 0 }

// Webhook returns the source as verification needs it.
func (s Source) Webhook() webhook.Source {
	return webhook.Source{
		ID:           s.ID,
		Forge:        s.Forge,
		Connection:   s.Connection,
		Secret:       []byte(s.Secret),
		Repositories: s.Repositories,
	}
}

// Request describes a new source.
type Request struct {
	// ID names the source.
	ID string
	// Forge is github or forgejo.
	Forge forgeauth.Kind
	// Connection is the forge-connector connection ID.
	Connection string
	// Repositories are owner/name paths.
	Repositories []string
	// Register creates the webhook on every repository.
	Register bool
}

// Sessions opens a forge session for a connection, such as through
// forge-connector.
type Sessions func(ctx context.Context, connection string) (*forgeauth.Session, error)

// Manager creates, rotates and deletes managed sources. Changes run one at
// a time.
type Manager struct {
	// Store keeps the sources.
	Store Store
	// Sessions opens forge sessions; nil disables registration.
	Sessions Sessions
	// PublicURL is the reactor's external base URL, such as
	// https://example.org/reactor; empty disables registration.
	PublicURL string
	// Reserved are IDs taken by configured sources.
	Reserved []string
	// Now returns the time; nil uses time.Now.
	Now func() time.Time

	lock chan struct{}
}

// mutex serializes changes. A channel lets waiting honour ctx.
func (m *Manager) mutex(ctx context.Context) (func(), error) {
	if m.lock == nil {
		return nil, reactor.ErrInvalid
	}
	select {
	case m.lock <- struct{}{}:
		return func() { <-m.lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// New checks m and prepares it for use.
func New(m *Manager) (*Manager, error) {
	if m == nil || m.Store == nil {
		return nil, reactor.ErrInvalid
	}
	if m.PublicURL != "" && !validBase(m.PublicURL) {
		return nil, reactor.ErrInvalid
	}
	m.lock = make(chan struct{}, 1)

	return m, nil
}

// validBase reports an absolute http or https URL without query or fragment.
func validBase(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}

	return u.Scheme == "http" || u.Scheme == "https"
}

// URL returns the payload URL of a source, or "" without a public URL.
func (m *Manager) URL(id string) string {
	if m.PublicURL == "" {
		return ""
	}

	return strings.TrimSuffix(m.PublicURL, "/") + "/webhooks/" + url.PathEscape(id)
}

// CanRegister reports whether registration is configured.
func (m *Manager) CanRegister() bool { return m.PublicURL != "" && m.Sessions != nil }

// Get returns one source.
func (m *Manager) Get(ctx context.Context, id string) (Source, error) {
	all, err := m.Store.Read(ctx)
	if err != nil {
		return Source{}, err
	}
	src, ok := all[id]
	if !ok {
		return Source{}, reactor.ErrNotFound
	}

	return src, nil
}

// List returns every source in ID order.
func (m *Manager) List(ctx context.Context) ([]Source, error) {
	all, err := m.Store.Read(ctx)
	if err != nil {
		return nil, err
	}

	list := make([]Source, 0, len(all))
	for _, src := range all {
		list = append(list, src)
	}
	slices.SortFunc(list, func(a, b Source) int { return cmp.Compare(a.ID, b.ID) })

	return list, nil
}

// newSecret returns 32 random bytes in hex.
func newSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)

	return hex.EncodeToString(b)
}
