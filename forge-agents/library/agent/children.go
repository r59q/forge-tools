package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/r59q/forge-tools/forge-agents/library/mcp"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
)

const maxResultBytes = 4 << 10

func shortResult(text string) string {
	return (transcript.Event{Text: text}).Truncate(maxResultBytes).Text
}

func (m *Manager) announceChild(ctx context.Context, parent, child Agent, start *childStart) (Agent, error) {
	if start != nil {
		if err := m.turnSlot(); err != nil {
			return Agent{}, err
		}
		if _, err := m.treeBudget(ctx, child); err != nil {
			return Agent{}, err
		}
	}
	if err := m.append(ctx, parent, transcript.Event{
		Kind: transcript.KindChildSpawned, ChildID: child.ID, ChildState: string(child.State),
	}, false); err != nil {
		return Agent{}, err
	}
	if start == nil {
		return child, nil
	}
	started, err := m.startTurn(ctx, child, start.text)
	if err != nil {
		m.childFinished(context.WithoutCancel(ctx), parent, Agent{
			ID: child.ID, State: StateFailed, Result: err.Error(), ResultStatus: transcript.StatusError,
		})
	}
	return started, err
}

func (m *Manager) childFinished(ctx context.Context, parent, child Agent) {
	err := m.append(ctx, parent, transcript.Event{
		Kind: transcript.KindChildFinished, ChildID: child.ID, ChildState: string(child.State),
		Text: child.Result, Status: child.ResultStatus,
	}, false)
	if errors.Is(err, ErrTranscriptFull) {
		if l := m.turns[parent.ID]; l != nil {
			l.cancel(errFull)
		}
	}
}

func childBusy(child Agent) bool {
	return child.State.Busy() || child.State == StateWaitingChildren
}

func (m *Manager) hasWorkingChildren(ctx context.Context, id string) bool {
	children, _ := m.cfg.Store.List(ctx, Filter{Parent: id})
	for _, child := range children {
		if childBusy(child) {
			return true
		}
	}
	return false
}

// afterTurn records the result for its parent and gives continuations a
// chance to use the freed slot. The caller holds m.mu.
func (m *Manager) afterTurn(ctx context.Context, a Agent) {
	if a.Parent != "" {
		if parent, err := m.cfg.Store.Get(ctx, a.Parent); err == nil {
			m.childFinished(ctx, parent, a)
		}
	}
	m.resumeParents(ctx)
}

func (m *Manager) resumeParents(ctx context.Context) {
	// A failed continuation can free its own parent. Scan again if any failed.
	for !m.closed && len(m.turns) < m.cfg.Bounds.MaxRunning {
		retry := m.resumeReadyParents(ctx)
		if !retry {
			return
		}
	}
}

func (m *Manager) resumeReadyParents(ctx context.Context) bool {
	agents, err := m.cfg.Store.List(ctx, Filter{State: StateWaitingChildren})
	if err != nil {
		return false
	}
	retry := false
	for _, parent := range agents {
		if len(m.turns) >= m.cfg.Bounds.MaxRunning {
			break
		}
		retry = m.resumeParent(ctx, parent) || retry
	}
	return retry
}

func (m *Manager) resumeParent(ctx context.Context, parent Agent) bool {
	children, err := m.cfg.Store.List(ctx, Filter{Parent: parent.ID})
	if err != nil {
		return false
	}
	results := make([]mcp.Child, 0, len(children))
	for _, child := range children {
		if childBusy(child) {
			return false
		}
		results = append(results, childSummary(child, nil))
	}
	const intro = "Your child agents have finished. Review their results, summarize them, and continue the task.\n"
	prompt := resultsPrompt(intro, results, m.cfg.Bounds.MaxEventBytes)
	started, err := m.startTurn(ctx, parent, prompt)
	if err == nil {
		return false
	}
	if started.State != StateFailed {
		started = m.failTurn(ctx, parent, err.Error())
	}
	if started.State != StateFailed {
		return false
	}
	if ancestor, err := m.cfg.Store.Get(ctx, started.Parent); err == nil {
		m.childFinished(ctx, ancestor, started)
	}
	return true
}

// resultsPrompt leaves each child ID and state intact as JSON. Distribute
// any space left over equally for result text, accounting for JSON escaping.
func resultsPrompt(intro string, children []mcp.Child, limit int) string {
	encoded, _ := json.Marshal(children)
	if len(intro)+len(encoded) <= limit {
		return intro + string(encoded)
	}
	texts := make([]string, 0, len(children))
	for i := range children {
		texts = append(texts, children[i].Result)
		children[i].Result = ""
	}
	base, _ := json.Marshal(children)
	if len(intro)+len(base) > limit {
		// A tiny event limit can leave too little room even for the IDs.
		// startTurn surfaces the limit instead of silently dropping children.
		return intro + string(base)
	}
	space := (limit - len(intro) - len(base)) / max(len(children), 1)
	for i, text := range texts {
		// encoding/json can expand a byte to a six-byte escape.
		children[i].Result = (transcript.Event{Text: text}).Truncate(max(1, space/6)).Text
		if space < 6 {
			children[i].Result = ""
		}
	}
	encoded, _ = json.Marshal(children)
	return strings.TrimSpace(intro + string(encoded))
}

func (m *Manager) directChild(ctx context.Context, parentID, id string) (Agent, error) {
	a, err := m.cfg.Store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) || err == nil && a.Parent != parentID {
		return Agent{}, fmt.Errorf("%w: no direct child %q", ErrNotFound, id)
	}
	return a, err
}
