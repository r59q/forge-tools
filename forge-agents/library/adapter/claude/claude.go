// Package claude drives Claude Code with `claude -p --output-format
// stream-json`. Claude Code can pause for permission: with Policy.Ask, its
// permission prompts go to the forge-agents approve tool.
package claude

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
)

// Name is the client name.
const Name = "claude"

const mcpFile = "mcp.json"

// Adapter drives Claude Code.
type Adapter struct{}

// Check validates settings for Claude Code.
func (Adapter) Check(model, effort string, policy adapter.Policy) error {
	if err := adapter.CheckModel(model); err != nil {
		return err
	}
	if err := adapter.CheckEffort(effort, "low", "medium", "high", "xhigh", "max"); err != nil {
		return err
	}
	return policy.Check()
}

// Command builds `claude -p` for a turn. The prompt comes on standard input.
func (Adapter) Command(turn adapter.Turn) (adapter.Invocation, error) {
	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		adapter.ServerName: map[string]any{
			"type":    "http",
			"url":     turn.Tools.URL,
			"headers": map[string]string{"Authorization": "Bearer " + turn.Tools.Token},
		},
	}})
	if err != nil {
		return adapter.Invocation{}, fmt.Errorf("claude: mcp config: %w", err)
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if turn.Session != "" {
		args = append(args, "--resume", turn.Session)
	}
	if turn.Model != "" {
		args = append(args, "--model", turn.Model)
	}
	if turn.Effort != "" {
		args = append(args, "--effort", turn.Effort)
	}
	args = append(args,
		"--mcp-config", filepath.Join(turn.RunDir, mcpFile),
		"--allowedTools", allowedTools(),
		// Questions go through ask_user, which waits for the answer.
		"--disallowedTools", "AskUserQuestion",
		"--permission-mode", mode(turn.Policy.Mode))
	if turn.Policy.Ask {
		args = append(args, "--permission-prompt-tool", tool(adapter.ToolApprove))
	} else {
		args = append(args, "--permission-prompts", "none")
	}
	return adapter.Invocation{
		Args:  args,
		Env:   []string{"MCP_TOOL_TIMEOUT=" + strconv.FormatInt(turn.Tools.Timeout.Milliseconds(), 10)},
		Files: map[string][]byte{mcpFile: config},
	}, nil
}

func allowedTools() string {
	names := []string{adapter.ToolAsk, adapter.ToolSpawn, adapter.ToolList, adapter.ToolSend, adapter.ToolWait, adapter.ToolStop}
	for i := range names {
		names[i] = tool(names[i])
	}
	return strings.Join(names, ",")
}

func tool(name string) string {
	return "mcp__" + adapter.ServerName + "__" + name
}

func mode(m string) string {
	switch m {
	case adapter.ModeEdit:
		return "acceptEdits"
	case adapter.ModeFull:
		return "bypassPermissions"
	}
	return "manual"
}

type line struct {
	Type      string   `json:"type"`
	Subtype   string   `json:"subtype"`
	SessionID string   `json:"session_id"`
	Message   *message `json:"message"`
	IsError   bool     `json:"is_error"`
	Result    string   `json:"result"`
	Cost      float64  `json:"total_cost_usd"`
	Usage     *usage   `json:"usage"`
	Errors    []string `json:"errors"`
}

type message struct {
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type usage struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
}

// Parse reads one stream-json line.
func (Adapter) Parse(s *adapter.Stream, raw []byte) error {
	var l line
	if err := json.Unmarshal(raw, &l); err != nil {
		return fmt.Errorf("claude: %w", err)
	}
	s.SetSession(l.SessionID)
	switch l.Type {
	case "assistant", "user":
		if l.Message != nil {
			blocks(s, l.Message.Content)
		}
	case "result":
		result(s, l)
	}
	return nil
}

func blocks(s *adapter.Stream, content json.RawMessage) {
	var list []block
	if json.Unmarshal(content, &list) != nil {
		return
	}
	for _, b := range list {
		switch b.Type {
		case "text":
			s.Emit(transcript.Event{Kind: transcript.KindMessage, Role: transcript.RoleAssistant, Text: b.Text})
		case "thinking":
			if b.Thinking != "" {
				s.Emit(transcript.Event{Kind: transcript.KindMessage, Role: transcript.RoleReasoning, Text: b.Thinking})
			}
		case "tool_use", "server_tool_use":
			s.Emit(transcript.Event{Kind: transcript.KindToolCall, ToolID: b.ID, ToolName: b.Name, Input: adapter.JSON(b.Input)})
		case "tool_result":
			s.Emit(transcript.Event{Kind: transcript.KindToolResult, ToolID: b.ToolUseID, Text: adapter.Text(b.Content), IsError: b.IsError})
		}
	}
}

func result(s *adapter.Stream, l line) {
	if l.Usage != nil && *l.Usage != (usage{}) {
		s.Emit(transcript.Event{Kind: transcript.KindUsage, Usage: &transcript.Usage{
			InputTokens:  l.Usage.Input + l.Usage.CacheCreation + l.Usage.CacheRead,
			OutputTokens: l.Usage.Output,
			CachedTokens: l.Usage.CacheRead,
			CostUSD:      l.Cost,
		}})
	}
	status, text := transcript.StatusSuccess, l.Result
	if l.IsError || l.Subtype != "success" {
		status = transcript.StatusError
		if text == "" {
			text = strings.Join(l.Errors, "; ")
		}
		if text == "" {
			text = l.Subtype
		}
		s.Emit(transcript.Event{Kind: transcript.KindError, Text: text})
	}
	s.Emit(transcript.Event{Kind: transcript.KindResult, Status: status, Text: text})
}
