package review

import (
	"context"
	"encoding/json"
	"sync"
)

// MemoryStore is transactional process-local storage for embedded use/tests.
// Its zero value is ready for use. Use durable storage for service deployments.
type MemoryStore struct {
	mu    sync.Mutex
	state State
}

func copyState(s State) State {
	b, _ := json.Marshal(s)
	var out State
	_ = json.Unmarshal(b, &out)
	if out.Subjects == nil {
		out.Subjects = map[string]Subject{}
	}
	if out.Reviews == nil {
		out.Reviews = map[string]Review{}
	}
	return out
}

// Read returns an isolated copy of the current state.
func (m *MemoryStore) Read(ctx context.Context) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	return copyState(m.state), nil
}

// Update commits fn's changes only on success.
func (m *MemoryStore) Update(ctx context.Context, fn func(*State) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s := copyState(m.state)
	if err := fn(&s); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.state = copyState(s)
	return nil
}
