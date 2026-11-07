package sources

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sync"
)

// Store keeps sources by ID. Update must commit fn's changes durably, all
// or nothing, before it returns nil.
type Store interface {
	// Read returns an isolated copy of the sources.
	Read(ctx context.Context) (map[string]Source, error)
	// Update applies fn to a copy and commits it only if fn succeeds.
	Update(ctx context.Context, fn func(map[string]Source) error) error
}

// MemoryStore keeps sources in memory and gives each new encoding to Save
// before it becomes visible. Its zero value is an empty, process-local
// store.
type MemoryStore struct {
	// Save persists an encoding. A nil Save keeps sources in memory only.
	Save func(ctx context.Context, data []byte) error

	mu      sync.Mutex
	sources map[string]Source
}

// NewMemoryStore returns a store starting from data, an encoding written by
// an earlier Save; empty data starts empty.
func NewMemoryStore(data []byte, save func(context.Context, []byte) error) (*MemoryStore, error) {
	s := &MemoryStore{Save: save}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.sources); err != nil {
		return nil, err
	}

	return s, nil
}

// Read returns a copy of the sources.
func (s *MemoryStore) Read(ctx context.Context) (map[string]Source, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return clone(s.sources), nil
}

// Update commits fn's changes only if fn and Save succeed.
func (s *MemoryStore) Update(ctx context.Context, fn func(map[string]Source) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	next := clone(s.sources)
	if err := fn(next); err != nil {
		return err
	}
	if s.Save != nil {
		data, err := json.Marshal(next) //nolint:gosec // keeping the generated secrets is this store's job.
		if err != nil {
			return err
		}
		if err := s.Save(ctx, data); err != nil {
			return err
		}
	}
	s.sources = next

	return nil
}

// clone copies sources deeply enough that callers cannot change the store.
func clone(in map[string]Source) map[string]Source {
	out := make(map[string]Source, len(in))
	for id, src := range in {
		src.Repositories = slices.Clone(src.Repositories)
		src.Hooks = maps.Clone(src.Hooks)
		out[id] = src
	}

	return out
}
