package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/executor"
	"github.com/r59q/forge-tools/forge-agents/library/mcp"
	"github.com/r59q/forge-tools/forge-agents/library/redact"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
)

// Send records a user message and starts a turn answering it.
func (m *Manager) Send(ctx context.Context, id, text string) (Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, err := m.cfg.Store.Get(ctx, id)
	if err != nil {
		return Agent{}, err
	}
	if a.State != StateCreated && a.State != StateIdle {
		return Agent{}, fmt.Errorf("%w: agent is %s", ErrState, a.State)
	}
	started, err := m.startTurn(ctx, a, text)
	if err != nil && started.State == StateFailed {
		m.afterTurn(ctx, started)
	}
	return started, err
}

// startTurn starts an admitted agent while m.mu is held.
func (m *Manager) startTurn(ctx context.Context, a Agent, text string) (Agent, error) {
	if strings.TrimSpace(text) == "" || len(text) > m.cfg.Bounds.MaxEventBytes {
		return Agent{}, fmt.Errorf("%w: a message needs 1 to %d bytes", ErrInvalid, m.cfg.Bounds.MaxEventBytes)
	}
	if err := m.turnSlot(); err != nil {
		return Agent{}, err
	}
	root, err := m.treeBudget(ctx, a)
	if err != nil {
		return Agent{}, err
	}
	a.Turns++
	err = m.append(ctx, a, transcript.Event{Kind: transcript.KindMessage, Role: transcript.RoleUser, Text: text}, false)
	if errors.Is(err, ErrTranscriptFull) {
		failed := m.failTurn(ctx, a, errFull.Error())
		return failed, fmt.Errorf("%w: %w", ErrLimit, errFull)
	} else if err != nil {
		return Agent{}, err
	}
	a, err = m.update(ctx, a.ID, func(s *Agent) error {
		s.State, s.Turns, s.Error = StateRunning, a.Turns, ""
		return nil
	})
	if err != nil {
		return Agent{}, err
	}
	// The turn outlives the request that started it.
	turnCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	l := &live{
		root: a.Root, cancel: cancel, cancelled: turnCtx.Done(),
		done: make(chan struct{}), pending: map[string]pendingInput{},
	}
	m.turns[a.ID] = l
	m.wg.Add(1)
	go m.run(turnCtx, a, text, l, root.TreeDeadline)
	return a, nil
}

func (m *Manager) turnSlot() error {
	switch {
	case m.closed:
		return fmt.Errorf("%w: shutting down", ErrState)
	case len(m.turns) >= m.cfg.Bounds.MaxRunning:
		return fmt.Errorf("%w: at most %d turns run at once", ErrLimit, m.cfg.Bounds.MaxRunning)
	default:
		return nil
	}
}

// outcome is how a turn ended.
type outcome struct {
	status  string
	message string
	session string
	state   State
	result  *transcript.Event
}

func (m *Manager) run(ctx context.Context, a Agent, prompt string, l *live, deadline time.Time) {
	defer m.wg.Done()
	defer close(l.done)
	defer l.cancel(nil)
	timeout, cancel := context.WithDeadlineCause(ctx, deadline, errTreeTime)
	defer cancel()
	timeout, cancelTurn := context.WithTimeout(timeout, a.Limits.TurnTimeout)
	defer cancelTurn()
	// Bookkeeping after the turn must survive its cancellation.
	store := context.WithoutCancel(ctx)
	o := m.execute(timeout, store, a, prompt, l)
	m.mu.Lock()
	defer m.mu.Unlock()
	if l.stopping {
		o.state, o.status, o.message = StateStopped, transcript.StatusStopped, ""
	} else if o.state == StateIdle && ctx.Err() == nil && !m.closed && m.hasWorkingChildren(store, a.ID) {
		o.state = StateWaitingChildren
	}
	finished, err := m.finish(store, a, o)
	delete(m.turns, a.ID)
	if err == nil {
		m.afterTurn(store, finished)
	}
}

