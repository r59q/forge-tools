package agent

import (
	"context"
	"slices"
	"sort"
	"sync"

	"github.com/r59q/forge-tools/forge-agents/library/transcript"
	"github.com/r59q/forge-tools/forge-agents/library/workspace"
)

// Filter selects agents. Zero fields match any agent.
type Filter struct {
	State      State
	Parent     string
	Repository workspace.Repository
}

// Match reports whether a passes f.
func (f Filter) Match(a Agent) bool {
	return (f.State == "" || a.State == f.State) &&
		(f.Parent == "" || a.Parent == f.Parent) &&
		(f.Repository.IsZero() || a.Workspace.Repository == f.Repository)
}

// Output is the raw client output of one turn, kept apart from the
// transcript.
type Output struct {
	Stdout    []byte `json:"stdout,omitzero"`
	Stderr    []byte `json:"stderr,omitzero"`
	Truncated bool   `json:"truncated,omitzero"`
}

// Store keeps agents, transcripts and raw output. Implementations must be
// safe for concurrent use.
type Store interface {
	Put(ctx context.Context, a Agent) error
	// Get returns ErrNotFound for an unknown agent.
	Get(ctx context.Context, id string) (Agent, error)
	// List returns matching agents, oldest first.
	List(ctx context.Context, f Filter) ([]Agent, error)
	// Delete removes an agent, its transcript and its output.
	Delete(ctx context.Context, id string) error
	// Append numbers e after the agent's last event and stores it, or
	// returns ErrTranscriptFull when the transcript holds max events.
	Append(ctx context.Context, id string, e transcript.Event, max int64) (transcript.Event, error)
	// Events returns up to limit events after sequence number after.
	Events(ctx context.Context, id string, after int64, limit int) ([]transcript.Event, error)
	// PutOutput stores a turn's output, keeping only the latest keep turns.
	PutOutput(ctx context.Context, id string, turn int, o Output, keep int) error
	// Output returns ErrNotFound for a turn without output.
	Output(ctx context.Context, id string, turn int) (Output, error)
}

// MemoryStore keeps everything in memory.
type MemoryStore struct {
	mu      sync.Mutex
	agents  map[string]Agent
	events  map[string][]transcript.Event
	outputs map[string]map[int]Output
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{agents: map[string]Agent{}, events: map[string][]transcript.Event{}, outputs: map[string]map[int]Output{}}
}

// Put stores a.
func (s *MemoryStore) Put(_ context.Context, a Agent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agents[a.ID] = a
	return nil
}

// Get returns an agent.
func (s *MemoryStore) Get(_ context.Context, id string) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	if !ok {
		return Agent{}, ErrNotFound
	}
	return a, nil
}

// List returns matching agents.
func (s *MemoryStore) List(_ context.Context, f Filter) ([]Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []Agent
	for _, a := range s.agents {
		if f.Match(a) {
			list = append(list, a)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].ID < list[j].ID
		}
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	return list, nil
}

// Delete removes an agent.
func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.agents, id)
	delete(s.events, id)
	delete(s.outputs, id)
	return nil
}

// Append stores an event.
func (s *MemoryStore) Append(_ context.Context, id string, e transcript.Event, limit int64) (transcript.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.agents[id]; !ok {
		return e, ErrNotFound
	}
	events := s.events[id]
	if int64(len(events)) >= limit {
		return e, ErrTranscriptFull
	}
	e.Seq = int64(len(events)) + 1
	s.events[id] = append(events, e)
	return e, nil
}

// Events returns a page of events.
func (s *MemoryStore) Events(_ context.Context, id string, after int64, limit int) ([]transcript.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.agents[id]; !ok {
		return nil, ErrNotFound
	}
	events := s.events[id]
	if after < 0 {
		after = 0
	}
	if after >= int64(len(events)) {
		return nil, nil
	}
	page := events[after:]
	if len(page) > limit {
		page = page[:limit]
	}
	return slices.Clone(page), nil
}

// PutOutput stores a turn's output.
func (s *MemoryStore) PutOutput(_ context.Context, id string, turn int, o Output, keep int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.agents[id]; !ok {
		return ErrNotFound
	}
	turns := s.outputs[id]
	if turns == nil {
		turns = map[int]Output{}
		s.outputs[id] = turns
	}
	turns[turn] = o
	for t := range turns {
		if t <= turn-keep {
			delete(turns, t)
		}
	}
	return nil
}

// Output returns a turn's output.
func (s *MemoryStore) Output(_ context.Context, id string, turn int) (Output, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.outputs[id][turn]
	if !ok {
		return Output{}, ErrNotFound
	}
	return o, nil
}
