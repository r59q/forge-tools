package broker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// Manager validates credentials and authorizes clients. Use NewManager, and use
// one Manager per Store. Operations are serialized to prevent checks, rotations
// and deletes from overwriting each other. Do not mutate Store concurrently with
// Manager operations. Registry providers must honor context deadlines.
type Manager struct {
	mu       sync.Mutex
	store    Store
	sealer   Sealer
	registry *forgeauth.Registry
}

// NewManager uses the supplied store, sealer and explicitly registered providers.
// All three are required. Callers own the encryption key and store lifecycle.
func NewManager(store Store, sealer Sealer, registry *forgeauth.Registry) (*Manager, error) {
	if store == nil || sealer == nil || registry == nil {
		return nil, ErrInvalid
	}
	return &Manager{store: store, sealer: sealer, registry: registry}, nil
}

// Format keeps dependencies and their credential material out of formatted output.
func (*Manager) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte("broker.Manager")) }

// CreateConnection validates token with Whoami, then atomically stores metadata
// and sealed credentials. Invalid credentials leave no record. The result never
// contains the token. Supply a context deadline to bound forge requests.
func (m *Manager) CreateConnection(ctx context.Context, forgeID ID, name string, token forgeauth.StaticToken) (Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	forge, err := m.store.GetForge(ctx, forgeID)
	if err != nil {
		return Connection{}, safeError(err, ErrStorage)
	}
	identity, err := m.whoami(ctx, forge, token)
	if err != nil {
		return Connection{}, err
	}
	id := randomID()
	sealed, err := m.seal(token)
	if err != nil {
		return Connection{}, err
	}
	now := time.Now().UTC()
	conn := Connection{
		ID: id, ForgeID: forgeID, Name: name, Identity: identity,
		Status: StatusOK, CheckedAt: now, CreatedAt: now,
	}
	if err := m.store.CreateConnection(ctx, conn, sealed); err != nil {
		return Connection{}, safeError(err, ErrStorage)
	}
	return conn, nil
}

// TestConnection runs Whoami with the stored credential and updates CheckedAt and
// Status. Rejected credentials become revoked; other forge failures become invalid.
// On a forge failure it returns updated metadata and a sanitized error. A later
// successful check restores StatusOK and refreshes Identity.
func (m *Manager) TestConnection(ctx context.Context, id ID) (Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	conn, forge, err := m.connection(ctx, id)
	if err != nil {
		return Connection{}, err
	}
	token, err := m.credential(ctx, id)
	if err != nil {
		return Connection{}, err
	}
	identity, checkErr := m.whoami(ctx, forge, token)
	conn.CheckedAt = time.Now().UTC()
	conn.Status = StatusOK
	if checkErr == nil {
		conn.Identity = identity
	} else if errors.Is(checkErr, forgehttp.ErrUnauthorized) || errors.Is(checkErr, forgehttp.ErrForbidden) {
		conn.Status = StatusRevoked
	} else {
		conn.Status = StatusInvalid
	}
	if err := m.store.UpdateConnection(ctx, conn, nil); err != nil {
		return Connection{}, safeError(err, ErrStorage)
	}
	return conn, checkErr
}

// RotateCredential validates a replacement token before changing the stored
// credential. A failed rotation preserves all old metadata and ciphertext. A
// successful rotation refreshes Identity, CheckedAt and Status, keeping CreatedAt.
func (m *Manager) RotateCredential(ctx context.Context, id ID, token forgeauth.StaticToken) (Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	conn, forge, err := m.connection(ctx, id)
	if err != nil {
		return Connection{}, err
	}
	identity, err := m.whoami(ctx, forge, token)
	if err != nil {
		return Connection{}, err
	}
	sealed, err := m.seal(token)
	if err != nil {
		return Connection{}, err
	}
	conn.Identity = identity
	conn.Status = StatusOK
	conn.CheckedAt = time.Now().UTC()
	if err := m.store.UpdateConnection(ctx, conn, sealed); err != nil {
		return Connection{}, safeError(err, ErrStorage)
	}
	return conn, nil
}