func (m *Manager) failTurn(ctx context.Context, a Agent, message string) Agent {
	a, _ = m.finish(ctx, a, outcome{state: StateFailed, status: transcript.StatusLimit, message: message})
	return a
}

func (m *Manager) finish(ctx context.Context, a Agent, o outcome) (Agent, error) {
	result := o.message
	if o.result == nil || o.result.Status != o.status {
		m.close(ctx, a, o.status, o.message)
	} else {
		result = o.result.Text
		_ = m.append(ctx, a, *o.result, true)
	}
	return m.update(ctx, a.ID, func(s *Agent) error {
		s.State, s.Error, s.Turns = o.state, "", a.Turns
		s.Result, s.ResultStatus = shortResult(result), o.status
		if o.status != transcript.StatusSuccess {
			s.Error = o.message
			if s.Error == "" {
				s.Error = "turn ended: " + o.status
			}
		}
		if o.session != "" {
			s.Session = o.session
		}
		return nil
	})
}

// execute runs one client invocation. ctx ends the turn; store outlives
// it for bookkeeping.
func (m *Manager) execute(ctx, store context.Context, a Agent, prompt string, l *live) outcome {
	client := m.cfg.Clients[a.Client]
	runDir := filepath.Join(m.cfg.RunDir, a.ID+"-"+strconv.Itoa(a.Turns))
	defer func() { _ = os.RemoveAll(runDir) }()
	token, unregister := m.tools.RegisterFor(func(token string) mcp.Binding {
		return m.binding(ctx, a, l, redact.FromEnv(m.cfg.Env, token))
	})
	defer unregister()
	inv, err := client.Adapter.Command(adapter.Turn{
		Session: a.Session, Model: a.Model, Effort: a.Effort, Policy: a.Policy, RunDir: runDir,
		Tools: adapter.Tools{URL: m.toolsURL, Token: token, Timeout: a.Limits.TurnTimeout},
	})
	if err == nil {
		err = writeFiles(runDir, inv.Files)
	}
	if err != nil {
		return outcome{status: transcript.StatusError, message: err.Error(), state: StateIdle}
	}
	env := slices.Concat(m.cfg.Env, inv.Env)
	r := &reader{m: m, ctx: store, agent: a, stream: &adapter.Stream{}, redactor: redact.FromEnv(env, token)}
	r.raw.limit = m.cfg.Bounds.MaxOutputBytes
	exit, err := m.cfg.Executor.Run(ctx, executor.Command{
		Path: client.Path, Args: inv.Args, Dir: a.Workspace.Path, Env: env, Stdin: []byte(prompt),
	}, func(out io.Reader) {
		if r.read(out) {
			l.cancel(errFull)
		}
	})
	r.flush()
	stderr := r.redactor.Bytes(exit.Stderr)
	output := Output{Stdout: r.raw.Bytes(), Stderr: stderr, Truncated: r.raw.truncated}
	_ = m.cfg.Store.PutOutput(store, a.ID, a.Turns, output, m.cfg.Bounds.KeepOutputs)
	o := outcome{session: r.stream.Session(), state: StateIdle, result: r.result}
	o.status, o.message = m.ending(ctx, err, exit, stderr, r.stream.HasResult())
	if o.status == transcript.StatusSuccess && r.result != nil {
		o.status = r.result.Status
		if o.status != transcript.StatusSuccess {
			o.message = r.result.Text
		}
	}
	switch o.status {
	case transcript.StatusStopped:
		o.state = StateStopped
	case transcript.StatusLimit:
		o.state = StateFailed
	}
	return o
}

