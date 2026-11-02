package workspaces

import (
	"context"
	"maps"
	"sync"
)

// MemoryStore holds metadata in memory. Its zero value is ready to use.
// It is useful for tests and short-lived programs, but cannot recover working
// copies after a process restart. Production callers should use a durable Store.
type MemoryStore struct {
	mu    sync.Mutex
	state State
}

// Read returns an isolated snapshot.
func (s *MemoryStore) Read(ctx context.Context) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	return State{Workspaces: maps.Clone(s.state.Workspaces)}, nil
}

// Update commits successful callbacks atomically, without retaining callback maps.
func (s *MemoryStore) Update(ctx context.Context, fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	next := State{Workspaces: maps.Clone(s.state.Workspaces)}
	if next.Workspaces == nil {
		next.Workspaces = map[string]Record{}
	}
	if err := fn(&next); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.state = State{Workspaces: maps.Clone(next.Workspaces)}
	return nil
}
