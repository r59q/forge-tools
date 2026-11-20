package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/r59q/forge-tools/forge-agents/library/transcript"
)

var (
	errTreeTokens = errors.New("tree token budget exhausted")
	errTreeTime   = errors.New("tree time budget exhausted")
)

// treeBudget checks the shared budget and starts the clock on first use.
// The caller holds m.mu.
func (m *Manager) treeBudget(ctx context.Context, a Agent) (Agent, error) {
	root, err := m.cfg.Store.Get(ctx, a.Root)
	if err != nil {
		return Agent{}, err
	}
	switch {
	case root.TreeTokens >= m.cfg.Bounds.MaxTreeTokens:
		return Agent{}, fmt.Errorf("%w: %w", ErrLimit, errTreeTokens)
	case root.TreeDeadline.IsZero():
		root.TreeDeadline = now().Add(m.cfg.Bounds.TreeTimeout)
		return root, m.cfg.Store.Put(ctx, root)
	case !now().Before(root.TreeDeadline):
		return Agent{}, fmt.Errorf("%w: %w", ErrLimit, errTreeTime)
	default:
		return root, nil
	}
}

// chargeUsage accounts for reported tokens once and stops concurrent turns
// when they have used the shared budget. Cached input is already counted.
func (m *Manager) chargeUsage(ctx context.Context, rootID string, usage transcript.Usage) {
	input, output := max(usage.InputTokens, 0), max(usage.OutputTokens, 0)
	if input == 0 && output == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	root, err := m.cfg.Store.Get(ctx, rootID)
	if err != nil {
		return
	}
	limit := max(root.TreeTokens, m.cfg.Bounds.MaxTreeTokens)
	root.TreeTokens += min(input, limit-root.TreeTokens)
	root.TreeTokens += min(output, limit-root.TreeTokens)
	_ = m.cfg.Store.Put(ctx, root)
	if root.TreeTokens >= m.cfg.Bounds.MaxTreeTokens {
		for _, l := range m.turns {
			if l.root == rootID {
				l.cancel(errTreeTokens)
			}
		}
	}
}
