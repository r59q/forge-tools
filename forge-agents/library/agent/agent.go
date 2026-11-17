// Package agent runs headless agent sessions: each agent is one client
// CLI session in a workspace, driven turn by turn, with a durable
// transcript of shared events.
package agent

import (
	"errors"
	"fmt"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/workspace"
)

// Errors returned by the manager.
var (
	ErrNotFound = errors.New("agent not found")
	ErrInvalid  = errors.New("invalid request")
	// ErrState reports an operation the agent's state does not allow.
	ErrState = errors.New("agent state does not allow this")
	// ErrLimit reports a bound that was reached.
	ErrLimit = errors.New("limit reached")
	// ErrTranscriptFull is returned by Store.Append at the event limit.
	ErrTranscriptFull = errors.New("transcript is full")
)

// State is where an agent is in its life.
type State string

// Agent states. created → running → idle | waiting_input → … → stopped |
// failed.
const (
	StateCreated      State = "created"
	StateRunning      State = "running"
	StateIdle         State = "idle"
	StateWaitingInput State = "waiting_input"
	StateStopped      State = "stopped"
	StateFailed       State = "failed"
)

// Terminal reports a state no turn can leave.
func (s State) Terminal() bool {
	return s == StateStopped || s == StateFailed
}

// Busy reports a state with a turn in progress.
func (s State) Busy() bool {
	return s == StateRunning || s == StateWaitingInput
}

// Limits bound one agent. Zero values take the manager's bounds.
type Limits struct {
	TurnTimeout time.Duration `json:"turnTimeout,omitzero"`
	MaxEvents   int64         `json:"maxEvents,omitzero"`
}

// Agent is one client session.
type Agent struct {
	ID        string              `json:"id"`
	Client    string              `json:"client"`
	Model     string              `json:"model,omitzero"`
	Effort    string              `json:"effort,omitzero"`
	Workspace workspace.Workspace `json:"workspace"`
	Policy    adapter.Policy      `json:"policy"`
	Limits    Limits              `json:"limits"`
	// Parent is the agent that started this one, if any.
	Parent string `json:"parent,omitzero"`
	State  State  `json:"state"`
	// Session is the client's session ID, resumed by the next turn.
	Session string `json:"session,omitzero"`
	// Turns counts the turns started.
	Turns int `json:"turns,omitzero"`
	// Error is why the latest turn failed, if it did.
	Error     string    `json:"error,omitzero"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Spec asks for an agent.
type Spec struct {
	Client    string
	Model     string
	Effort    string
	Workspace workspace.Spec
	// Policy defaults to read-only.
	Policy adapter.Policy
	Limits Limits
	Parent string
}

// Bounds limit the manager. Zero values take defaults.
type Bounds struct {
	// MaxAgents is how many agents may exist.
	MaxAgents int
	// MaxRunning is how many turns may run at once.
	MaxRunning int
	// MaxEvents is the most events in one transcript.
	MaxEvents int64
	// MaxEventBytes bounds one event, and so a message or answer.
	MaxEventBytes int
	// MaxLineBytes bounds one line of client output.
	MaxLineBytes int
	// MaxOutputBytes bounds the raw output kept for one turn.
	MaxOutputBytes int
	// KeepOutputs is how many turns keep raw output.
	KeepOutputs int
	// TurnTimeout is the longest a turn may run.
	TurnTimeout time.Duration
}

// Default bounds.
const (
	DefaultMaxAgents      = 100
	DefaultMaxRunning     = 4
	DefaultMaxEvents      = 10000
	DefaultMaxEventBytes  = 256 << 10
	DefaultMaxLineBytes   = 4 << 20
	DefaultMaxOutputBytes = 4 << 20
	DefaultKeepOutputs    = 10
	DefaultTurnTimeout    = time.Hour
	// minEvents leaves room for a message, the closing events and some
	// work.
	minEvents = 16
	// closing is how many events are kept back to end a turn visibly.
	closing = 2
)

// WithDefaults fills zero bounds.
func (b Bounds) WithDefaults() Bounds {
	b.MaxAgents = orDefault(b.MaxAgents, DefaultMaxAgents)
	b.MaxRunning = orDefault(b.MaxRunning, DefaultMaxRunning)
	b.MaxEvents = orDefault(b.MaxEvents, DefaultMaxEvents)
	b.MaxEventBytes = orDefault(b.MaxEventBytes, DefaultMaxEventBytes)
	b.MaxLineBytes = orDefault(b.MaxLineBytes, DefaultMaxLineBytes)
	b.MaxOutputBytes = orDefault(b.MaxOutputBytes, DefaultMaxOutputBytes)
	b.KeepOutputs = orDefault(b.KeepOutputs, DefaultKeepOutputs)
	b.TurnTimeout = orDefault(b.TurnTimeout, DefaultTurnTimeout)
	return b
}

//nolint:ireturn // T is a number, not an interface.
func orDefault[T int | int64 | time.Duration](v, d T) T {
	if v <= 0 {
		return d
	}
	return v
}

// resolve fills zero limits from b and rejects limits above it.
func (b Bounds) resolve(l Limits) (Limits, error) {
	if l.TurnTimeout < 0 || l.TurnTimeout > b.TurnTimeout {
		return l, fmt.Errorf("%w: turn timeout must be at most %s", ErrInvalid, b.TurnTimeout)
	}
	if l.MaxEvents < 0 || l.MaxEvents > b.MaxEvents || (l.MaxEvents > 0 && l.MaxEvents < minEvents) {
		return l, fmt.Errorf("%w: max events must be between %d and %d", ErrInvalid, minEvents, b.MaxEvents)
	}
	l.TurnTimeout = orDefault(l.TurnTimeout, b.TurnTimeout)
	l.MaxEvents = orDefault(l.MaxEvents, b.MaxEvents)
	return l, nil
}