// ending is the status of a finished turn, and a message when it did not
// end with the client's own result.
func (m *Manager) ending(ctx context.Context, err error, exit executor.Exit, stderr []byte, result bool) (string, string) {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errCancelled):
		return transcript.StatusCancelled, ""
	case errors.Is(cause, errStopped):
		return transcript.StatusStopped, ""
	case errors.Is(cause, errFull), errors.Is(cause, errTreeTokens):
		return transcript.StatusLimit, cause.Error()
	case errors.Is(cause, errShutdown):
		return transcript.StatusError, errShutdown.Error()
	case errors.Is(cause, errTreeTime):
		return transcript.StatusTimeout, cause.Error()
	case errors.Is(cause, context.DeadlineExceeded):
		return transcript.StatusTimeout, "turn timed out"
	case err != nil:
		return transcript.StatusError, err.Error()
	case !result:
		message := fmt.Sprintf("client exited with code %d without a result", exit.Code)
		if tail := strings.TrimSpace(string(stderr)); tail != "" {
			message += ": " + lastLines(tail, 5)
		}
		return transcript.StatusError, message
	}
	return transcript.StatusSuccess, ""
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

func writeFiles(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("run directory: %w", err)
	}
	for name, data := range files {
		if filepath.Base(name) != name {
			return fmt.Errorf("%w: run file %q", ErrInvalid, name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return fmt.Errorf("run file: %w", err)
		}
	}
	return nil
}

// reader turns client output into transcript events and bounded raw
// output.
type reader struct {
	m        *Manager
	ctx      context.Context //nolint:containedctx // a reader lives for one turn.
	agent    Agent
	stream   *adapter.Stream
	redactor redact.Redactor
	raw      bounded
	full     bool
	result   *transcript.Event
}

// read consumes the output and reports whether the transcript filled up.
func (r *reader) read(out io.Reader) bool {
	br := bufio.NewReaderSize(out, 64<<10)
	for !r.full {
		line, long, err := readLine(br, r.m.cfg.Bounds.MaxLineBytes)
		if len(line) > 0 || long {
			r.line(line, long)
		}
		if err != nil {
			break
		}
	}
	return r.full
}

func (r *reader) line(line []byte, long bool) {
	line = r.redactor.Bytes(line)
	r.raw.write(line)
	r.raw.write([]byte("\n"))
	if long {
		r.emit(transcript.Event{Kind: transcript.KindError, Text: fmt.Sprintf(
			"client output line longer than %d bytes was not parsed", r.m.cfg.Bounds.MaxLineBytes)})
		return
	}
	if r.m.cfg.Clients[r.agent.Client].Adapter.Parse(r.stream, line) == nil {
		r.emit()
	}
}

// flush emits a buffered message once output ends.
func (r *reader) flush() {
	r.stream.Close()
	r.emit()
}

func (r *reader) emit(extra ...transcript.Event) {
	for _, e := range append(r.stream.Take(), extra...) {
		if e.Usage != nil {
			r.m.chargeUsage(r.ctx, r.agent.Root, *e.Usage)
		}
		if e.Kind == transcript.KindResult {
			e = e.Truncate(r.m.cfg.Bounds.MaxEventBytes)
			r.result = &e
			continue
		}
		if r.full {
			return
		}
		if err := r.m.append(r.ctx, r.agent, e, false); errors.Is(err, ErrTranscriptFull) {
			r.full = true
		}
	}
}

// readLine reads one line without its newline. A line longer than limit
// is cut to limit bytes and reported as long.
func readLine(br *bufio.Reader, limit int) ([]byte, bool, error) {
	var line []byte
	long := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !long {
			if len(line)+len(chunk) > limit+1 {
				long, line = true, append(line, chunk[:max(0, min(len(chunk), limit-len(line)))]...)
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimRight(line, "\r\n"), long, err
	}
}

// bounded keeps the first limit bytes written to it.
type bounded struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *bounded) write(p []byte) {
	room := b.limit - b.Len()
	if len(p) > room {
		p, b.truncated = p[:max(room, 0)], true
	}
	_, _ = b.Write(p)
}
