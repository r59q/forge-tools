package agent_test

// The test binary doubles as a fake client CLI: with fakeEnv set it
// replays a script instead of running tests. Scripts are client output
// lines, printed as they are, and directives that call the forge-agents
// tool server the way a real client would. No model is ever called.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/adapter/codex"
	"github.com/r59q/forge-tools/forge-agents/library/adapter/gemini"
)

const (
	fakeEnv    = "FORGE_AGENTS_FAKE_CLIENT"
	scriptsEnv = "FORGE_AGENTS_FAKE_SCRIPTS"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) != "" {
		os.Exit(fakeClient())
	}
	os.Exit(m.Run())
}

// directive is a script line the fake acts on instead of printing.
type directive struct {
	Fake     string          `json:"fake"`
	Question string          `json:"question"`
	Tool     string          `json:"tool"`
	Input    json.RawMessage `json:"input"`
	Text     string          `json:"text"`
	Line     string          `json:"line"`
	Count    int             `json:"count"`
	Code     int             `json:"code"`
	MS       int             `json:"ms"`
	Alias    string          `json:"alias"`
	Args     map[string]any  `json:"args"`
	Children []string        `json:"children"`
}

// fakeClient plays script <turn>.jsonl, counting turns in the workspace,
// and records its arguments, prompt and tool answers there.
func fakeClient() int {
	prompt, _ := io.ReadAll(os.Stdin)
	if name, ok := scriptedName(string(prompt)); ok {
		return namedScript(name, prompt)
	}
	turns, _ := os.ReadFile("turns")
	n := len(turns) + 1
	_ = os.WriteFile("turns", bytes.Repeat([]byte("x"), n), 0o600)
	_ = os.WriteFile(fmt.Sprintf("args-%d", n), []byte(strings.Join(os.Args[1:], "\n")), 0o600)
	_ = os.WriteFile(fmt.Sprintf("prompt-%d", n), prompt, 0o600)
	script, err := os.ReadFile(filepath.Join(os.Getenv(scriptsEnv), strconv.Itoa(n)+".jsonl"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(script)), "\n") {
		var d directive
		if json.Unmarshal([]byte(line), &d) != nil || d.Fake == "" {
			fmt.Println(line)
			continue
		}
		if code, done := act(d); done {
			return code
		}
	}
	return 0
}

func act(d directive) (int, bool) {
	switch d.Fake {
	case "ask":
		record(callTool("ask_user", map[string]any{"question": d.Question}))
	case "approve":
		record(callTool("approve", map[string]any{"tool_name": d.Tool, "input": d.Input}))
	case "sleep":
		time.Sleep(time.Duration(d.MS) * time.Millisecond)
	case "stderr":
		fmt.Fprintln(os.Stderr, d.Text)
	case "repeat":
		for range d.Count {
			fmt.Println(d.Line)
		}
	case "long":
		fmt.Println(strings.Repeat("x", d.Count))
	case "exit":
		return d.Code, true
	}
	return 0, false
}

func record(text string) {
	f, err := os.OpenFile("answers", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, text)
}

// toolServer finds the tool server the way each client is told about it.
func toolServer() (string, string) {
	var cfg struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			HTTPURL string            `json:"httpUrl"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	args := os.Args[1:]
	for i, a := range args {
		if a == "--mcp-config" && i+1 < len(args) {
			data, _ := os.ReadFile(args[i+1])
			_ = json.Unmarshal(data, &cfg)
		}
		if v, ok := strings.CutPrefix(a, "mcp_servers.forge-agents.url="); ok {
			url, _ := strconv.Unquote(v)
			return url, "Bearer " + os.Getenv(codex.TokenEnv)
		}
	}
	if path := os.Getenv(gemini.SettingsEnv); path != "" {
		data, _ := os.ReadFile(path)
		_ = json.Unmarshal(data, &cfg)
	}
	s := cfg.MCPServers["forge-agents"]
	return s.URL + s.HTTPURL, s.Headers["Authorization"]
}

func callTool(name string, args map[string]any) string {
	url, auth := toolServer()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body)) //nolint:noctx // fake client
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "error: " + err.Error()
	}
	defer resp.Body.Close()
	var out struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "error: " + resp.Status
	}
	if out.Error != nil {
		return "error: " + out.Error.Message
	}
	if len(out.Result.Content) == 0 {
		return ""
	}
	if out.Result.IsError {
		return "error: " + out.Result.Content[0].Text
	}
	return out.Result.Content[0].Text
}