// DeleteConnection removes metadata, ciphertext and all client grants for id.
func (m *Manager) DeleteConnection(ctx context.Context, id ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return safeError(m.store.DeleteConnection(ctx, id), ErrStorage)
}

// CreateClient creates a scoped client and returns its random 256-bit token once.
// Only a SHA-256 hash is stored. This one-time bootstrap token is the exception to
// write-only client credentials; it cannot be retrieved later. The token type
// redacts fmt, slog and JSON output. The returned Client omits TokenHash.
func (m *Manager) CreateClient(ctx context.Context, name string, connections []ID, canIssueTokens bool) (Client, forgeauth.StaticToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Client{}, "", err
	}
	id := randomID()
	token := base64.RawURLEncoding.EncodeToString(randomBytes(32))
	client := Client{
		ID: id, Name: name, TokenHash: sha256.Sum256([]byte(token)),
		Connections: slices.Clone(connections), CanIssueTokens: canIssueTokens,
	}
	if err := m.store.CreateClient(ctx, client); err != nil {
		return Client{}, "", safeError(err, ErrStorage)
	}
	return clientMetadata(client), forgeauth.StaticToken(token), nil
}

// DeleteClient immediately invalidates the client's token.
func (m *Manager) DeleteClient(ctx context.Context, id ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return safeError(m.store.DeleteClient(ctx, id), ErrStorage)
}

// VerifyClient authenticates token and checks its scope and the connection's
// StatusOK. It does not require CanIssueTokens and does not decrypt credentials.
// The returned metadata omits TokenHash. Unknown tokens fail with ErrUnauthorized;
// out-of-scope requests fail with ErrForbidden.
func (m *Manager) VerifyClient(ctx context.Context, token forgeauth.StaticToken, connectionID ID) (Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	client, err := m.verifyClient(ctx, token, connectionID)
	if err != nil {
		return Client{}, err
	}
	return clientMetadata(client), nil
}

// IssuedToken is a forge credential issued to an authorized client. Token is
// explicitly accessible as a string conversion, but redacts formatted and JSON
// output. Services must deliberately encode it when sending an authorized reply.
type IssuedToken struct {
	// Kind selects the forge's authentication format.
	Kind forgeauth.Kind
	// BaseURL is the forge API endpoint for Token.
	BaseURL string
	// Token is the decrypted forge credential, redacted from formatted output.
	Token forgeauth.StaticToken
}

// IssueToken returns the forge token only if the client is scoped to a healthy
// connection and has CanIssueTokens. It does not recheck the forge; callers can
// use TestConnection to refresh the last known credential status.
func (m *Manager) IssueToken(ctx context.Context, clientToken forgeauth.StaticToken, connectionID ID) (IssuedToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	client, err := m.verifyClient(ctx, clientToken, connectionID)
	if err != nil {
		return IssuedToken{}, err
	}
	if !client.CanIssueTokens {
		return IssuedToken{}, ErrForbidden
	}
	_, forge, err := m.connection(ctx, connectionID)
	if err != nil {
		return IssuedToken{}, err
	}
	token, err := m.credential(ctx, connectionID)
	if err != nil {
		return IssuedToken{}, err
	}
	return IssuedToken{Kind: forge.Kind, BaseURL: forge.BaseURL, Token: token}, nil
}

