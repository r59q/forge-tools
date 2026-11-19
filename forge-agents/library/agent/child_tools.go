package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/mcp"
	"github.com/r59q/forge-tools/forge-agents/library/redact"
	"github.com/r59q/forge-tools/forge-agents/library/workspace"
)

func (m *Manager) childTools(turn context.Context, a Agent, l *live, mask redact.Redactor) *mcp.Children {
	return &mcp.Children{
		Spawn: func(call context.Context, req mcp.Spawn) (string, error) {
			ctx, cancel := toolContext(turn, call)
			defer cancel()
			return m.spawn(ctx, a, l, req, mask.String(req.Prompt))
		},
		List: func(call context.Context) ([]mcp.Child, error) {
			ctx, cancel := toolContext(turn, call)
			defer cancel()
			children, _, err := m.children(ctx, a.ID, l, nil)
			return children, err
		},
		Send: func(call context.Context, id, text string) (mcp.Child, error) {
			ctx, cancel := toolContext(turn, call)
			defer cancel()
			return m.sendChild(ctx, a.ID, id, l, mask.String(text))
		},
		Wait: func(call context.Context, ids []string, timeoutMS int64) (mcp.Wait, error) {
			ctx, cancel := toolContext(turn, call)
			defer cancel()
			return m.waitChildren(ctx, a.ID, l, ids, timeoutMS)
		},
		Stop: func(call context.Context, id string) (mcp.Child, error) {
			ctx, cancel := toolContext(turn, call)
			defer cancel()
			child, err := m.stop(ctx, id, a.ID, l)
			return childSummary(child, nil), err
		},
	}
}

func toolContext(turn, call context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(call)
	stop := context.AfterFunc(turn, cancel)
	if turn.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stop()
		cancel()
	}
}

// checkCaller rejects an in-flight tool request after its turn has ended.
func (m *Manager) checkCaller(ctx context.Context, id string, l *live) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case m.closed || m.turns[id] != l || turnCancelled(l):
		return fmt.Errorf("%w: caller's turn has ended", ErrState)
	default:
		return nil
	}
}

func turnCancelled(l *live) bool {
	select {
	case <-l.cancelled:
		return true
	default:
		return false
	}
}

func (m *Manager) spawn(ctx context.Context, parent Agent, l *live, req mcp.Spawn, text string) (string, error) {
	if strings.TrimSpace(text) == "" || len(text) > m.cfg.Bounds.MaxEventBytes {
		return "", fmt.Errorf("%w: a prompt needs 1 to %d bytes", ErrInvalid, m.cfg.Bounds.MaxEventBytes)
	}
	w, err := m.childWorkspace(ctx, parent, req.Workspace)
	if err != nil {
		return "", err
	}
	spec := Spec{Client: req.Client, Model: parent.Model, Effort: parent.Effort, Parent: parent.ID, Workspace: w, Policy: parent.Policy}
	if req.Model != nil {
		spec.Model, spec.modelSet = *req.Model, true
	}
	if req.Effort != nil {
		spec.Effort, spec.effortSet = *req.Effort, true
	}
	if req.Mode != "" {
		spec.Policy.Mode = req.Mode
	}
	if req.Ask != nil {
		spec.Policy.Ask = *req.Ask
	}
	for _, model := range req.AllowedModels {
		spec.AllowedModels = append(spec.AllowedModels, Model(model))
	}
	a, err := m.create(ctx, spec, &childStart{turn: l, text: text})
	return a.ID, err
}

func (m *Manager) childWorkspace(ctx context.Context, parent Agent, mode string) (workspace.Spec, error) {
	switch mode {
	case string(workspace.KindShared):
		return workspace.Spec{Kind: workspace.KindShared}, nil
	case "none":
		return workspace.Spec{Kind: workspace.KindScratch}, nil
	case string(workspace.KindCheckout):
		repo, err := m.ancestorRepo(ctx, parent)
		return workspace.Spec{Kind: workspace.KindCheckout, Repository: repo}, err
	default:
		return workspace.Spec{}, fmt.Errorf("%w: workspace must be shared, checkout or none", ErrInvalid)
	}
}

