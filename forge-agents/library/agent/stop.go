package agent

import (
	"context"
	"fmt"

	"github.com/r59q/forge-tools/forge-agents/library/workspace"
)

// Cancel ends the running turn or cancels a pending continuation.
func (m *Manager) Cancel(ctx context.Context, id string) (Agent, error) {
	m.mu.Lock()
	l := m.turns[id]
	if l == nil {
		defer m.mu.Unlock()
		a, err := m.update(ctx, id, func(a *Agent) error {
			if a.State != StateWaitingChildren {
				return fmt.Errorf("%w: no turn is running", ErrState)
			}
			a.State = StateIdle
			return nil
		})
		if err == nil {
			m.resumeParents(ctx)
		}
		return a, err
	}
	l.cancel(errCancelled)
	m.mu.Unlock()
	if err := waitDone(ctx, l.done); err != nil {
		return Agent{}, err
	}
	return m.cfg.Store.Get(ctx, id)
}

// Stop ends any running turns and stops the agent and its descendants for good.
func (m *Manager) Stop(ctx context.Context, id string) (Agent, error) {
	return m.stop(ctx, id, "", nil)
}

func (m *Manager) stop(ctx context.Context, id, caller string, turn *live) (Agent, error) {
	done, err := m.stopSubtree(ctx, id, caller, turn)
	if err != nil {
		return Agent{}, err
	}
	for _, end := range done {
		if err := waitDone(ctx, end); err != nil {
			return Agent{}, err
		}
	}
	return m.cfg.Store.Get(ctx, id)
}

func (m *Manager) stopSubtree(ctx context.Context, id, caller string, turn *live) ([]<-chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var a Agent
	var err error
	if turn == nil {
		a, err = m.cfg.Store.Get(ctx, id)
	} else {
		if err := m.checkCaller(ctx, caller, turn); err != nil {
			return nil, err
		}
		a, err = m.directChild(ctx, caller, id)
	}
	if err != nil {
		return nil, err
	}
	tree, err := m.cfg.Store.List(ctx, Filter{Root: a.Root})
	if err != nil {
		return nil, err
	}
	// Once stopping starts, finish marking the subtree even if the
	// request times out. No child can start while it is being stopped.
	store := context.WithoutCancel(ctx)
	done, err := m.markStopped(store, a, tree)
	m.resumeParents(store)
	return done, err
}

func (m *Manager) markStopped(ctx context.Context, root Agent, tree []Agent) ([]<-chan struct{}, error) {
	byParent := make(map[string][]Agent)
	for _, a := range tree {
		byParent[a.Parent] = append(byParent[a.Parent], a)
	}
	queue := []Agent{root}
	var done []<-chan struct{}
	for len(queue) > 0 {
		a := queue[0]
		queue = append(queue[1:], byParent[a.ID]...)
		if l := m.turns[a.ID]; l != nil {
			l.stopping = true
			l.cancel(errStopped)
			done = append(done, l.done)
		} else if !a.State.Terminal() {
			if _, err := m.update(ctx, a.ID, func(s *Agent) error { s.State = StateStopped; return nil }); err != nil {
				return done, err
			}
		}
	}
	return done, nil
}

func waitDone(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Delete stops the subtree, releases the named agent's workspace and
// removes its record. Descendants keep their transcripts and owned workspaces.
func (m *Manager) Delete(ctx context.Context, id string, removeCheckout bool) error {
	a, err := m.Stop(ctx, id)
	if err != nil {
		return err
	}
	if a.Workspace.Kind != workspace.KindShared {
		if err := m.cfg.Workspaces.Release(ctx, a.Workspace, removeCheckout); err != nil {
			return fmt.Errorf("release workspace: %w", err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.cfg.Store.Delete(ctx, id); err != nil {
		return err
	}
	m.signal(id)
	if a.Parent != "" {
		m.signal(a.Parent)
	}
	return nil
}