func (m *Manager) verifyClient(ctx context.Context, token forgeauth.StaticToken, connectionID ID) (Client, error) {
	clients, err := m.store.ListClients(ctx)
	if err != nil {
		return Client{}, safeError(err, ErrStorage)
	}
	hash := sha256.Sum256([]byte(token))
	var matched Client
	found := 0
	// Compare fixed-size hashes for every client, without an early return on match.
	for _, client := range clients {
		equal := subtle.ConstantTimeCompare(hash[:], client.TokenHash[:])
		if equal == 1 {
			matched = client
		}
		found |= equal
	}
	if found != 1 || token == "" {
		return Client{}, ErrUnauthorized
	}
	if !slices.Contains(matched.Connections, connectionID) {
		return Client{}, ErrForbidden
	}
	conn, err := m.store.GetConnection(ctx, connectionID)
	if err != nil {
		return Client{}, safeError(err, ErrStorage)
	}
	if conn.Status != StatusOK {
		return Client{}, ErrUnavailable
	}
	return matched, nil
}

func (m *Manager) connection(ctx context.Context, id ID) (Connection, Forge, error) {
	conn, err := m.store.GetConnection(ctx, id)
	if err != nil {
		return Connection{}, Forge{}, safeError(err, ErrStorage)
	}
	forge, err := m.store.GetForge(ctx, conn.ForgeID)
	if err != nil {
		return Connection{}, Forge{}, safeError(err, ErrStorage)
	}
	return conn, forge, nil
}

func (m *Manager) credential(ctx context.Context, id ID) (forgeauth.StaticToken, error) {
	sealed, err := m.store.GetCredential(ctx, id)
	if err != nil {
		return "", safeError(err, ErrStorage)
	}
	plaintext, err := m.sealer.Open(sealed)
	defer clear(plaintext)
	if err != nil {
		return "", ErrSealing
	}
	return forgeauth.StaticToken(plaintext), nil
}

func (m *Manager) seal(token forgeauth.StaticToken) ([]byte, error) {
	plaintext := []byte(token)
	defer clear(plaintext)
	sealed, err := m.sealer.Seal(plaintext)
	if err != nil {
		return nil, ErrSealing
	}
	return sealed, nil
}

func (m *Manager) whoami(ctx context.Context, forge Forge, token forgeauth.StaticToken) (forgeauth.Identity, error) {
	session, err := m.registry.Open(ctx, forgeauth.Connection{Kind: forge.Kind, BaseURL: forge.BaseURL}, token)
	if err != nil {
		return forgeauth.Identity{}, safeError(err, ErrInvalidCredential)
	}
	identity, err := m.registry.Whoami(ctx, session)
	if err != nil {
		return forgeauth.Identity{}, safeError(err, ErrInvalidCredential)
	}
	// A forge may echo a credential even in a successful response.
	redact := func(value string) string {
		if token == "" {
			return value
		}
		return strings.ReplaceAll(value, string(token), "[REDACTED]")
	}
	identity.ID = redact(identity.ID)
	identity.Login = redact(identity.Login)
	identity.Name = redact(identity.Name)
	identity.Email = redact(identity.Email)
	return identity, nil
}

func clientMetadata(client Client) Client {
	client.TokenHash = [32]byte{}
	return cloneClient(client)
}

func randomID() ID {
	return ID(base64.RawURLEncoding.EncodeToString(randomBytes(16)))
}

func randomBytes(size int) []byte {
	data := make([]byte, size)
	// crypto/rand.Read always fills data and terminates on entropy failure.
	_, _ = rand.Read(data)
	return data
}

// safeError discards untrusted messages (including forge bodies and transport,
// storage or provider errors), retaining only fixed, recognized error classes.
func safeError(err, fallback error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{
		context.Canceled, context.DeadlineExceeded,
		ErrNotFound, ErrConflict, ErrInvalid,
		forgeauth.ErrUnknownKind, forgeauth.ErrNoBaseURL, forgeauth.ErrNoToken,
		forgehttp.ErrUnauthorized, forgehttp.ErrForbidden, forgehttp.ErrNotFound,
		forgehttp.ErrConflict, forgehttp.ErrRateLimited,
	} {
		if errors.Is(err, known) {
			return fmt.Errorf("%w: %w", fallback, known)
		}
	}
	return fallback
}
