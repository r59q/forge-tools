// Package transcript holds the events every client adapter produces, the
// same for Claude Code, Codex and Gemini CLI.
package transcript

import (
	"time"
	"unicode/utf8"
)

// Kind names what an event records.
type Kind string

// Event kinds.
const (
	// KindMessage is text from the user, the assistant or its reasoning.
	KindMessage Kind = "message"
	// KindToolCall is a tool the client started.
	KindToolCall Kind = "tool_call"
	// KindToolResult is the outcome of a tool call.
	KindToolResult Kind = "tool_result"
	// KindInputRequest is a question or permission request waiting for an answer.
	KindInputRequest Kind = "input_request"
	// KindInputAnswer is the answer to an input request.
	KindInputAnswer Kind = "input_answer"
	// KindUsage is token use and cost reported by the client.
	KindUsage Kind = "usage"
	// KindError is a failure the client or forge-agents reported.
	KindError Kind = "error"
	// KindResult ends a turn.
	KindResult Kind = "result"
	// KindChildSpawned records a new child session.
	KindChildSpawned Kind = "child_spawned"
	// KindChildFinished records the result of a child's turn.
	KindChildFinished Kind = "child_finished"
)

// Message roles.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleReasoning = "reasoning"
)

// Input request kinds.
const (
	// RequestQuestion asks for free text.
	RequestQuestion = "question"
	// RequestPermission asks to allow or deny a tool call.
	RequestPermission = "permission"
)

// Result statuses.
const (
	StatusSuccess   = "success"
	StatusError     = "error"
	StatusCancelled = "cancelled"
	StatusTimeout   = "timeout"
	StatusStopped   = "stopped"
	StatusLimit     = "limit"
)

// Event is one entry of an agent transcript. Seq, Time and Turn are set when
// the event is stored; adapters leave them empty.
type Event struct {
	Seq  int64     `json:"seq,omitzero"`
	Time time.Time `json:"time,omitzero"`
	Turn int       `json:"turn,omitzero"`
	Kind Kind      `json:"kind"`
	// Role is set on messages.
	Role string `json:"role,omitzero"`
	// Text is the message, tool output, question, answer, error or result.
	Text string `json:"text,omitzero"`
	// ToolID and ToolName identify a tool call and its result.
	ToolID   string `json:"toolId,omitzero"`
	ToolName string `json:"toolName,omitzero"`
	// Input is the tool call arguments, or the tool a permission request is
	// for, as JSON.
	Input string `json:"input,omitzero"`
	// IsError marks a failed tool result.
	IsError bool `json:"isError,omitzero"`
	// RequestID identifies an input request and its answer.
	RequestID string `json:"requestId,omitzero"`
	// RequestKind is RequestQuestion or RequestPermission.
	RequestKind string `json:"requestKind,omitzero"`
	// Allow is the answer to a permission request.
	Allow bool `json:"allow,omitzero"`
	// Status is set on results.
	Status string `json:"status,omitzero"`
	// ChildID and ChildState are set on child lifecycle events.
	ChildID    string `json:"childId,omitzero"`
	ChildState string `json:"childState,omitzero"`
	// Usage is set on usage events.
	Usage *Usage `json:"usage,omitzero"`
	// Truncated reports that Text or Input was cut to the event size limit.
	Truncated bool `json:"truncated,omitzero"`
}

// Usage is what the client reports for a turn.
type Usage struct {
	InputTokens  int64   `json:"inputTokens,omitzero"`
	OutputTokens int64   `json:"outputTokens,omitzero"`
	CachedTokens int64   `json:"cachedTokens,omitzero"`
	CostUSD      float64 `json:"costUsd,omitzero"`
}

// Size is the event's variable length, as bounded by Truncate.
func (e Event) Size() int {
	return len(e.Text) + len(e.Input)
}

// Truncate cuts Text and Input so that Size is at most limit, at UTF-8
// boundaries, and sets Truncated when it cut anything.
func (e Event) Truncate(limit int) Event {
	if limit <= 0 || e.Size() <= limit {
		return e
	}
	e.Truncated = true
	e.Input = cut(e.Input, max(limit/2, limit-len(e.Text)))
	e.Text = cut(e.Text, limit-len(e.Input))
	return e
}

func cut(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
