// Package explorer browses explicitly opened live worktrees without running repository code.
package explorer

import "errors"

// Stable error categories do not contain host paths or repository text.
var (
	ErrInvalid     = errors.New("invalid explorer request")
	ErrNotFound    = errors.New("source or path not found")
	ErrUnsupported = errors.New("unsupported content or operation")
	ErrTooLarge    = errors.New("content exceeds limit")
	ErrChanged     = errors.New("content changed; reload the location")
	ErrForbidden   = errors.New("path unavailable within root")
)

// Limits bound work and response sizes independently of caller input.
type Limits struct {
	FileBytes   int `json:"fileBytes"`
	Entries     int `json:"entries"`
	Results     int `json:"results"`
	SearchBytes int `json:"searchBytes"`
	Depth       int `json:"depth"`
}

// DefaultLimits returns the fixed initial resource budget.
func DefaultLimits() Limits {
	return Limits{FileBytes: 1 << 20, Entries: 10000, Results: 200, SearchBytes: 64 << 20, Depth: 64}
}

// Capabilities advertise optional provider operations. Live worktrees support none of these.
type Capabilities struct {
	Symbols     bool `json:"symbols"`
	Definitions bool `json:"definitions"`
	References  bool `json:"references"`
	History     bool `json:"history"`
}

// Source identifies a configured root incarnation, never an immutable snapshot.
// Version changes on reopen; individual reads have separate content versions.
type Source struct {
	ID           string       `json:"id"`
	Label        string       `json:"label"`
	Version      string       `json:"version"`
	Kind         string       `json:"kind"`
	Capabilities Capabilities `json:"capabilities"`
	Limits       Limits       `json:"limits"`
}

// Range uses one-based lines and UTF-16 columns, with an exclusive end column.
// A zero range denotes the whole file.
type Range struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
	EndLine     int `json:"endLine"`
	EndColumn   int `json:"endColumn"`
}

// Location binds a path and range to an originating source and file content version.
type Location struct {
	Source  string `json:"source"`
	Version string `json:"version"`
	Path    string `json:"path"`
	Range   Range  `json:"range"`
}

// File contains UTF-8 text and the identity of exactly those bytes.
type File struct {
	Location Location `json:"location"`
	Text     string   `json:"text"`
}

// Entry is a directory child. Kind is file, directory, or unsupported.
type Entry struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// State describes incomplete discovery. Skipped counts entries omitted by policy or read failures.
type State struct {
	Truncated bool `json:"truncated"`
	Canceled  bool `json:"canceled"`
	Skipped   int  `json:"skipped"`
}

// Directory is a bounded live listing. Version identifies this listing, not its descendants.
type Directory struct {
	Source  string  `json:"source"`
	Version string  `json:"version"`
	Entries []Entry `json:"entries"`
	State   State   `json:"state"`
}

// Query selects literal, case-sensitive text or path lookup, with an optional slash-path glob.
// Glob uses path.Match: * does not cross /; ** has no special meaning.
type Query struct {
	Text     string
	PathGlob string
	Files    bool
	Limit    int
}

// Match retains its originating file version. Preview is bounded to 240 UTF-16 units.
type Match struct {
	Location Location `json:"location"`
	Preview  string   `json:"preview"`
}

// Results contain bounded matches and explicit partial-result information.
type Results struct {
	Matches []Match `json:"matches"`
	State   State   `json:"state"`
}