func (m *Manager) ancestorRepo(ctx context.Context, a Agent) (workspace.Repository, error) {
	for {
		if repo := a.Workspace.Repository; !repo.IsZero() {
			return repo, nil
		}
		if a.Workspace.Kind == workspace.KindPath {
			return workspace.Repository{LocalPath: a.Workspace.Path}, nil
		}
		if a.Parent == "" {
			return workspace.Repository{}, fmt.Errorf("%w: no repository is associated with the parent workspace", ErrInvalid)
		}
		parent, err := m.cfg.Store.Get(ctx, a.Parent)
		if err != nil {
			return workspace.Repository{}, err
		}
		a = parent
	}
}

func (m *Manager) sendChild(ctx context.Context, caller, id string, l *live, text string) (mcp.Child, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkCaller(ctx, caller, l); err != nil {
		return mcp.Child{}, err
	}
	a, err := m.directChild(ctx, caller, id)
	if err != nil {
		return mcp.Child{}, err
	}
	if a.State != StateCreated && a.State != StateIdle {
		return mcp.Child{}, fmt.Errorf("%w: agent is %s", ErrState, a.State)
	}
	a, err = m.startTurn(ctx, a, text)
	if err != nil && a.State == StateFailed {
		m.afterTurn(ctx, a)
	}
	return childSummary(a, nil), err
}

func (m *Manager) children(ctx context.Context, caller string, l *live, ids []string) ([]mcp.Child, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkCaller(ctx, caller, l); err != nil {
		return nil, false, err
	}
	children, err := m.selectedChildren(ctx, caller, ids)
	if err != nil {
		return nil, false, err
	}
	results := make([]mcp.Child, 0, len(children))
	busy := false
	for _, a := range children {
		results = append(results, childSummary(a, m.turns[a.ID]))
		busy = busy || childBusy(a)
	}
	return results, busy, nil
}

func (m *Manager) selectedChildren(ctx context.Context, caller string, ids []string) ([]Agent, error) {
	if len(ids) == 0 {
		return m.cfg.Store.List(ctx, Filter{Parent: caller})
	}
	children := make([]Agent, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		a, err := m.directChild(ctx, caller, id)
		if err != nil {
			return nil, err
		}
		seen[id] = true
		children = append(children, a)
	}
	return children, nil
}

func childSummary(a Agent, l *live) mcp.Child {
	child := mcp.Child{ID: a.ID, State: string(a.State), Result: a.Result, Status: a.ResultStatus}
	if l != nil {
		for _, pending := range l.pending {
			e := pending.event
			child.Inputs = append(child.Inputs, mcp.Input{
				ID: e.RequestID, Kind: e.RequestKind, Text: shortResult(e.Text), Tool: e.ToolName, Input: shortResult(e.Input),
			})
		}
		sort.Slice(child.Inputs, func(i, j int) bool { return child.Inputs[i].ID < child.Inputs[j].ID })
	}
	return child
}

func (m *Manager) waitChildren(ctx context.Context, caller string, l *live, ids []string, ms int64) (mcp.Wait, error) {
	if len(ids) == 0 || len(ids) > m.cfg.Bounds.MaxChildren {
		return mcp.Wait{}, fmt.Errorf("%w: provide one to %d child IDs", ErrInvalid, m.cfg.Bounds.MaxChildren)
	}
	duration := 30 * time.Second
	if ms < 0 || ms > int64((1<<63-1)/time.Millisecond) {
		return mcp.Wait{}, fmt.Errorf("%w: invalid wait timeout", ErrInvalid)
	}
	if ms > 0 {
		duration = time.Duration(ms) * time.Millisecond
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	timedOut := false
	for {
		changed := m.changed(caller)
		children, busy, err := m.children(ctx, caller, l, ids)
		if err != nil || !busy || timedOut {
			return mcp.Wait{Children: children, TimedOut: timedOut && busy}, err
		}
		select {
		case <-ctx.Done():
			return mcp.Wait{}, ctx.Err()
		case <-timer.C:
			timedOut = true
		case <-changed:
		}
	}
}
