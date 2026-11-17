// Package gemini drives Gemini CLI with `gemini --output-format
// stream-json`. Gemini CLI cannot pause for permission headlessly, so the
// policy sets its approval mode up front.
package gemini

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
)

// Name is the client name.
const Name = "gemini"

// SettingsEnv names the system settings file Gemini CLI reads; forge-agents
// uses it to add its MCP server without touching user settings.
const SettingsEnv = "GEMINI_CLI_SYSTEM_SETTINGS_PATH"

const settingsFile = "settings.json"

// Adapter drives Gemini CLI.
type Adapter struct{}

// Check validates settings for Gemini CLI, which has no reasoning effort.
func (Adapter) Check(model, effort string, policy adapter.Policy) error {
	if err := adapter.CheckModel(model); err != nil {
		return err
	}
	if err := adapter.CheckEffort(effort); err != nil {
		return err
	}
	return policy.Check()
}

// Command builds `gemini` for a turn. Without --prompt and with standard
// input not a terminal, Gemini CLI reads the prompt from standard input and
// runs headlessly.
func (Adapter) Command(turn adapter.Turn) (adapter.Invocation, error) {
	settings, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		adapter.ServerName: map[string]any{
			"httpUrl": turn.Tools.URL,
			"headers": map[string]string{"Authorization": "Bearer " + turn.Tools.Token},
			"timeout": turn.Tools.Timeout.Milliseconds(),
			// Its tools only ask the user; confirming them would block.
			"trust": true,
		},
	}})
	if err != nil {
		return adapter.Invocation{}, fmt.Errorf("gemini: settings: %w", err)
	}
	args := []string{"--output-format", "stream-json", "--approval-mode", approval(turn.Policy.Mode)}
	if turn.Model != "" {
		args = append(args, "--model", turn.Model)
	}
	if turn.Session != "" {
		args = append(args, "--resume", turn.Session)
	}
	return adapter.Invocation{
		Args:  args,
		Env:   []string{SettingsEnv + "=" + filepath.Join(turn.RunDir, settingsFile)},
		Files: map[string][]byte{settingsFile: settings},
	}, nil
}

func approval(mode string) string {
	switch mode {
	case adapter.ModeEdit:
		return "auto_edit"
	case adapter.ModeFull:
		return "yolo"
	}
	return "default"
}

type event struct {
	Type       string          `json:"type"`
	SessionID  string          `json:"session_id"`
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	Delta      bool            `json:"delta"`
	ToolName   string          `json:"tool_name"`
	ToolID     string          `json:"tool_id"`
	Parameters json.RawMessage `json:"parameters"`
	Status     string          `json:"status"`
	Output     json.RawMessage `json:"output"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
	Stats   *struct {
		Input  int64 `json:"input_tokens"`
		Output int64 `json:"output_tokens"`
		Cached int64 `json:"cached"`
	} `json:"stats"`
}

// Parse reads one stream-json line.
func (Adapter) Parse(s *adapter.Stream, raw []byte) error {
	var e event
	if err := json.Unmarshal(raw, &e); err != nil {
		return fmt.Errorf("gemini: %w", err)
	}
	switch e.Type {
	case "init":
		s.SetSession(e.SessionID)
	case "message":
		if e.Role != transcript.RoleAssistant {
			return nil
		}
		if e.Delta {
			s.Append(transcript.RoleAssistant, e.Content)
		} else {
			s.Emit(transcript.Event{Kind: transcript.KindMessage, Role: transcript.RoleAssistant, Text: e.Content})
		}
	case "tool_use":
		s.Emit(transcript.Event{Kind: transcript.KindToolCall, ToolID: e.ToolID, ToolName: e.ToolName, Input: adapter.JSON(e.Parameters)})
	case "tool_result":
		r := transcript.Event{Kind: transcript.KindToolResult, ToolID: e.ToolID, Text: adapter.Text(e.Output), IsError: e.Status == "error"}
		if e.Error != nil && r.Text == "" {
			r.Text = e.Error.Message
		}
		s.Emit(r)
	case "error":
		s.Emit(transcript.Event{Kind: transcript.KindError, Text: e.Message})
	case "result":
		result(s, e)
	}
	return nil
}

func result(s *adapter.Stream, e event) {
	if e.Stats != nil {
		s.Emit(transcript.Event{Kind: transcript.KindUsage, Usage: &transcript.Usage{
			InputTokens: e.Stats.Input, OutputTokens: e.Stats.Output, CachedTokens: e.Stats.Cached,
		}})
	}
	if e.Status == transcript.StatusSuccess {
		s.Emit(transcript.Event{Kind: transcript.KindResult, Status: transcript.StatusSuccess, Text: s.LastText()})
		return
	}
	text := "turn failed"
	if e.Error != nil && e.Error.Message != "" {
		text = e.Error.Message
	}
	s.Emit(transcript.Event{Kind: transcript.KindError, Text: text})
	s.Emit(transcript.Event{Kind: transcript.KindResult, Status: transcript.StatusError, Text: text})
}
