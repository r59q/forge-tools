package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/adapter/claude"
	"github.com/r59q/forge-tools/forge-agents/library/adapter/codex"
	"github.com/r59q/forge-tools/forge-agents/library/agent"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
	"github.com/r59q/forge-tools/forge-agents/library/workspace"
)

func TestTranscriptLimit(t *testing.T) {
	h := start(t, agent.Bounds{})
	h.script(t, 1, `{"fake":"repeat","count":50,"line":`+quote(claudeText("again"))+`}`, `{"fake":"sleep","ms":30000}`)
	a := h.create(t, agent.Spec{Client: claude.Name, Limits: agent.Limits{MaxEvents: 16}})
	h.send(t, a.ID, "talk")
	a = h.wait(t, a.ID, agent.StateFailed)
	events := h.events(t, a.ID)
	if len(events) != 16 || last(events).Status != transcript.StatusLimit || events[14].Kind != transcript.KindError {
		t.Fatalf("%d events: %s", len(events), kinds(events))
	}
	if _, err := h.Send(t.Context(), a.ID, "more"); !errors.Is(err, agent.ErrState) {
		t.Errorf("send after failure: %v", err)
	}
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func TestAgentAndTurnLimits(t *testing.T) {
	h := start(t, agent.Bounds{MaxAgents: 2, MaxRunning: 1})
	h.script(t, 1, `{"fake":"sleep","ms":30000}`)
	a, b := h.create(t, agent.Spec{Client: codex.Name}), h.create(t, agent.Spec{Client: codex.Name})
	if _, err := h.Create(t.Context(), agent.Spec{Client: codex.Name}); !errors.Is(err, agent.ErrLimit) {
		t.Errorf("third agent: %v", err)
	}
	h.send(t, a.ID, "wait")
	if _, err := h.Send(t.Context(), b.ID, "wait"); !errors.Is(err, agent.ErrLimit) {
		t.Errorf("second turn: %v", err)
	}
	if _, err := h.Cancel(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	h.send(t, b.ID, "wait")
}

func TestInvalidSpecs(t *testing.T) {
	h := start(t, agent.Bounds{TurnTimeout: time.Minute})
	for name, spec := range map[string]agent.Spec{
		"client":    {Client: "cursor"},
		"model":     {Client: claude.Name, Model: "--dangerously-skip-permissions"},
		"effort":    {Client: codex.Name, Effort: "ultra"},
		"ask":       {Client: codex.Name, Policy: adapter.Policy{Mode: adapter.ModeEdit, Ask: true}},
		"timeout":   {Client: claude.Name, Limits: agent.Limits{TurnTimeout: time.Hour}},
		"events":    {Client: claude.Name, Limits: agent.Limits{MaxEvents: 3}},
		"parent":    {Client: claude.Name, Parent: "missing"},
		"workspace": {Client: claude.Name, Workspace: workspace.Spec{Kind: workspace.KindPath, Path: "/"}},
	} {
		if _, err := h.Create(t.Context(), spec); err == nil {
			t.Errorf("%s: created", name)
		}
	}
	a := h.create(t, agent.Spec{Client: claude.Name})
	if a.Policy.Mode != adapter.ModeReadOnly || a.Limits.TurnTimeout != time.Minute {
		t.Errorf("defaults: %+v %+v", a.Policy, a.Limits)
	}
	child := h.create(t, agent.Spec{Client: claude.Name, Parent: a.ID})
	if list, _ := h.List(t.Context(), agent.Filter{Parent: a.ID}); len(list) != 1 || list[0].ID != child.ID {
		t.Errorf("children = %+v", list)
	}
	if _, err := h.Send(t.Context(), a.ID, " "); !errors.Is(err, agent.ErrInvalid) {
		t.Errorf("empty message: %v", err)
	}
}

func TestRecoverInterruptedTurns(t *testing.T) {
	store := agent.NewMemoryStore()
	ctx := t.Context()
	stale := agent.Agent{ID: "a1", Client: claude.Name, State: agent.StateWaitingInput, Turns: 3, Limits: agent.Limits{MaxEvents: 100}}
	if err := store.Put(ctx, stale); err != nil {
		t.Fatal(err)
	}
	h := startWith(t, store, agent.Bounds{})
	a, err := h.Get(ctx, "a1")
	if err != nil || a.State != agent.StateIdle || !strings.Contains(a.Error, "restarted") {
		t.Fatalf("recovered %+v %v", a, err)
	}
	if k := kinds(h.events(t, "a1")); k != "error result" {
		t.Errorf("events: %s", k)
	}
}

func TestWatch(t *testing.T) {
	h := start(t, agent.Bounds{})
	h.script(t, 1, `{"fake":"sleep","ms":200}`, claudeText("one"), `{"fake":"sleep","ms":200}`, claudeText("two"), claudeResult)
	a := h.create(t, agent.Spec{Client: claude.Name})
	var (
		mu      sync.Mutex
		watched []transcript.Event
		done    = make(chan error, 1)
	)
	go func() {
		done <- h.Watch(context.Background(), a.ID, 0, func(e transcript.Event) error {
			mu.Lock()
			defer mu.Unlock()
			watched = append(watched, e)
			return nil
		})
	}()
	h.send(t, a.ID, "count")
	h.wait(t, a.ID, agent.StateIdle)
	if _, err := h.Stop(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watch did not end after stop")
	}
	if got, want := kinds(watched), kinds(h.events(t, a.ID)); got != want || got != "message message message result" {
		t.Errorf("watched %s, transcript %s", got, want)
	}
	late, err := h.Transcript(t.Context(), a.ID, 2, 1)
	if err != nil || len(late) != 1 || late[0].Seq != 3 {
		t.Errorf("page after 2: %+v %v", late, err)
	}
}

func TestSecretsStayOut(t *testing.T) {
	const secret = "sk-ant-test-0123456789"
	h := start(t, agent.Bounds{}, "ANTHROPIC_API_KEY="+secret)
	h.script(t, 1, claudeText("the key is "+secret), `{"fake":"stderr","text":"auth `+secret+`"}`, `{"fake":"exit","code":1}`)
	a := h.create(t, agent.Spec{Client: claude.Name})
	h.send(t, a.ID, "leak it")
	a = h.wait(t, a.ID, agent.StateIdle)
	out, err := h.Output(t.Context(), a.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	everything := string(out.Stdout) + string(out.Stderr) + a.Error
	for _, e := range h.events(t, a.ID) {
		everything += e.Text
	}
	if strings.Contains(everything, secret) || !strings.Contains(everything, "the key is [redacted]") {
		t.Errorf("secret leaked or not masked:\n%s", everything)
	}
	if !strings.Contains(a.Error, "code 1 without a result") {
		t.Errorf("error = %q", a.Error)
	}
}

func TestLongLine(t *testing.T) {
	h := start(t, agent.Bounds{MaxLineBytes: 1024, MaxOutputBytes: 512})
	h.script(t, 1, `{"fake":"long","count":5000}`, claudeResult)
	a := h.create(t, agent.Spec{Client: claude.Name})
	h.send(t, a.ID, "go")
	h.wait(t, a.ID, agent.StateIdle)
	if k := kinds(h.events(t, a.ID)); k != "message error result" {
		t.Errorf("events: %s", k)
	}
	if out, _ := h.Output(t.Context(), a.ID, 1); !out.Truncated || len(out.Stdout) > 512 {
		t.Errorf("output %d bytes, truncated %v", len(out.Stdout), out.Truncated)
	}
}
