// Package codex drives Codex with `codex exec --json`. Codex cannot pause
// for permission in exec mode, so the policy sets its sandbox up front and
// approvals are never asked.
package codex

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
)

// Name is the client name.
const Name = "codex"

// TokenEnv carries the MCP server token, so it never appears in arguments.
const TokenEnv = "FORGE_AGENTS_MCP_TOKEN"

// Adapter drives Codex.
type Adapter struct{}

// Check validates settings for Codex.
func (Adapter) Check(model, effort string, policy adapter.Policy) error {
	if err := adapter.CheckModel(model); err != nil {
		return err
	}
	if err := adapter.CheckEffort(effort, "none", "minimal", "low", "medium", "high", "xhigh"); err != nil {
		return err
	}
	return policy.Check()
}

// Command builds `codex exec` for a turn, or `codex exec resume` for a
// later one. The prompt comes on standard input.
func (Adapter) Command(turn adapter.Turn) (adapter.Invocation, error) {
	args := []string{"exec"}
	if turn.Session != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--skip-git-repo-check")
	if turn.Model != "" {
		args = append(args, "--model", turn.Model)
	}
	server := "mcp_servers." + adapter.ServerName
	settings := [][2]string{
		{"sandbox_mode", quote(sandbox(turn.Policy.Mode))},
		{"approval_policy", quote("never")},
		{server + ".url", quote(turn.Tools.URL)},
		{server + ".bearer_token_env_var", quote(TokenEnv)},
		{server + ".tool_timeout_sec", strconv.FormatInt(int64(turn.Tools.Timeout.Seconds()), 10)},
	}
	if turn.Effort != "" {
		settings = append(settings, [2]string{"model_reasoning_effort", quote(turn.Effort)})
	}
	for _, kv := range settings {
		args = append(args, "--config", kv[0]+"="+kv[1])
	}
	if turn.Session != "" {
		args = append(args, turn.Session)
	}
	args = append(args, "-")
	return adapter.Invocation{Args: args, Env: []string{TokenEnv + "=" + turn.Tools.Token}}, nil
}

// quote writes a TOML string; JSON strings are valid TOML basic strings.
func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

func sandbox(mode string) string {
	switch mode {
	case adapter.ModeEdit:
		return "workspace-write"
	case adapter.ModeFull:
		return "danger-full-access"
	}
	return "read-only"
}

type event struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     *item  `json:"item"`
	Usage    *struct {
		Input  int64 `json:"input_tokens"`
		Cached int64 `json:"cached_input_tokens"`
		Output int64 `json:"output_tokens"`
	} `json:"usage"`
	Error   *failure `json:"error"`
	Message string   `json:"message"`
}

type failure struct {
	Message string `json:"message"`
}

type item struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Message   string          `json:"message"`
	Command   string          `json:"command"`
	Output    string          `json:"aggregated_output"`
	ExitCode  *int            `json:"exit_code"`
	Status    string          `json:"status"`
	Changes   json.RawMessage `json:"changes"`
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    *struct {
		Content json.RawMessage `json:"content"`
	} `json:"result"`
	Error *failure `json:"error"`
	Query string   `json:"query"`
}

// Parse reads one exec --json line.
func (Adapter) Parse(s *adapter.Stream, raw []byte) error {
	var e event
	if err := json.Unmarshal(raw, &e); err != nil {
		return fmt.Errorf("codex: %w", err)
	}
	switch e.Type {
	case "thread.started":
		s.SetSession(e.ThreadID)
	case "item.started":
		if e.Item != nil {
			started(s, *e.Item)
		}
	case "item.completed":
		if e.Item != nil {
			completed(s, *e.Item)
		}
	case "turn.completed":
		if e.Usage != nil {
			s.Emit(transcript.Event{Kind: transcript.KindUsage, Usage: &transcript.Usage{
				InputTokens: e.Usage.Input, OutputTokens: e.Usage.Output, CachedTokens: e.Usage.Cached,
			}})
		}
		s.Emit(transcript.Event{Kind: transcript.KindResult, Status: transcript.StatusSuccess, Text: s.LastText()})
	case "turn.failed":
		text := "turn failed"
		if e.Error != nil && e.Error.Message != "" {
			text = e.Error.Message
		}
		s.Emit(transcript.Event{Kind: transcript.KindError, Text: text})
		s.Emit(transcript.Event{Kind: transcript.KindResult, Status: transcript.StatusError, Text: text})
	case "error":
		s.Emit(transcript.Event{Kind: transcript.KindError, Text: e.Message})
	}
	return nil
}

// call is the tool call event of a tool item, or false for other items.
func call(it item) (transcript.Event, bool) {
	e := transcript.Event{Kind: transcript.KindToolCall, ToolID: it.ID, ToolName: it.Type}
	switch it.Type {
	case "command_execution":
		e.Input = args(map[string]any{"command": it.Command})
	case "file_change":
		e.Input = args(map[string]any{"changes": it.Changes})
	case "web_search":
		e.Input = args(map[string]any{"query": it.Query})
	case "mcp_tool_call":
		e.ToolName = "mcp__" + it.Server + "__" + it.Tool
		e.Input = adapter.JSON(it.Arguments)
	default:
		return transcript.Event{}, false
	}
	return e, true
}

func args(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func started(s *adapter.Stream, it item) {
	if e, ok := call(it); ok && s.Mark(it.ID) {
		s.Emit(e)
	}
}

func completed(s *adapter.Stream, it item) {
	switch it.Type {
	case "agent_message":
		s.Emit(transcript.Event{Kind: transcript.KindMessage, Role: transcript.RoleAssistant, Text: it.Text})
		return
	case "reasoning":
		s.Emit(transcript.Event{Kind: transcript.KindMessage, Role: transcript.RoleReasoning, Text: it.Text})
		return
	case "error":
		s.Emit(transcript.Event{Kind: transcript.KindError, Text: it.Message})
		return
	}
	started(s, it)
	if _, ok := call(it); ok {
		s.Emit(outcome(it))
	}
}

func outcome(it item) transcript.Event {
	e := transcript.Event{Kind: transcript.KindToolResult, ToolID: it.ID, IsError: it.Status == "failed"}
	switch {
	case it.Type == "command_execution":
		e.Text = it.Output
		e.IsError = e.IsError || it.ExitCode != nil && *it.ExitCode != 0
	case it.Error != nil:
		e.Text, e.IsError = it.Error.Message, true
	case it.Result != nil:
		e.Text = adapter.Text(it.Result.Content)
	default:
		e.Text = it.Status
	}
	return e
}
