package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/executor"
	"github.com/r59q/forge-tools/forge-agents/library/mcp"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
	"github.com/r59q/forge-tools/forge-agents/library/workspace"
)

// Client is a configured client CLI.
type Client struct {
	Adapter adapter.Adapter
	// Path is the client binary.
	Path string
}

// Config configures a manager.
type Config struct {
	Store      Store
	Clients    map[string]Client
	Executor   executor.Executor
	Workspaces workspace.Provider
	// Env is the whole environment of client processes, holding their
	// credentials; it is never recorded.
	Env []string
	// RunDir holds per-turn files such as client MCP configuration.
	RunDir string
	Bounds Bounds
	// ToolsListen is the address of the MCP tool server clients reach;
	// default a free loopback port.
	ToolsListen string
}

// live is the runtime state of an agent with a turn in progress.
type live struct {
	cancel  context.CancelCauseFunc
	done    chan struct{}
	pending map[string]chan answer
}

// Manager runs agents.
type Manager struct {
	cfg      Config
	tools    mcp.Server
	toolsURL string
	server   *http.Server

	mu       sync.Mutex
	turns    map[string]*live
	creating int
	closed   bool
	wg       sync.WaitGroup

	nmu    sync.Mutex
	notify map[string]chan struct{}
}

// Causes that end a turn early.
var (
	errCancelled = errors.New("turn cancelled")
	errStopped   = errors.New("agent stopped")
	errFull      = errors.New("transcript limit reached")
	errShutdown  = errors.New("turn interrupted: forge-agents shut down")
)

// interrupted is the error of turns a restart cut short.
const interrupted = "turn interrupted: forge-agents restarted"

// Open starts a manager and its tool server, and ends turns a previous
// run left in progress.
func Open(ctx context.Context, cfg Config) (*Manager, error) {
	if cfg.Store == nil || cfg.Executor == nil || cfg.Workspaces == nil || len(cfg.Clients) == 0 {
		return nil, fmt.Errorf("%w: a manager needs a store, an executor, workspaces and clients", ErrInvalid)
	}
	cfg.Bounds = cfg.Bounds.WithDefaults()
	if cfg.ToolsListen == "" {
		cfg.ToolsListen = "127.0.0.1:0"
	}
	m := &Manager{cfg: cfg, turns: map[string]*live{}, notify: map[string]chan struct{}{}}
	if err := m.recover(ctx); err != nil {
		return nil, err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.ToolsListen)
	if err != nil {
		return nil, fmt.Errorf("tool server: %w", err)
	}
	m.toolsURL = "http://" + ln.Addr().String() + "/mcp"
	mux := http.NewServeMux()
	mux.Handle("/mcp", &m.tools)
	m.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = m.server.Serve(ln) }()
	return m, nil
}

// recover ends turns that were in progress when a previous run stopped.
func (m *Manager) recover(ctx context.Context) error {
	agents, err := m.cfg.Store.List(ctx, Filter{})
	if err != nil {
		return err
	}
	for _, a := range agents {
		if !a.State.Busy() {
			continue
		}
		m.close(ctx, a, transcript.StatusError, interrupted)
		a.State, a.Error, a.UpdatedAt = StateIdle, interrupted, now()
		if err := m.cfg.Store.Put(ctx, a); err != nil {
			return err
		}
	}
	return nil
}

// Close cancels running turns and stops the tool server.
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	for _, l := range m.turns {
		l.cancel(errShutdown)
	}
	m.mu.Unlock()
	m.wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.server.Shutdown(ctx)
}

func now() time.Time {
	return time.Now().UTC()
}

