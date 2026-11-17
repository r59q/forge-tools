package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/mcp"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
)

// answer is the reply to an input request.
type answer struct {
	text  string
	allow bool
}

// errUnanswered is returned to the client when its turn ends first.
var errUnanswered = errors.New("the turn ended before the question was answered")

// binding connects a turn's tool calls to input requests.
func (m *Manager) binding(ctx context.Context, a Agent, l *live) mcp.Binding {
	b := mcp.Binding{
		Ask: func(call context.Context, question string) (string, error) {
			reply, err := m.request(ctx, call, a, l, transcript.Event{
				Kind: transcript.KindInputRequest, RequestKind: transcript.RequestQuestion, Text: question,
			})
			return reply.text, err
		},
	}
	// Only clients that can pause for permission accept Policy.Ask.
	if a.Policy.Ask {
		b.Approve = func(call context.Context, tool string, input json.RawMessage) (mcp.Decision, error) {
			reply, err := m.request(ctx, call, a, l, transcript.Event{
				Kind: transcript.KindInputRequest, RequestKind: transcript.RequestPermission,
				ToolName: tool, Input: adapter.JSON(input),
			})
			return mcp.Decision{Allow: reply.allow, Message: reply.text}, err
		}
	}
	return b
}

// request records an input request, puts the agent in waiting_input and
// waits for the answer, the end of the turn, or the client giving up.
func (m *Manager) request(turn, call context.Context, a Agent, l *live, e transcript.Event) (answer, error) {
	store := context.WithoutCancel(turn)
	e.RequestID = newID(8)
	ch := make(chan answer, 1)
	m.mu.Lock()
	l.pending[e.RequestID] = ch
	m.mu.Unlock()
	defer m.settle(store, a.ID, l, e.RequestID)
	if err := m.append(store, a, e, false); err != nil {
		if errors.Is(err, ErrTranscriptFull) {
			l.cancel(errFull)
		}
		return answer{}, err
	}
	m.mu.Lock()
	if _, ok := l.pending[e.RequestID]; ok {
		_, _ = m.update(store, a.ID, func(s *Agent) error {
			if s.State == StateRunning {
				s.State = StateWaitingInput
			}
			return nil
		})
	}
	m.mu.Unlock()
	select {
	case reply := <-ch:
		return reply, nil
	case <-turn.Done():
		return answer{}, errUnanswered
	case <-call.Done():
		_ = m.append(store, a, transcript.Event{Kind: transcript.KindError, RequestID: e.RequestID,
			Text: "the client stopped waiting for input request " + e.RequestID}, false)
		return answer{}, call.Err()
	}
}

// settle forgets a request and returns the agent to running when nothing
// else waits for input.
func (m *Manager) settle(ctx context.Context, id string, l *live, requestID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(l.pending, requestID)
	m.resume(ctx, id, l)
}

// resume moves a waiting agent back to running; the caller holds m.mu.
func (m *Manager) resume(ctx context.Context, id string, l *live) {
	if len(l.pending) > 0 || m.turns[id] != l {
		return
	}
	_, _ = m.update(ctx, id, func(s *Agent) error {
		if s.State == StateWaitingInput {
			s.State = StateRunning
		}
		return nil
	})
}

// Answer replies to a pending input request. For a permission request,
// allow grants it and text, if any, explains a denial.
func (m *Manager) Answer(ctx context.Context, id, requestID, text string, allow bool) (Agent, error) {
	if len(text) > m.cfg.Bounds.MaxEventBytes {
		return Agent{}, fmt.Errorf("%w: an answer has at most %d bytes", ErrInvalid, m.cfg.Bounds.MaxEventBytes)
	}
	m.mu.Lock()
	a, err := m.cfg.Store.Get(ctx, id)
	if err != nil {
		m.mu.Unlock()
		return Agent{}, err
	}
	l := m.turns[id]
	var ch chan answer
	if l != nil {
		ch = l.pending[requestID]
	}
	if ch == nil {
		m.mu.Unlock()
		return Agent{}, fmt.Errorf("%w: no pending input request %q", ErrNotFound, requestID)
	}
	delete(l.pending, requestID)
	err = m.append(ctx, a, transcript.Event{Kind: transcript.KindInputAnswer, RequestID: requestID, Text: text, Allow: allow}, false)
	if err != nil {
		l.pending[requestID] = ch
		m.mu.Unlock()
		return Agent{}, err
	}
	ch <- answer{text: text, allow: allow}
	m.resume(ctx, id, l)
	m.mu.Unlock()
	return m.cfg.Store.Get(ctx, id)
}
