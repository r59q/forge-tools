package broker

import (
	"context"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// CreateForge validates a provider and API endpoint without contacting the forge.
// It generates an ID; credentials are supplied later through CreateConnection.
func (m *Manager) CreateForge(ctx context.Context, kind forgeauth.Kind, baseURL, name string) (Forge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Forge{}, err
	}
	if strings.TrimSpace(baseURL) == "" {
		return Forge{}, ErrInvalid
	}
	// Open checks the endpoint with a placeholder credential and makes no request.
	session, err := m.registry.Open(ctx, forgeauth.Connection{Kind: kind, BaseURL: baseURL}, forgeauth.StaticToken("endpoint-validation"))
	if err != nil {
		return Forge{}, safeError(err, ErrInvalid)
	}
	f := Forge{ID: randomID(), Kind: kind, BaseURL: session.BaseURL, Name: name}
	if err := m.store.CreateForge(ctx, f); err != nil {
		return Forge{}, safeError(err, ErrStorage)
	}
	return f, nil
}

// ListForges returns endpoint metadata ordered by ID.
func (m *Manager) ListForges(ctx context.Context) ([]Forge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result, err := m.store.ListForges(ctx)
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
