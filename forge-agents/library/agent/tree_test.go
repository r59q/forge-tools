package agent_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/adapter/claude"
	"github.com/r59q/forge-tools/forge-agents/library/agent"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
	"github.com/r59q/forge-tools/forge-agents/library/workspace"
)

func TestTreeLimitThroughMCP(t *testing.T) {
	h := start(t, agent.Bounds{MaxTreeAgents: 2})
	h.named(t, "parent", spawn("child", "child", "none"), result("done"))
	h.named(t, "child", spawn("over", "unused", "none"), result("done"))
	h.named(t, "continuation", result("continued"))
	parent := h.create(t, agent.Spec{Client: claude.Name})
	h.send(t, parent.ID, "@fake parent")
	if got := h.file(t, "over.response"); !strings.Contains(got, "at most 2 agents per tree") {
		t.Fatalf("tree limit = %s", got)
	}
	h.wait(t, parent.ID, agent.StateIdle)
}

func TestTreeCreationReservations(t *testing.T) {
	h := start(t, agent.Bounds{MaxChildren: 2, MaxTreeAgents: 3})
	root := h.create(t, agent.Spec{Client: claude.Name})
	results := make(chan error, 10)
	var wg sync.WaitGroup
	for range cap(results) {
		wg.Go(func() {
			_, err := h.Create(t.Context(), agent.Spec{Parent: root.ID})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	created := 0
	for err := range results {
		if err == nil {
			created++
		} else if !errors.Is(err, agent.ErrLimit) {
			t.Errorf("create: %v", err)
		}
	}
	if created != 2 {
		t.Errorf("created %d children, want 2", created)
	}
}

func TestChildCanNarrowModelPolicy(t *testing.T) {
	h := start(t, agent.Bounds{})
	opus := agent.Model{Client: claude.Name, Name: "opus"}
	sonnet := agent.Model{Client: claude.Name, Name: "sonnet"}
	root := h.create(t, agent.Spec{Client: claude.Name, Model: opus.Name, AllowedModels: []agent.Model{opus, sonnet}})
	child := h.create(t, agent.Spec{Parent: root.ID, Model: sonnet.Name, AllowedModels: []agent.Model{sonnet}})
	if child.Model != sonnet.Name || len(child.AllowedModels) != 1 {
		t.Errorf("child policy = %+v", child)
	}
	if _, err := h.Create(t.Context(), agent.Spec{Parent: child.ID, Model: opus.Name}); !errors.Is(err, agent.ErrInvalid) {
		t.Errorf("grandchild selected excluded model: %v", err)
	}
}

func TestChildResultIsBounded(t *testing.T) {
	h := start(t, agent.Bounds{})
	root := h.create(t, agent.Spec{Client: claude.Name})
	child := h.create(t, agent.Spec{Parent: root.ID})
	h.named(t, "child", result(strings.Repeat("é", 3000)))
	h.send(t, child.ID, "@fake child")
	done := h.wait(t, child.ID, agent.StateIdle)
	if done.Result != strings.Repeat("é", 2048) {
		t.Errorf("child result length = %d", len(done.Result))
	}
}

func TestSharedTreeBudget(t *testing.T) {
	t.Run("tokens", func(t *testing.T) {
		h := start(t, agent.Bounds{MaxTreeTokens: 10})
		root := h.create(t, agent.Spec{Client: claude.Name})
		child := h.create(t, agent.Spec{Parent: root.ID})
		h.named(t, "six", `{"type":"result","subtype":"success","result":"six","usage":{"input_tokens":5,"output_tokens":1}}`)
		h.send(t, root.ID, "@fake six")
		h.wait(t, root.ID, agent.StateIdle)
		h.send(t, child.ID, "@fake six")
		a := h.wait(t, child.ID, agent.StateFailed)
		if a.ResultStatus != transcript.StatusLimit || !strings.Contains(a.Result, "tree token budget") {
			t.Errorf("child limit = %+v", a)
		}
		if _, err := h.Send(t.Context(), root.ID, "blocked"); !errors.Is(err, agent.ErrLimit) {
			t.Errorf("root after child exhausted budget: %v", err)
		}
	})
	t.Run("time", func(t *testing.T) {
		h := start(t, agent.Bounds{TreeTimeout: 500 * time.Millisecond})
		root := h.create(t, agent.Spec{Client: claude.Name})
		child := h.create(t, agent.Spec{Parent: root.ID})
		h.named(t, "quick", result("done"))
		h.named(t, "blocked", gate("never"), result("done"))
		h.send(t, root.ID, "@fake quick")
		h.wait(t, root.ID, agent.StateIdle)
		h.send(t, child.ID, "@fake blocked")
		done := h.wait(t, child.ID, agent.StateIdle)
		if done.ResultStatus != transcript.StatusTimeout || done.Result != "tree time budget exhausted" {
			t.Errorf("child timeout = %+v", done)
		}
		if _, err := h.Send(t.Context(), root.ID, "expired"); !errors.Is(err, agent.ErrLimit) {
			t.Errorf("root after tree deadline: %v", err)
		}
	})
}

func TestRecoverParentWaitingOnInterruptedChild(t *testing.T) {
	store := agent.NewMemoryStore()
	limits := agent.Limits{TurnTimeout: time.Minute, MaxEvents: 256}
	root := agent.Agent{
		ID: "legacy-root", Client: claude.Name, State: agent.StateWaitingChildren, Turns: 1, Session: "s1",
		Workspace: workspace.Workspace{Kind: workspace.KindPath, Path: t.TempDir()}, Policy: adapter.Policy{Mode: adapter.ModeReadOnly}, Limits: limits,
	}
	child := root
	child.ID, child.Parent, child.State = "legacy-child", root.ID, agent.StateRunning
	for _, a := range []agent.Agent{root, child} {
		if err := store.Put(t.Context(), a); err != nil {
			t.Fatal(err)
		}
	}
	h := startWithScripts(t, store, agent.Bounds{}, map[string]string{"continuation": result("continued")})
	finished := h.wait(t, root.ID, agent.StateIdle)
	recovered := h.wait(t, child.ID, agent.StateIdle)
	if finished.Turns != 2 || finished.Root != root.ID || recovered.Root != root.ID || recovered.Depth != 1 {
		t.Errorf("recovered root = %+v, child = %+v", finished, recovered)
	}
	if prompt := h.file(t, "continuation.prompt"); !strings.Contains(prompt, child.ID) || !strings.Contains(prompt, "forge-agents restarted") {
		t.Errorf("continuation = %s", prompt)
	}
}