func newID(n int) string {
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// Create prepares the workspace and stores a new agent.
func (m *Manager) Create(ctx context.Context, spec Spec) (Agent, error) {
	a, err := m.check(ctx, spec)
	if err != nil {
		return Agent{}, err
	}
	if err := m.reserve(ctx); err != nil {
		return Agent{}, err
	}
	defer m.unreserve()
	w, err := m.cfg.Workspaces.Prepare(ctx, a.ID, spec.Workspace)
	if err != nil {
		return Agent{}, err
	}
	a.Workspace = w
	if err := m.cfg.Store.Put(ctx, a); err != nil {
		_ = m.cfg.Workspaces.Release(context.WithoutCancel(ctx), w, true)
		return Agent{}, err
	}
	return a, nil
}

func (m *Manager) check(ctx context.Context, spec Spec) (Agent, error) {
	client, ok := m.cfg.Clients[spec.Client]
	if !ok {
		return Agent{}, fmt.Errorf("%w: unknown client %q", ErrInvalid, spec.Client)
	}
	if spec.Policy.Mode == "" {
		spec.Policy.Mode = adapter.ModeReadOnly
	}
	if err := client.Adapter.Check(spec.Model, spec.Effort, spec.Policy); err != nil {
		return Agent{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	limits, err := m.cfg.Bounds.resolve(spec.Limits)
	if err != nil {
		return Agent{}, err
	}
	if spec.Parent != "" {
		if _, err := m.cfg.Store.Get(ctx, spec.Parent); errors.Is(err, ErrNotFound) {
			return Agent{}, fmt.Errorf("%w: parent agent %q not found", ErrInvalid, spec.Parent)
		} else if err != nil {
			return Agent{}, err
		}
	}
	t := now()
	return Agent{
		ID: newID(16), Client: spec.Client, Model: spec.Model, Effort: spec.Effort,
		Policy: spec.Policy, Limits: limits, Parent: spec.Parent, State: StateCreated,
		CreatedAt: t, UpdatedAt: t,
	}, nil
}

// reserve holds a place for a new agent while its workspace is prepared.
func (m *Manager) reserve(ctx context.Context) error {
	agents, err := m.cfg.Store.List(ctx, Filter{})
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("%w: shutting down", ErrState)
	}
	if len(agents)+m.creating >= m.cfg.Bounds.MaxAgents {
		return fmt.Errorf("%w: at most %d agents", ErrLimit, m.cfg.Bounds.MaxAgents)
	}
	m.creating++
	return nil
}

func (m *Manager) unreserve() {
	m.mu.Lock()
	m.creating--
	m.mu.Unlock()
}

// Get returns an agent.
func (m *Manager) Get(ctx context.Context, id string) (Agent, error) {
	return m.cfg.Store.Get(ctx, id)
}

// List returns matching agents.
func (m *Manager) List(ctx context.Context, f Filter) ([]Agent, error) {
	return m.cfg.Store.List(ctx, f)
}

// update changes a stored agent under the manager lock.
func (m *Manager) update(ctx context.Context, id string, change func(*Agent) error) (Agent, error) {
	a, err := m.cfg.Store.Get(ctx, id)
	if err != nil {
		return Agent{}, err
	}
	if err := change(&a); err != nil {
		return Agent{}, err
	}
	a.UpdatedAt = now()
	if err := m.cfg.Store.Put(ctx, a); err != nil {
		return Agent{}, err
	}
	m.signal(id)
	return a, nil
}

// Cancel ends the running turn and waits until the agent is idle.
func (m *Manager) Cancel(ctx context.Context, id string) (Agent, error) {
	return m.interrupt(ctx, id, errCancelled, false)
}

// Stop ends any running turn and stops the agent for good.
func (m *Manager) Stop(ctx context.Context, id string) (Agent, error) {
	return m.interrupt(ctx, id, errStopped, true)
}

func (m *Manager) interrupt(ctx context.Context, id string, cause error, stop bool) (Agent, error) {
	m.mu.Lock()
	l := m.turns[id]
	if l == nil {
		defer m.mu.Unlock()
		return m.update(ctx, id, func(a *Agent) error {
			switch {
			case stop && !a.State.Terminal():
				a.State = StateStopped
			case !stop:
				return fmt.Errorf("%w: no turn is running", ErrState)
			}
			return nil
		})
	}
	l.cancel(cause)
	m.mu.Unlock()
	select {
	case <-l.done:
	case <-ctx.Done():
		return Agent{}, ctx.Err()
	}
	return m.cfg.Store.Get(ctx, id)
}

// Delete stops an agent, releases its workspace and removes it. A
// checkout workspace is removed only when removeCheckout is set; if that
// fails the agent is kept, stopped.
func (m *Manager) Delete(ctx context.Context, id string, removeCheckout bool) error {
	a, err := m.Stop(ctx, id)
	if err != nil {
		return err
	}
	if err := m.cfg.Workspaces.Release(ctx, a.Workspace, removeCheckout); err != nil {
		return fmt.Errorf("release workspace: %w", err)
	}
	if err := m.cfg.Store.Delete(ctx, id); err != nil {
		return err
	}
	m.signal(id)
	m.nmu.Lock()
	delete(m.notify, id)
	m.nmu.Unlock()
	return nil
}

// signal wakes watchers of an agent.
func (m *Manager) signal(id string) {
	m.nmu.Lock()
	defer m.nmu.Unlock()
	if ch, ok := m.notify[id]; ok {
		close(ch)
		delete(m.notify, id)
	}
}

// changed returns a channel closed at the agent's next change.
func (m *Manager) changed(id string) <-chan struct{} {
	m.nmu.Lock()
	defer m.nmu.Unlock()
	ch, ok := m.notify[id]
	if !ok {
		ch = make(chan struct{})
		m.notify[id] = ch
	}
	return ch
}

// append records an event of the agent's current turn. Ordinary events
// leave room for the closing ones; closing events may use it.
func (m *Manager) append(ctx context.Context, a Agent, e transcript.Event, closingEvent bool) error {
	limit := a.Limits.MaxEvents - closing
	if closingEvent {
		limit = a.Limits.MaxEvents
	}
	e.Time = now()
	e.Turn = a.Turns
	e = e.Truncate(m.cfg.Bounds.MaxEventBytes)
	if _, err := m.cfg.Store.Append(ctx, a.ID, e, limit); err != nil {
		return err
	}
	m.signal(a.ID)
	return nil
}

// close ends a turn visibly with an error, when there is one, and a
// result.
func (m *Manager) close(ctx context.Context, a Agent, status, message string) {
	if message != "" {
		_ = m.append(ctx, a, transcript.Event{Kind: transcript.KindError, Text: message}, true)
	}
	_ = m.append(ctx, a, transcript.Event{Kind: transcript.KindResult, Status: status}, true)
}
