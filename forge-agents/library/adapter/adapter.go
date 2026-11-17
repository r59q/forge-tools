// Package adapter defines how forge-agents drives one client CLI headlessly:
// the command line of a turn and the parsing of its output stream into
// transcript events.
package adapter

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalid reports settings a client cannot honour.
var ErrInvalid = errors.New("invalid agent settings")

// Policy modes: what the client may do without asking.
const (
	// ModeReadOnly lets the client read the workspace, but not change it.
	ModeReadOnly = "read_only"
	// ModeEdit also lets the client edit files in the workspace.
	ModeEdit = "edit"
	// ModeFull lets the client run anything without asking.
	ModeFull = "full"
)

// Policy decides what a client may do. Ask routes permission requests to
// the user as input requests, for clients that can pause for them; other
// clients deny what Mode does not allow.
type Policy struct {
	Mode string
	Ask  bool
}

// Check reports whether p names a known mode.
func (p Policy) Check() error {
	switch p.Mode {
	case ModeReadOnly, ModeEdit, ModeFull:
		return nil
	}
	return fmt.Errorf("%w: policy mode %q is not read_only, edit or full", ErrInvalid, p.Mode)
}

// Tool names the forge-agents MCP server offers clients.
const (
	ServerName   = "forge-agents"
	ToolAsk      = "ask_user"
	ToolApprove  = "approve"
	maxModelSize = 128
)

// Tools is the MCP server a turn reaches for questions and permissions.
type Tools struct {
	URL   string
	Token string
	// Timeout is how long a client waits for one tool call.
	Timeout time.Duration
}

// Turn is what one client invocation needs.
type Turn struct {
	// Session resumes a client session; empty starts a new one.
	Session string
	Model   string
	Effort  string
	Policy  Policy
	// RunDir is a private directory for the files of Invocation.
	RunDir string
	Tools  Tools
}

// Invocation is the command line of a turn. The prompt is written to the
// process's standard input, so it never appears in arguments.
type Invocation struct {
	Args []string
	Env  []string
	// Files are written to RunDir, readable only by the owner, before the
	// process starts, and removed when the turn ends.
	Files map[string][]byte
}

// Adapter drives one client CLI.
type Adapter interface {
	// Check validates model, reasoning effort and policy for this client.
	Check(model, effort string, policy Policy) error
	// Command builds the invocation of a turn.
	Command(turn Turn) (Invocation, error)
	// Parse turns one line of client output into events on s. An error
	// means the line was not part of the stream; it is kept only as raw
	// output.
	Parse(s *Stream, line []byte) error
}

// CheckModel validates a model name. Empty uses the client's default.
func CheckModel(model string) error {
	if len(model) > maxModelSize || strings.HasPrefix(model, "-") ||
		strings.ContainsFunc(model, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return fmt.Errorf("%w: model %q", ErrInvalid, model)
	}
	return nil
}

// CheckEffort validates a reasoning effort against the levels a client
// accepts. Empty uses the client's default.
func CheckEffort(effort string, levels ...string) error {
	if effort == "" {
		return nil
	}
	for _, l := range levels {
		if effort == l {
			return nil
		}
	}
	if len(levels) == 0 {
		return fmt.Errorf("%w: this client has no reasoning effort setting", ErrInvalid)
	}
	return fmt.Errorf("%w: reasoning effort %q is not one of %s", ErrInvalid, effort, strings.Join(levels, ", "))
}
