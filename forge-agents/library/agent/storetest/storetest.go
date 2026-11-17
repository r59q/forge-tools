// Package storetest checks that an agent.Store behaves as the manager
// expects, so every implementation runs the same tests.
package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/agent"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
	"github.com/r59q/forge-tools/forge-agents/library/workspace"
)

// Run tests the store open returns; each test opens its own.
func Run(t *testing.T, open func(t *testing.T) agent.Store) {
	t.Helper()
	t.Run("agents", func(t *testing.T) { testAgents(t, open(t)) })
	t.Run("events", func(t *testing.T) { testEvents(t, open(t)) })
	t.Run("output", func(t *testing.T) { testOutput(t, open(t)) })
}

// repository is the repository of checkout samples.
func repository() workspace.Repository {
	return workspace.Repository{Source: "github", Path: "r59q/forge-tools"}
}

func sample(id string, created time.Time) agent.Agent {
	return agent.Agent{
		ID: id, Client: "claude", Model: "opus", State: agent.StateIdle,
		Workspace: workspace.Workspace{Kind: workspace.KindCheckout, Path: "/data/copies/x", Checkout: "x", Repository: repository()},
		Policy:    adapter.Policy{Mode: adapter.ModeEdit, Ask: true},
		Limits:    agent.Limits{TurnTimeout: time.Minute, MaxEvents: 3},
		Session:   "s1", Turns: 2, CreatedAt: created.UTC(), UpdatedAt: created.UTC(),
	}
}

func testAgents(t *testing.T, s agent.Store) {
	ctx := t.Context()
	base := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	b, a := sample("b", base), sample("a", base.Add(time.Second))
	a.Parent, a.Workspace = "b", workspace.Workspace{Kind: workspace.KindScratch, Path: "/tmp/a"}
	for _, x := range []agent.Agent{a, b} {
		if err := s.Put(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Get(ctx, "b")
	if err != nil || got.Policy != b.Policy || got.Limits != b.Limits || got.Workspace != b.Workspace || !got.CreatedAt.Equal(b.CreatedAt) {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.Get(ctx, "c"); !errors.Is(err, agent.ErrNotFound) {
		t.Errorf("get missing: %v", err)
	}
	b.State = agent.StateRunning
	if err := s.Put(ctx, b); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		f    agent.Filter
		want string
	}{
		"all":        {agent.Filter{}, "ba"},
		"state":      {agent.Filter{State: agent.StateRunning}, "b"},
		"parent":     {agent.Filter{Parent: "b"}, "a"},
		"repository": {agent.Filter{Repository: repository()}, "b"},
		"none":       {agent.Filter{State: agent.StateFailed}, ""},
	} {
		list, err := s.List(ctx, c.f)
		ids := ""
		for _, x := range list {
			ids += x.ID
		}
		if err != nil || ids != c.want {
			t.Errorf("%s: %q, %v", name, ids, err)
		}
	}
	if err := s.Delete(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "b"); !errors.Is(err, agent.ErrNotFound) {
		t.Errorf("get deleted: %v", err)
	}
}

func testEvents(t *testing.T, s agent.Store) {
	ctx := t.Context()
	if err := s.Put(ctx, sample("a", time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "missing", transcript.Event{Kind: transcript.KindMessage}, 3); !errors.Is(err, agent.ErrNotFound) {
		t.Errorf("append to missing: %v", err)
	}
	usage := &transcript.Usage{InputTokens: 10, CostUSD: 0.5}
	for i, e := range []transcript.Event{
		{Kind: transcript.KindMessage, Role: transcript.RoleUser, Text: "hi", Turn: 1},
		{Kind: transcript.KindUsage, Usage: usage, Turn: 1},
		{Kind: transcript.KindResult, Status: transcript.StatusSuccess, Turn: 1},
	} {
		stored, err := s.Append(ctx, "a", e, 3)
		if err != nil || stored.Seq != int64(i+1) {
			t.Fatalf("append %d: %+v, %v", i, stored, err)
		}
	}
	if _, err := s.Append(ctx, "a", transcript.Event{Kind: transcript.KindError}, 3); !errors.Is(err, agent.ErrTranscriptFull) {
		t.Errorf("append past the limit: %v", err)
	}
	page, err := s.Events(ctx, "a", 1, 1)
	if err != nil || len(page) != 1 || page[0].Seq != 2 || page[0].Usage == nil || *page[0].Usage != *usage {
		t.Errorf("page = %+v, %v", page, err)
	}
	if page, err := s.Events(ctx, "a", 3, 10); err != nil || len(page) != 0 {
		t.Errorf("page after the end = %+v, %v", page, err)
	}
	if _, err := s.Events(ctx, "missing", 0, 10); !errors.Is(err, agent.ErrNotFound) {
		t.Errorf("events of missing: %v", err)
	}
	if err := s.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, sample("a", time.Now())); err != nil {
		t.Fatal(err)
	}
	if page, err := s.Events(ctx, "a", 0, 10); err != nil || len(page) != 0 {
		t.Errorf("events survived delete: %+v, %v", page, err)
	}
}

func testOutput(t *testing.T, s agent.Store) {
	ctx := t.Context()
	if err := s.Put(ctx, sample("a", time.Now())); err != nil {
		t.Fatal(err)
	}
	for turn := 1; turn <= 3; turn++ {
		o := agent.Output{Stdout: []byte{byte('0' + turn)}, Stderr: []byte("err"), Truncated: turn == 3}
		if err := s.PutOutput(ctx, "a", turn, o, 2); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Output(ctx, "a", 1); !errors.Is(err, agent.ErrNotFound) {
		t.Errorf("turn 1 kept: %v", err)
	}
	o, err := s.Output(ctx, "a", 3)
	if err != nil || string(o.Stdout) != "3" || string(o.Stderr) != "err" || !o.Truncated {
		t.Errorf("turn 3 = %+v, %v", o, err)
	}
	if _, err := s.Output(ctx, "a", 2); err != nil {
		t.Errorf("turn 2: %v", err)
	}
}
