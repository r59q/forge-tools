package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/adapter/claude"
	"github.com/r59q/forge-tools/forge-agents/library/adapter/codex"
	"github.com/r59q/forge-tools/forge-agents/library/adapter/gemini"
	"github.com/r59q/forge-tools/forge-agents/library/agent"
	"github.com/r59q/forge-tools/forge-agents/library/executor"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
	"github.com/r59q/forge-tools/forge-agents/library/workspace"
)

type fakeCheckouts struct {
	dir     string
	removed []string
}

func (f *fakeCheckouts) Create(context.Context, workspace.Repository, string) (workspace.Checkout, error) {
	return workspace.Checkout{ID: "0123456789abcdef0123456789abcdef", Path: f.dir, Reference: "main"}, nil
}

func (f *fakeCheckouts) Remove(_ context.Context, id string) error {
	f.removed = append(f.removed, id)
	return nil
}

type harness struct {
	*agent.Manager
	store     *agent.MemoryStore
	scripts   string
	scratch   string
	checkouts *fakeCheckouts
}

func start(t *testing.T, bounds agent.Bounds, env ...string) *harness {
	t.Helper()
	return startWith(t, agent.NewMemoryStore(), bounds, env...)
}

func startWith(t *testing.T, store *agent.MemoryStore, bounds agent.Bounds, env ...string) *harness {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{store: store, scripts: t.TempDir(), scratch: t.TempDir(), checkouts: &fakeCheckouts{dir: t.TempDir()}}
	m, err := agent.Open(t.Context(), agent.Config{
		Store: store,
		Clients: map[string]agent.Client{
			claude.Name: {Adapter: claude.Adapter{}, Path: exe},
			codex.Name:  {Adapter: codex.Adapter{}, Path: exe},
			gemini.Name: {Adapter: gemini.Adapter{}, Path: exe},
		},
		Executor:   executor.Local{},
		Workspaces: workspace.Local{ScratchDir: h.scratch, Checkouts: h.checkouts},
		Env:        append([]string{fakeEnv + "=1", scriptsEnv + "=" + h.scripts}, env...),
		RunDir:     t.TempDir(),
		Bounds:     bounds,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	h.Manager = m
	return h
}

// script sets the output of turn n.
func (h *harness) script(t *testing.T, n int, lines ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.scripts, strconv.Itoa(n)+".jsonl"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) fixture(t *testing.T, n int, client, name string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "adapter", "testdata", client, name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	h.script(t, n, string(data))
}

func (h *harness) create(t *testing.T, spec agent.Spec) agent.Agent {
	t.Helper()
	a, err := h.Create(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (h *harness) send(t *testing.T, id, text string) {
	t.Helper()
	if _, err := h.Send(t.Context(), id, text); err != nil {
		t.Fatal(err)
	}
}

// wait polls until the agent is in one of states.
func (h *harness) wait(t *testing.T, id string, states ...agent.State) agent.Agent {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		a, err := h.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(states, a.State) {
			return a
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent is %s, want %v", a.State, states)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) events(t *testing.T, id string) []transcript.Event {
	t.Helper()
	events, err := h.Transcript(t.Context(), id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func kinds(events []transcript.Event) string {
	var k []string
	for _, e := range events {
		k = append(k, string(e.Kind))
	}
	return strings.Join(k, " ")
}

func last(events []transcript.Event) transcript.Event {
	return events[len(events)-1]
}

func claudeText(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "assistant", "session_id": "s1",
		"message": map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}})
	return string(b)
}

const claudeResult = `{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s1"}`

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTurnsResumeEachClient(t *testing.T) {
	cases := []struct {
		client, fixture, resume string
		policy                  adapter.Policy
	}{
		{claude.Name, "turn", "--resume\n6f1c2a9e-3b7d-4e8a-9c1f-2d5b8a7e4c10", adapter.Policy{Mode: adapter.ModeEdit, Ask: true}},
		{codex.Name, "turn", "0199a213-81c0-7800-8aa1-bbab2a035a53\n-", adapter.Policy{Mode: adapter.ModeEdit}},
		{gemini.Name, "turn", "--resume\nc3d4e5f6-a7b8-4c9d-8e0f-1a2b3c4d5e6f", adapter.Policy{Mode: adapter.ModeReadOnly}},
	}
	for _, c := range cases {
		t.Run(c.client, func(t *testing.T) {
			h := start(t, agent.Bounds{})
			h.fixture(t, 1, c.client, c.fixture)
			h.fixture(t, 2, c.client, c.fixture)
			a := h.create(t, agent.Spec{Client: c.client, Policy: c.policy})
			if a.State != agent.StateCreated || a.Workspace.Kind != workspace.KindScratch {
				t.Fatalf("created %+v", a)
			}
			h.send(t, a.ID, "How many tests are there?")
			a = h.wait(t, a.ID, agent.StateIdle)
			if a.Session == "" || a.Turns != 1 || a.Error != "" {
				t.Fatalf("after turn 1: %+v", a)
			}
			events := h.events(t, a.ID)
			if events[0].Kind != transcript.KindMessage || events[0].Role != transcript.RoleUser || last(events).Kind != transcript.KindResult {
				t.Fatalf("events: %s", kinds(events))
			}
			for i, e := range events {
				if e.Seq != int64(i+1) || e.Turn != 1 || e.Time.IsZero() {
					t.Fatalf("event %d: %+v", i, e)
				}
			}
			h.send(t, a.ID, "And now?")
			h.wait(t, a.ID, agent.StateIdle)
			if args := read(t, filepath.Join(a.Workspace.Path, "args-2")); !strings.Contains(args, c.resume) {
				t.Errorf("turn 2 does not resume:\n%s", args)
			}
			if prompt := read(t, filepath.Join(a.Workspace.Path, "prompt-2")); prompt != "And now?" {
				t.Errorf("prompt = %q", prompt)
			}
			out, err := h.Output(t.Context(), a.ID, 2)
			if err != nil || len(out.Stdout) == 0 {
				t.Errorf("output = %q, %v", out.Stdout, err)
			}
		})
	}
}

func TestAskUser(t *testing.T) {
	h := start(t, agent.Bounds{})
	h.script(t, 1, `{"fake":"ask","question":"Which branch?"}`, claudeText("ok"), claudeResult)
	a := h.create(t, agent.Spec{Client: claude.Name})
	h.send(t, a.ID, "go")
	h.wait(t, a.ID, agent.StateWaitingInput)
	request := last(h.events(t, a.ID))
	if request.Kind != transcript.KindInputRequest || request.RequestKind != transcript.RequestQuestion || request.Text != "Which branch?" {
		t.Fatalf("request = %+v", request)
	}
	if _, err := h.Answer(t.Context(), a.ID, "nope", "main", false); !errors.Is(err, agent.ErrNotFound) {
		t.Errorf("unknown request: %v", err)
	}
	if _, err := h.Answer(t.Context(), a.ID, request.RequestID, "main", false); err != nil {
		t.Fatal(err)
	}
	h.wait(t, a.ID, agent.StateIdle)
	if got := read(t, filepath.Join(a.Workspace.Path, "answers")); got != "main\n" {
		t.Errorf("client got %q", got)
	}
	if k := kinds(h.events(t, a.ID)); k != "message input_request input_answer message result" {
		t.Errorf("events: %s", k)
	}
}

func TestApprove(t *testing.T) {
	h := start(t, agent.Bounds{})
	h.script(t, 1, `{"fake":"approve","tool":"Bash","input":{"command":"rm -rf build"}}`, claudeResult)
	a := h.create(t, agent.Spec{Client: claude.Name, Policy: adapter.Policy{Mode: adapter.ModeEdit, Ask: true}})
	h.send(t, a.ID, "clean up")
	h.wait(t, a.ID, agent.StateWaitingInput)
	request := last(h.events(t, a.ID))
	if request.RequestKind != transcript.RequestPermission || request.ToolName != "Bash" || request.Input != `{"command":"rm -rf build"}` {
		t.Fatalf("request = %+v", request)
	}
	if _, err := h.Answer(t.Context(), a.ID, request.RequestID, "not now", false); err != nil {
		t.Fatal(err)
	}
	h.wait(t, a.ID, agent.StateIdle)
	if got := read(t, filepath.Join(a.Workspace.Path, "answers")); got != `{"behavior":"deny","message":"not now"}`+"\n" {
		t.Errorf("client got %q", got)
	}
}

func TestApproveNeedsAsk(t *testing.T) {
	h := start(t, agent.Bounds{})
	h.script(t, 1, `{"fake":"approve","tool":"Bash","input":{}}`, claudeResult)
	a := h.create(t, agent.Spec{Client: claude.Name})
	h.send(t, a.ID, "go")
	h.wait(t, a.ID, agent.StateIdle)
	if got := read(t, filepath.Join(a.Workspace.Path, "answers")); !strings.HasPrefix(got, "error: unknown tool") {
		t.Errorf("client got %q", got)
	}
}

func TestCancelAndTimeout(t *testing.T) {
	h := start(t, agent.Bounds{})
	h.script(t, 1, `{"fake":"sleep","ms":30000}`)
	h.script(t, 2, `{"fake":"sleep","ms":30000}`)
	a := h.create(t, agent.Spec{Client: codex.Name, Limits: agent.Limits{TurnTimeout: time.Minute}})
	h.send(t, a.ID, "wait")
	if _, err := h.Send(t.Context(), a.ID, "again"); !errors.Is(err, agent.ErrState) {
		t.Errorf("send while running: %v", err)
	}
	started := time.Now()
	a, err := h.Cancel(t.Context(), a.ID)
	if err != nil || a.State != agent.StateIdle || time.Since(started) > 10*time.Second {
		t.Fatalf("cancel: %+v %v after %s", a, err, time.Since(started))
	}
	if e := last(h.events(t, a.ID)); e.Kind != transcript.KindResult || e.Status != transcript.StatusCancelled {
		t.Errorf("last event = %+v", e)
	}
	if _, err := h.Cancel(t.Context(), a.ID); !errors.Is(err, agent.ErrState) {
		t.Errorf("cancel while idle: %v", err)
	}

	b := h.create(t, agent.Spec{Client: codex.Name, Limits: agent.Limits{TurnTimeout: 300 * time.Millisecond}})
	h.send(t, b.ID, "wait")
	b = h.wait(t, b.ID, agent.StateIdle)
	events := h.events(t, b.ID)
	if b.Error != "turn timed out" || last(events).Status != transcript.StatusTimeout || events[len(events)-2].Kind != transcript.KindError {
		t.Errorf("timeout: %+v %s", b, kinds(events))
	}
}

func TestStopAndDelete(t *testing.T) {
	h := start(t, agent.Bounds{})
	h.script(t, 1, `{"fake":"sleep","ms":30000}`)
	a := h.create(t, agent.Spec{Client: gemini.Name})
	h.send(t, a.ID, "wait")
	a, err := h.Stop(t.Context(), a.ID)
	if err != nil || a.State != agent.StateStopped {
		t.Fatalf("stop: %+v %v", a, err)
	}
	if last(h.events(t, a.ID)).Status != transcript.StatusStopped {
		t.Errorf("events: %s", kinds(h.events(t, a.ID)))
	}
	if _, err := h.Send(t.Context(), a.ID, "more"); !errors.Is(err, agent.ErrState) {
		t.Errorf("send after stop: %v", err)
	}
	if err := h.Delete(t.Context(), a.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Workspace.Path); !os.IsNotExist(err) {
		t.Errorf("scratch kept: %v", err)
	}
	if _, err := h.Get(t.Context(), a.ID); !errors.Is(err, agent.ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
}

func TestCheckoutWorkspace(t *testing.T) {
	h := start(t, agent.Bounds{})
	repo := workspace.Repository{Source: "github", Path: "r59q/forge-tools"}
	spec := agent.Spec{Client: claude.Name, Workspace: workspace.Spec{Kind: workspace.KindCheckout, Repository: repo}}
	kept, removed := h.create(t, spec), h.create(t, spec)
	if kept.Workspace.Checkout == "" || kept.Workspace.Path != h.checkouts.dir {
		t.Fatalf("workspace = %+v", kept.Workspace)
	}
	if list, _ := h.List(t.Context(), agent.Filter{Repository: repo}); len(list) != 2 {
		t.Errorf("list by repository = %d", len(list))
	}
	if err := h.Delete(t.Context(), kept.ID, false); err != nil || len(h.checkouts.removed) != 0 {
		t.Fatalf("delete keeping checkout: %v %v", err, h.checkouts.removed)
	}
	if err := h.Delete(t.Context(), removed.ID, true); err != nil || len(h.checkouts.removed) != 1 {
		t.Fatalf("delete removing checkout: %v %v", err, h.checkouts.removed)
	}
	if _, err := os.Stat(h.checkouts.dir); err != nil {
		t.Errorf("checkout directory touched: %v", err)
	}
}
