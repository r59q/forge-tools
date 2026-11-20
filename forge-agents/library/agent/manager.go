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
	cancel    context.CancelCauseFunc
	done      chan struct{}
	stopping  bool
	cancelled <-chan struct{}
	root      string
	pending   map[string]pendingInput
}

// Manager runs agents.
type Manager struct {
	cfg      Config
	tools    mcp.Server
	toolsURL string
	server   *http.Server

	mu       sync.Mutex
	turns    map[string]*live
	creating map[string]Agent
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
	m := &Manager{cfg: cfg, turns: map[string]*live{}, creating: map[string]Agent{}, notify: map[string]chan struct{}{}}
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
	m.mu.Lock()
	m.resumeParents(ctx)
	m.mu.Unlock()
	return m, nil
}

// recover ends turns that were in progress when a previous run stopped.
func (m *Manager) recover(ctx context.Context) error {
	agents, err := m.cfg.Store.List(ctx, Filter{})
	if err != nil {
		return err
	}
	if err := m.recoverLinks(ctx, agents); err != nil {
		return err
	}
	for _, a := range agents {
		if !a.State.Busy() {
			continue
		}
		a, err := m.finish(ctx, a, outcome{state: StateIdle, status: transcript.StatusError, message: interrupted})
		if err != nil {
			return err
		}
		if parent, err := m.cfg.Store.Get(ctx, a.Parent); err == nil {
			m.childFinished(ctx, parent, a)
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
	if a.Parent != "" {
		m.signal(a.Parent)
	}
	return a, nil
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
	_ = m.append(ctx, a, transcript.Event{Kind: transcript.KindResult, Status: status, Text: message}, true)
}
