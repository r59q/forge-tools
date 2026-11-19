package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/r59q/forge-tools/forge-agents/library/workspace"
)

// Create prepares the workspace and stores a new agent.
func (m *Manager) Create(ctx context.Context, spec Spec) (Agent, error) {
	return m.create(ctx, spec, nil)
}

// childStart identifies the parent's turn and supplies the child's first prompt.
type childStart struct {
	turn *live
	text string
}

func (m *Manager) create(ctx context.Context, spec Spec, start *childStart) (Agent, error) {
	a, w, err := m.reserveCreation(ctx, spec, start)
	if err != nil {
		return Agent{}, err
	}
	defer m.unreserve(a.ID)
	if w.Kind != workspace.KindShared {
		w, err = m.cfg.Workspaces.Prepare(ctx, a.ID, spec.Workspace)
		if err != nil {
			return Agent{}, err
		}
	}
	a.Workspace = w
	created, err := m.saveCreation(ctx, a, start)
	if err != nil && w.Kind != workspace.KindShared {
		_ = m.cfg.Workspaces.Release(context.WithoutCancel(ctx), w, true)
	}
	return created, err
}

// reserveCreation counts the new agent while its workspace is prepared.
func (m *Manager) reserveCreation(ctx context.Context, spec Spec, start *childStart) (Agent, workspace.Workspace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	parent, err := m.parentForCreation(ctx, spec.Parent, start)
	if err != nil {
		return Agent{}, workspace.Workspace{}, err
	}
	a, err := m.check(spec, parent)
	if err != nil {
		return Agent{}, workspace.Workspace{}, err
	}
	agents, err := m.cfg.Store.List(ctx, Filter{})
	if err != nil {
		return Agent{}, workspace.Workspace{}, err
	}
	if err := m.checkTreeSize(a, append(agents, slices.Collect(maps.Values(m.creating))...)); err != nil {
		return Agent{}, workspace.Workspace{}, err
	}
	var w workspace.Workspace
	if spec.Workspace.Kind == workspace.KindShared {
		if parent.ID == "" {
			return Agent{}, w, fmt.Errorf("%w: a shared workspace needs a parent", ErrInvalid)
		}
		w = workspace.Workspace{Kind: workspace.KindShared, Path: parent.Workspace.Path,
			Repository: parent.Workspace.Repository, Reference: parent.Workspace.Reference}
	}
	m.creating[a.ID] = a
	m.wg.Add(1)
	return a, w, nil
}

func (m *Manager) unreserve(id string) {
	m.mu.Lock()
	delete(m.creating, id)
	m.mu.Unlock()
	m.wg.Done()
}

func (m *Manager) parentForCreation(ctx context.Context, id string, start *childStart) (Agent, error) {
	switch {
	case ctx.Err() != nil:
		return Agent{}, ctx.Err()
	case m.closed:
		return Agent{}, fmt.Errorf("%w: shutting down", ErrState)
	case id == "":
		return Agent{}, nil
	}
	parent, err := m.cfg.Store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Agent{}, fmt.Errorf("%w: parent agent %q not found", ErrInvalid, id)
	}
	if err != nil {
		return Agent{}, err
	}
	l := m.turns[id]
	if parent.State.Terminal() || l != nil && l.stopping || start != nil && (l != start.turn || turnCancelled(l)) {
		return Agent{}, fmt.Errorf("%w: parent is no longer available", ErrState)
	}
	return parent, nil
}

func (m *Manager) saveCreation(ctx context.Context, a Agent, start *childStart) (Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	parent, err := m.parentForCreation(ctx, a.Parent, start)
	if err != nil {
		return Agent{}, err
	}
	if err := m.cfg.Store.Put(ctx, a); err != nil {
		return Agent{}, err
	}
	delete(m.creating, a.ID)
	if parent.ID == "" {
		return a, nil
	}
	created, err := m.announceChild(ctx, parent, a, start)
	if err != nil {
		_ = m.cfg.Store.Delete(context.WithoutCancel(ctx), a.ID)
	}
	return created, err
}

func (m *Manager) check(spec Spec, parent Agent) (Agent, error) {
	bounds := m.cfg.Bounds
	if err := m.inherit(&spec, parent); err != nil {
		return Agent{}, err
	}
	client, ok := m.cfg.Clients[spec.Client]
	if !ok {
		return Agent{}, fmt.Errorf("%w: unknown client %q", ErrInvalid, spec.Client)
	}
	if err := client.Adapter.Check(spec.Model, spec.Effort, spec.Policy); err != nil {
		return Agent{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if parent.ID != "" {
		bounds.TurnTimeout, bounds.MaxEvents = parent.Limits.TurnTimeout, parent.Limits.MaxEvents
	}
	limits, err := bounds.resolve(spec.Limits)
	if err != nil {
		return Agent{}, err
	}
	t, id := now(), newID(16)
	a := Agent{
		ID: id, Client: spec.Client, Model: spec.Model, Effort: spec.Effort,
		Policy: spec.Policy, Limits: limits, Parent: spec.Parent, Root: id, State: StateCreated,
		AllowedModels: slices.Clone(spec.AllowedModels), CreatedAt: t, UpdatedAt: t,
	}
	if parent.ID != "" {
		a.Root, a.Depth = parent.Root, parent.Depth+1
	}
	return a, nil
}

func (m *Manager) checkTreeSize(a Agent, agents []Agent) error {
	b := m.cfg.Bounds
	if len(agents) >= b.MaxAgents {
		return fmt.Errorf("%w: at most %d agents", ErrLimit, b.MaxAgents)
	}
	if a.Depth > b.MaxDepth {
		return fmt.Errorf("%w: maximum child depth is %d", ErrLimit, b.MaxDepth)
	}
	if a.Parent == "" {
		return nil
	}
	var children, tree int
	for _, other := range agents {
		if other.Parent == a.Parent {
			children++
		}
		if other.Root == a.Root {
			tree++
		}
	}
	if children >= b.MaxChildren {
		return fmt.Errorf("%w: at most %d direct children", ErrLimit, b.MaxChildren)
	}
	if tree >= b.MaxTreeAgents {
		return fmt.Errorf("%w: at most %d agents per tree", ErrLimit, b.MaxTreeAgents)
	}
	return nil
}
