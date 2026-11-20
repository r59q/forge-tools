package adapter_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/adapter/claude"
	"github.com/r59q/forge-tools/forge-agents/library/adapter/codex"
	"github.com/r59q/forge-tools/forge-agents/library/adapter/gemini"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
)

var update = flag.Bool("update", false, "rewrite golden files")

var adapters = map[string]adapter.Adapter{
	claude.Name: claude.Adapter{},
	codex.Name:  codex.Adapter{},
	gemini.Name: gemini.Adapter{},
}

type golden struct {
	Session string             `json:"session"`
	Result  bool               `json:"result"`
	Events  []transcript.Event `json:"events"`
}

// TestGolden parses each recorded stream in testdata/<client> and compares
// the events with <name>.golden.json. Run with -update to rewrite them.
func TestGolden(t *testing.T) {
	fixtures, err := filepath.Glob("testdata/*/*.jsonl")
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, path := range fixtures {
		client := filepath.Base(filepath.Dir(path))
		t.Run(client+"/"+filepath.Base(path), func(t *testing.T) {
			got := parse(t, adapters[client], path)
			want := strings.TrimSuffix(path, ".jsonl") + ".golden.json"
			data, err := json.MarshalIndent(got, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, '\n')
			if *update {
				if err := os.WriteFile(want, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			expected, err := os.ReadFile(want)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, expected) {
				t.Errorf("events differ from %s:\n%s", want, data)
			}
		})
	}
}

func parse(t *testing.T, a adapter.Adapter, path string) golden {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var s adapter.Stream
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		_ = a.Parse(&s, scanner.Bytes())
	}
	s.Close()
	return golden{Session: s.Session(), Result: s.HasResult(), Events: s.Take()}
}

func TestParseRejectsText(t *testing.T) {
	for name, a := range adapters {
		var s adapter.Stream
		if err := a.Parse(&s, []byte("Reading prompt from stdin...")); err == nil {
			t.Errorf("%s: parsed a text line", name)
		}
	}
}

func turn(session string, policy adapter.Policy) adapter.Turn {
	return adapter.Turn{
		Session: session, Model: "m-1", Policy: policy, RunDir: "/run/a",
		Tools: adapter.Tools{URL: "http://127.0.0.1:1/mcp", Token: "secret-token", Timeout: time.Minute},
	}
}

