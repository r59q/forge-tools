package broker

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sync"
)

type connectionRecord struct {
	connection Connection
	sealed     []byte
}

// MemoryStore is an in-memory Store for tests and ephemeral deployments. The zero
// value is ready to use. Data is lost when the process exits.
type MemoryStore struct {
	mu          sync.Mutex
	forges      map[ID]Forge
	connections map[ID]connectionRecord
	clients     map[ID]Client
}

var _ Store = (*MemoryStore)(nil)

// Format keeps stored credential material out of formatted output.
func (*MemoryStore) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte("broker.MemoryStore")) }

// CreateForge implements Store.CreateForge.
func (s *MemoryStore) CreateForge(ctx context.Context, forge Forge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if forge.ID == "" || forge.Kind == "" || forge.BaseURL == "" {
		return ErrInvalid
	}
	if _, ok := s.forges[forge.ID]; ok {
		return ErrConflict
	}
	if s.forges == nil {
		s.forges = make(map[ID]Forge)
	}
	s.forges[forge.ID] = forge
	return nil
}

// GetForge implements Store.GetForge.
func (s *MemoryStore) GetForge(ctx context.Context, id ID) (Forge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Forge{}, err
	}
	f, ok := s.forges[id]
	if !ok {
		return Forge{}, ErrNotFound
	}
	return f, nil
}

// ListForges implements Store.ListForges.
func (s *MemoryStore) ListForges(ctx context.Context) ([]Forge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([]Forge, 0, len(s.forges))
	for _, id := range sortedIDs(s.forges) {
		result = append(result, s.forges[id])
	}
	return result, nil
}

// UpdateForge implements Store.UpdateForge.
func (s *MemoryStore) UpdateForge(ctx context.Context, forge Forge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	old, ok := s.forges[forge.ID]
	if !ok {
		return ErrNotFound
	}
	if forge.Kind == "" || forge.BaseURL == "" {
		return ErrInvalid
	}
	if old.Kind != forge.Kind || old.BaseURL != forge.BaseURL {
		for _, record := range s.connections {
			if record.connection.ForgeID == forge.ID {
				return ErrConflict
			}
		}
	}
	s.forges[forge.ID] = forge
	return nil
}

// DeleteForge implements Store.DeleteForge.
func (s *MemoryStore) DeleteForge(ctx context.Context, id ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := s.forges[id]; !ok {
		return ErrNotFound
	}
	for _, record := range s.connections {
		if record.connection.ForgeID == id {
			return ErrConflict
		}
	}
	delete(s.forges, id)
	return nil
}

// CreateConnection implements Store.CreateConnection.
func (s *MemoryStore) CreateConnection(ctx context.Context, conn Connection, sealed []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if conn.ID == "" || len(sealed) == 0 || !validStatus(conn.Status) {
		return ErrInvalid
	}
	if _, ok := s.connections[conn.ID]; ok {
		return ErrConflict
	}
	if _, ok := s.forges[conn.ForgeID]; !ok {
		return ErrNotFound
	}
	if s.connections == nil {
		s.connections = make(map[ID]connectionRecord)
	}
	s.connections[conn.ID] = connectionRecord{conn, bytes.Clone(sealed)}
	return nil
}

// GetConnection implements Store.GetConnection.
func (s *MemoryStore) GetConnection(ctx context.Context, id ID) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Connection{}, err
	}
	record, ok := s.connections[id]
	if !ok {
		return Connection{}, ErrNotFound
	}
	return record.connection, nil
}

// ListConnections implements Store.ListConnections.
func (s *MemoryStore) ListConnections(ctx context.Context) ([]Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([]Connection, 0, len(s.connections))
	for _, id := range sortedIDs(s.connections) {
		result = append(result, s.connections[id].connection)
	}
	return result, nil
}

// UpdateConnection implements Store.UpdateConnection.
func (s *MemoryStore) UpdateConnection(ctx context.Context, conn Connection, sealed []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	record, ok := s.connections[conn.ID]
	if !ok {
		return ErrNotFound
	}
	if conn.ForgeID != record.connection.ForgeID || !validStatus(conn.Status) || (sealed != nil && len(sealed) == 0) {
		return ErrInvalid
	}
	if sealed != nil {
		record.sealed = bytes.Clone(sealed)
	}
	record.connection = conn
	s.connections[conn.ID] = record
	return nil
}

// GetCredential implements Store.GetCredential.
func (s *MemoryStore) GetCredential(ctx context.Context, id ID) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, ok := s.connections[id]
	if !ok {
		return nil, ErrNotFound
	}
	return bytes.Clone(record.sealed), nil
}

// DeleteConnection implements Store.DeleteConnection.
func (s *MemoryStore) DeleteConnection(ctx context.Context, id ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := s.connections[id]; !ok {
		return ErrNotFound
	}
	delete(s.connections, id)
	for clientID, client := range s.clients {
		client.Connections = slices.DeleteFunc(client.Connections, func(candidate ID) bool { return candidate == id })
		s.clients[clientID] = client
	}
	return nil
}

// CreateClient implements Store.CreateClient.
func (s *MemoryStore) CreateClient(ctx context.Context, client Client) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := s.clients[client.ID]; ok {
		return ErrConflict
	}
	if err := s.checkClient(client); err != nil {
		return err
	}
	if s.clients == nil {
		s.clients = make(map[ID]Client)
	}
	s.clients[client.ID] = cloneClient(client)
	return nil
}

// GetClient implements Store.GetClient.
func (s *MemoryStore) GetClient(ctx context.Context, id ID) (Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Client{}, err
	}
	client, ok := s.clients[id]
	if !ok {
		return Client{}, ErrNotFound
	}
	return cloneClient(client), nil
}

// ListClients implements Store.ListClients.
func (s *MemoryStore) ListClients(ctx context.Context) ([]Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([]Client, 0, len(s.clients))
	for _, id := range sortedIDs(s.clients) {
		result = append(result, cloneClient(s.clients[id]))
	}
	return result, nil
}

// UpdateClient implements Store.UpdateClient.
func (s *MemoryStore) UpdateClient(ctx context.Context, client Client) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := s.clients[client.ID]; !ok {
		return ErrNotFound
	}
	if err := s.checkClient(client); err != nil {
		return err
	}
	s.clients[client.ID] = cloneClient(client)
	return nil
}

// DeleteClient implements Store.DeleteClient.
func (s *MemoryStore) DeleteClient(ctx context.Context, id ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := s.clients[id]; !ok {
		return ErrNotFound
	}
	delete(s.clients, id)
	return nil
}

func (s *MemoryStore) checkClient(client Client) error {
	if client.ID == "" || client.TokenHash == [32]byte{} || client.AllConnections && len(client.Connections) > 0 {
		return ErrInvalid
	}
	for _, id := range client.Connections {
		if _, ok := s.connections[id]; !ok {
			return ErrNotFound
		}
	}
	return nil
}

func cloneClient(client Client) Client {
	client.Connections = slices.Clone(client.Connections)
	return client
}

func sortedIDs[T any](records map[ID]T) []ID {
	ids := make([]ID, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func validStatus(status Status) bool {
	return status == StatusOK || status == StatusInvalid || status == StatusRevoked
}
