package agent_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/adapter/claude"
	"github.com/r59q/forge-tools/forge-agents/library/agent"
	"github.com/r59q/forge-tools/forge-agents/library/mcp"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
	"github.com/r59q/forge-tools/forge-agents/library/workspace"
)

func (h *harness) named(t *testing.T, name string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.scripts, name+".jsonl"), []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) file(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(h.scripts, name)
	if !fileGate(path) {
		t.Fatalf("missing %s", name)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (h *harness) gate(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.scripts, name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func response[T any](t *testing.T, h *harness, name string) T {
	t.Helper()
	var parsed T
	raw := h.file(t, name+".response")
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("%s: %s", name, raw)
	}
	return parsed
}

func childID(t *testing.T, h *harness, alias string) string {
	t.Helper()
	id := response[map[string]string](t, h, alias)["id"]
	if id == "" {
		t.Fatal("missing child ID")
	}
	return id
}

func spawn(alias, script, mode string) string {
	return fmt.Sprintf(`{"fake":"mcp","tool":"spawn_agent","alias":%q,"args":{"prompt":%q,"workspace":%q}}`,
		alias, "@fake "+script, mode)
}

func gate(name string) string {
	return fmt.Sprintf(`{"fake":"gate","text":%q}`, name)
}

func result(text string) string {
	return fmt.Sprintf(`{"type":"result","subtype":"success","result":%q,"session_id":"s1"}`, text)
}

func TestChildMCPTools(t *testing.T) {
	h := start(t, agent.Bounds{})
	repo := workspace.Repository{Source: "test", Path: "owner/repo"}
	parent := h.create(t, agent.Spec{Client: claude.Name, Model: "opus", Effort: "high",
		Workspace: workspace.Spec{Kind: workspace.KindCheckout, Repository: repo, Reference: "feature"}})
	h.checkouts.dir = t.TempDir()
	outsider := h.create(t, agent.Spec{Client: claude.Name})
	h.named(t, "shared", result("shared result"))
	h.named(t, "checkout", result("checkout result"))
	h.named(t, "scratch", `{"fake":"ask","question":"Need an answer?"}`, result("scratch result"))
	h.named(t, "second", result("second result"))
	h.named(t, "parent",
		spawn("shared-id", "shared", "shared"), spawn("checkout-id", "checkout", "checkout"), spawn("scratch-id", "scratch", "none"),
		gate("input-pending"), `{"fake":"mcp","tool":"list_agents","alias":"listed"}`,
		`{"fake":"mcp","tool":"wait_agents","alias":"timeout","children":["scratch-id"],"args":{"timeout_ms":5}}`,
		`{"fake":"mcp","tool":"wait_agents","alias":"waited","children":["shared-id","checkout-id","scratch-id"]}`,
		`{"fake":"mcp","tool":"send_message","alias":"sent","children":["scratch-id"],"args":{"text":"@fake second"}}`,
		`{"fake":"mcp","tool":"wait_agents","alias":"waited-again","children":["scratch-id"]}`,
		fmt.Sprintf(`{"fake":"mcp","tool":"stop_agent","alias":"forbidden","children":[%q]}`, outsider.ID),
		`{"fake":"mcp","tool":"stop_agent","alias":"stopped","children":["scratch-id"]}`, result("parent result"))
	h.send(t, parent.ID, "@fake parent")
	scratch := h.wait(t, childID(t, h, "scratch-id"), agent.StateWaitingInput)
	h.gate(t, "input-pending")
	listed := response[[]mcp.Child](t, h, "listed")
	if len(listed) != 3 || listed[2].ID != scratch.ID || len(listed[2].Inputs) != 1 {
		t.Fatalf("list = %+v", listed)
	}
	input := listed[2].Inputs[0]
	if input.Text != "Need an answer?" || input.Kind != transcript.RequestQuestion {
		t.Fatalf("input = %+v", input)
	}
	if wait := response[mcp.Wait](t, h, "timeout"); !wait.TimedOut || wait.Children[0].State != string(agent.StateWaitingInput) {
		t.Fatalf("wait timeout = %+v", wait)
	}
	if _, err := h.Answer(t.Context(), scratch.ID, input.ID, "Yes", false); err != nil {
		t.Fatal(err)
	}
	checkChildToolResults(t, h, parent, scratch, repo)
}

func checkChildToolResults(t *testing.T, h *harness, parent, scratch agent.Agent, repo workspace.Repository) {
	t.Helper()
	h.wait(t, parent.ID, agent.StateIdle)
	waited := response[mcp.Wait](t, h, "waited")
	if waited.TimedOut || len(waited.Children) != 3 {
		t.Fatalf("wait = %+v", waited)
	}
	for i, want := range []string{"shared result", "checkout result", "scratch result"} {
		if got := waited.Children[i]; got.Result != want || got.State != string(agent.StateIdle) || len(got.Inputs) != 0 {
			t.Errorf("child result = %+v, want %s", got, want)
		}
	}
	if got := response[mcp.Wait](t, h, "waited-again").Children[0]; got.Result != "second result" {
		t.Errorf("second result = %+v", got)
	}
	if got := response[mcp.Child](t, h, "stopped"); got.State != string(agent.StateStopped) {
		t.Errorf("stop = %+v", got)
	}
	if got := h.file(t, "forbidden.response"); !strings.Contains(got, "no direct child") {
		t.Errorf("out-of-scope stop = %s", got)
	}
	if scratch.Client != parent.Client || scratch.Model != parent.Model || scratch.Effort != parent.Effort ||
		scratch.Root != parent.ID || scratch.Depth != 1 {
		t.Errorf("inherited child config = %+v", scratch)
	}
	if h.file(t, "shared.cwd") != parent.Workspace.Path || h.file(t, "checkout.cwd") != h.checkouts.dir ||
		h.file(t, "scratch.cwd") != filepath.Join(h.scratch, scratch.ID) {
		t.Error("a child ran in the wrong workspace")
	}
	if len(h.checkouts.requests) != 2 || h.checkouts.requests[1].Reference != "" || h.checkouts.requests[1].Repository != repo {
		t.Errorf("checkout requests = %+v", h.checkouts.requests)
	}
	shared := childID(t, h, "shared-id")
	if err := h.Delete(t.Context(), shared, true); err != nil || len(h.checkouts.removed) != 0 {
		t.Errorf("delete shared borrowed checkout: %v, removes = %v", err, h.checkouts.removed)
	}
}

func TestParentResumesAfterAllChildren(t *testing.T) {
	h := start(t, agent.Bounds{})
	h.named(t, "parent", spawn("slow-id", "slow", "none"), spawn("fast-id", "fast", "none"), result("delegated"))
	h.named(t, "slow", gate("finish-slow"), result("slow finding"))
	h.named(t, "fast", gate("finish-fast"), result("fast finding"))
	h.named(t, "continuation", result("combined findings"))
	parent := h.create(t, agent.Spec{Client: claude.Name})
	h.send(t, parent.ID, "@fake parent")
	h.wait(t, parent.ID, agent.StateWaitingChildren)
	fast, slow := childID(t, h, "fast-id"), childID(t, h, "slow-id")
	h.gate(t, "finish-fast")
	h.wait(t, fast, agent.StateIdle)
	if got, _ := h.Get(t.Context(), parent.ID); got.State != agent.StateWaitingChildren || got.Turns != 1 {
		t.Fatalf("parent after one child finishes: %+v", got)
	}
	h.gate(t, "finish-slow")
	got := h.wait(t, parent.ID, agent.StateIdle)
	prompt := h.file(t, "continuation.prompt")
	for _, want := range []string{fast, slow, "fast finding", "slow finding", `"state":"idle"`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("continuation missing %q: %s", want, prompt)
		}
	}
	if got.Turns != 2 || got.Result != "combined findings" {
		t.Errorf("parent = %+v", got)
	}
	var spawned, finished int
	for _, e := range h.events(t, parent.ID) {
		switch e.Kind {
		case transcript.KindChildSpawned:
			spawned++
		case transcript.KindChildFinished:
			finished++
			if e.ChildID == "" || e.ChildState != string(agent.StateIdle) || e.Text == "" {
				t.Errorf("child_finished = %+v", e)
			}
		}
	}
	if spawned != 2 || finished != 2 {
		t.Errorf("events: %d child_spawned, %d child_finished", spawned, finished)
	}
}

func TestChildPolicyAndTreeLimits(t *testing.T) {
	h := start(t, agent.Bounds{MaxDepth: 1, MaxChildren: 2, MaxTreeAgents: 3})
	parent := h.create(t, agent.Spec{Client: claude.Name, Model: "opus", Policy: adapter.Policy{Mode: adapter.ModeEdit, Ask: true}})
	h.named(t, "parent",
		`{"fake":"mcp","tool":"spawn_agent","alias":"widen","args":{"prompt":"hi","workspace":"none","mode":"full"}}`,
		`{"fake":"mcp","tool":"spawn_agent","alias":"other-model","args":{"prompt":"hi","workspace":"none","model":"other"}}`,
		spawn("first", "nested", "none"), spawn("second", "finish", "none"), spawn("fan-out", "finish", "none"),
		`{"fake":"mcp","tool":"wait_agents","children":["first","second"]}`, result("done"))
	h.named(t, "nested", spawn("depth", "finish", "none"), result("done"))
	h.named(t, "finish", result("done"))
	h.send(t, parent.ID, "@fake parent")
	h.wait(t, parent.ID, agent.StateIdle)
	for alias, want := range map[string]string{"widen": "permissions exceed", "other-model": "outside the policy",
		"fan-out": "direct children", "depth": "maximum child depth"} {
		if got := h.file(t, alias+".response"); !strings.HasPrefix(got, "error: ") || !strings.Contains(got, want) {
			t.Errorf("%s = %s", alias, got)
		}
	}
	root := h.create(t, agent.Spec{Client: claude.Name, AllowedModels: []agent.Model{{Client: claude.Name}, {Client: claude.Name, Name: "opus"}}})
	if _, err := h.Create(t.Context(), agent.Spec{Parent: root.ID, AllowedModels: []agent.Model{{Client: claude.Name, Name: "external"}}}); !errors.Is(err, agent.ErrInvalid) {
		t.Errorf("widen allowed models: %v", err)
	}
}

func TestStopAndDeleteCascades(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete=%v", remove), func(t *testing.T) {
			h := start(t, agent.Bounds{})
			h.named(t, "parent", spawn("child", "child", "shared"), gate("hold-parent"), result("parent"))
			h.named(t, "child", spawn("grandchild", "grandchild", "none"), gate("hold-child"), result("child"))
			h.named(t, "grandchild", `{"fake":"ask","question":"Wait here"}`, result("grandchild"))
			parent := h.create(t, agent.Spec{Client: claude.Name})
			h.send(t, parent.ID, "@fake parent")
			child, grandchild := childID(t, h, "child"), childID(t, h, "grandchild")
			h.wait(t, grandchild, agent.StateWaitingInput)
			if remove {
				if err := h.Delete(t.Context(), parent.ID, false); err != nil {
					t.Fatal(err)
				}
				if _, err := h.Get(t.Context(), parent.ID); !errors.Is(err, agent.ErrNotFound) {
					t.Fatalf("parent was kept: %v", err)
				}
			} else if a, err := h.Stop(t.Context(), parent.ID); err != nil || a.State != agent.StateStopped {
				t.Fatalf("stop: %+v, %v", a, err)
			}
			for _, id := range []string{child, grandchild} {
				a := h.wait(t, id, agent.StateStopped)
				if a.ResultStatus != transcript.StatusStopped || a.Root != parent.ID {
					t.Errorf("descendant stopped: %+v", a)
				}
			}
		})
	}
}
