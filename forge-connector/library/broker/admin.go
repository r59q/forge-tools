package broker

import (
	"context"
	"net/http"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// CreateForge validates a provider and API endpoint without contacting the forge.
// It generates an ID; credentials are supplied later through CreateConnection.
// The Git base URL is derived with forgeauth.DefaultGitURL when possible.
func (m *Manager) CreateForge(ctx context.Context, kind forgeauth.Kind, baseURL, name string) (Forge, error) {
	return m.RegisterForge(ctx, Forge{Kind: kind, BaseURL: baseURL, Name: name})
}

// RegisterForge is CreateForge with every Forge field. forge supplies Kind,
// BaseURL, Name and an optional GitBaseURL; the ID is generated. GitBaseURL
// follows the BaseURL rules. When empty, it is derived with
// forgeauth.DefaultGitURL, and left empty if that fails.
func (m *Manager) RegisterForge(ctx context.Context, forge Forge) (Forge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Forge{}, err
	}
	if strings.TrimSpace(forge.BaseURL) == "" {
		return Forge{}, ErrInvalid
	}
	// Open checks the endpoint with a placeholder credential and makes no request.
	session, err := m.registry.Open(ctx, forgeauth.Connection{Kind: forge.Kind, BaseURL: forge.BaseURL}, placeholder)
	if err != nil {
		return Forge{}, safeError(err, ErrInvalid)
	}
	gitBase := defaultGitURL(forge.Kind, session.BaseURL)
	if strings.TrimSpace(forge.GitBaseURL) != "" {
		if gitBase, err = checkGitURL(forge.GitBaseURL); err != nil {
			return Forge{}, err
		}
	}
	f := Forge{ID: randomID(), Kind: forge.Kind, BaseURL: session.BaseURL, Name: forge.Name, GitBaseURL: gitBase}
	if err := m.store.CreateForge(ctx, f); err != nil {
		return Forge{}, safeError(err, ErrStorage)
	}
	return f, nil
}

// placeholder validates endpoints without a real credential.
const placeholder = forgeauth.StaticToken("endpoint-validation")

// checkGitURL validates and normalizes a Git base URL.
func checkGitURL(raw string) (string, error) {
	remote, err := forgeauth.NewGitRemote(raw, placeholder, func(http.Header, string) {}, nil)
	if err != nil {
		return "", ErrInvalid
	}
	return remote.BaseURL, nil
}

// defaultGitURL derives a Git base URL, or "" when none can be derived.
func defaultGitURL(kind forgeauth.Kind, baseURL string) string {
	raw, err := forgeauth.DefaultGitURL(kind, baseURL)
	if err != nil {
		return ""
	}
	gitBase, err := checkGitURL(raw)
	if err != nil {
		return ""
	}
	return gitBase
}

// withGitDefault fills GitBaseURL for records stored without one.
func withGitDefault(f Forge) Forge {
	if f.GitBaseURL == "" {
		f.GitBaseURL = defaultGitURL(f.Kind, f.BaseURL)
	}
	return f
}

// ListForges returns endpoint metadata ordered by ID.
func (m *Manager) ListForges(ctx context.Context) ([]Forge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result, err := m.store.ListForges(ctx)
	for i := range result {
		result[i] = withGitDefault(result[i])
	}
	return result, safeError(err, ErrStorage)
}

// DeleteForge removes an endpoint only when it has no connections.
func (m *Manager) DeleteForge(ctx context.Context, id ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return safeError(m.store.DeleteForge(ctx, id), ErrStorage)
}

// ListConnections returns connection metadata ordered by ID, without credentials.
func (m *Manager) ListConnections(ctx context.Context) ([]Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result, err := m.store.ListConnections(ctx)
	return result, safeError(err, ErrStorage)
}

// GetConnection returns connection metadata without its credential.
func (m *Manager) GetConnection(ctx context.Context, id ID) (Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result, err := m.store.GetConnection(ctx, id)
	return result, safeError(err, ErrStorage)
}

// ListClients returns client metadata ordered by ID, without tokens or hashes.
func (m *Manager) ListClients(ctx context.Context) ([]Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	clients, err := m.store.ListClients(ctx)
	if err != nil {
		return nil, safeError(err, ErrStorage)
	}
	for i := range clients {
		clients[i] = clientMetadata(clients[i])
	}
	return clients, nil
}