func TestClaudeCommand(t *testing.T) {
	tr := turn("s-1", adapter.Policy{Mode: adapter.ModeEdit, Ask: true})
	tr.Effort = "high"
	inv, err := claude.Adapter{}.Command(tr)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-p", "--output-format", "stream-json", "--verbose", "--resume", "s-1", "--model", "m-1",
		"--effort", "high", "--mcp-config", "/run/a/mcp.json", "--allowedTools",
		"mcp__forge-agents__ask_user,mcp__forge-agents__spawn_agent,mcp__forge-agents__list_agents," +
			"mcp__forge-agents__send_message,mcp__forge-agents__wait_agents,mcp__forge-agents__stop_agent",
		"--disallowedTools", "AskUserQuestion", "--permission-mode", "acceptEdits",
		"--permission-prompt-tool", "mcp__forge-agents__approve"}
	if !slices.Equal(inv.Args, want) {
		t.Errorf("args = %q", inv.Args)
	}
	if !slices.Equal(inv.Env, []string{"MCP_TOOL_TIMEOUT=60000"}) {
		t.Errorf("env = %q", inv.Env)
	}
	if !strings.Contains(string(inv.Files["mcp.json"]), `"Authorization":"Bearer secret-token"`) {
		t.Errorf("mcp config = %s", inv.Files["mcp.json"])
	}
	noAsk, err := claude.Adapter{}.Command(turn("", adapter.Policy{Mode: adapter.ModeReadOnly}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(noAsk.Args, "manual") || !slices.Contains(noAsk.Args, "none") || slices.Contains(noAsk.Args, "--resume") {
		t.Errorf("args = %q", noAsk.Args)
	}
	assertNoToken(t, inv, noAsk)
}

func TestCodexCommand(t *testing.T) {
	first, err := codex.Adapter{}.Command(turn("", adapter.Policy{Mode: adapter.ModeEdit}))
	if err != nil {
		t.Fatal(err)
	}
	if first.Args[0] != "exec" || first.Args[1] != "--json" || first.Args[len(first.Args)-1] != "-" {
		t.Errorf("args = %q", first.Args)
	}
	if !slices.Contains(first.Args, `sandbox_mode="workspace-write"`) ||
		!slices.Contains(first.Args, `mcp_servers.forge-agents.url="http://127.0.0.1:1/mcp"`) ||
		!slices.Contains(first.Args, "mcp_servers.forge-agents.tool_timeout_sec=60") {
		t.Errorf("args = %q", first.Args)
	}
	if !slices.Equal(first.Env, []string{codex.TokenEnv + "=secret-token"}) {
		t.Errorf("env = %q", first.Env)
	}
	tr := turn("thread-1", adapter.Policy{Mode: adapter.ModeFull})
	tr.Effort = "low"
	next, err := codex.Adapter{}.Command(tr)
	if err != nil {
		t.Fatal(err)
	}
	n := len(next.Args)
	if next.Args[1] != "resume" || next.Args[n-2] != "thread-1" || !slices.Contains(next.Args, `model_reasoning_effort="low"`) ||
		!slices.Contains(next.Args, `sandbox_mode="danger-full-access"`) {
		t.Errorf("args = %q", next.Args)
	}
	assertNoToken(t, first, next)
}

func TestGeminiCommand(t *testing.T) {
	inv, err := gemini.Adapter{}.Command(turn("g-1", adapter.Policy{Mode: adapter.ModeReadOnly, Ask: true}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--output-format", "stream-json", "--approval-mode", "default", "--model", "m-1", "--resume", "g-1"}
	if !slices.Equal(inv.Args, want) {
		t.Errorf("args = %q", inv.Args)
	}
	if !slices.Equal(inv.Env, []string{gemini.SettingsEnv + "=/run/a/settings.json"}) {
		t.Errorf("env = %q", inv.Env)
	}
	if !strings.Contains(string(inv.Files["settings.json"]), `"httpUrl":"http://127.0.0.1:1/mcp"`) {
		t.Errorf("settings = %s", inv.Files["settings.json"])
	}
	assertNoToken(t, inv)
}

func assertNoToken(t *testing.T, invocations ...adapter.Invocation) {
	t.Helper()
	for _, inv := range invocations {
		for _, a := range inv.Args {
			if strings.Contains(a, "secret-token") {
				t.Errorf("token in argument %q", a)
			}
		}
	}
}

func TestCheck(t *testing.T) {
	ok := adapter.Policy{Mode: adapter.ModeEdit}
	cases := []struct {
		client, model, effort string
		policy                adapter.Policy
		valid                 bool
	}{
		{claude.Name, "claude-opus-5-5", "max", ok, true},
		{claude.Name, "", "minimal", ok, false},
		{codex.Name, "gpt-5.1-codex", "minimal", ok, true},
		{codex.Name, "-c", "", ok, false},
		{gemini.Name, "gemini-2.5-pro", "", ok, true},
		{gemini.Name, "", "high", ok, false},
		{gemini.Name, "a b", "", ok, false},
		{claude.Name, "", "", adapter.Policy{Mode: "root"}, false},
		{claude.Name, "", "", adapter.Policy{Mode: adapter.ModeReadOnly, Ask: true}, true},
		{codex.Name, "", "", adapter.Policy{Mode: adapter.ModeReadOnly, Ask: true}, false},
		{gemini.Name, "", "", adapter.Policy{Mode: adapter.ModeEdit, Ask: true}, false},
	}
	for _, c := range cases {
		if err := adapters[c.client].Check(c.model, c.effort, c.policy); (err == nil) != c.valid {
			t.Errorf("%s %q %q %v: %v", c.client, c.model, c.effort, c.policy, err)
		}
	}
}
