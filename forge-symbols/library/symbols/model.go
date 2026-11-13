// Package symbols finds definitions and references in source files with
// Tree-sitter tag queries, the way an editor outlines code.
//
// Analysis is heuristic and stateless. It never builds, type-checks or runs
// the code, and reads nothing but the bytes it is given.
package symbols

import (
	"errors"
	"time"

	"github.com/r59q/forge-tools/forge-symbols/library/internal/treesitter"
)

// Stable error categories. Match them with errors.Is; their messages never
// contain paths or source text.
var (
	// ErrUnsupported reports a file whose language is unknown or not bundled.
	ErrUnsupported = errors.New("unsupported language")

	// ErrTooLarge reports a file or request over a size or count limit.
	ErrTooLarge = errors.New("content exceeds limit")

	// ErrTimeout reports a file that took longer than Limits.ParseTime.
	ErrTimeout = errors.New("analysis exceeds time limit")

	// ErrMemory reports a file that needed more than Limits.MemoryBytes.
	ErrMemory = treesitter.ErrMemory
)

// None is the index of a missing parent or enclosing definition.
const None = -1

// MinMemoryBytes is the least memory a parser needs to start and be useful.
const MinMemoryBytes = 4 << 20

// Limits bound the work of one request. Zero fields use the default.
type Limits struct {
	// FileBytes is the largest file content.
	FileBytes int `json:"fileBytes"`

	// Files is the most files in one AnalyzeFiles call.
	Files int `json:"files"`

	// TotalBytes is the most content in one AnalyzeFiles call.
	TotalBytes int `json:"totalBytes"`

	// ParseTime is the longest time to parse and query one file.
	ParseTime time.Duration `json:"parseTime"`

	// MemoryBytes is the most memory one parse may use. Values below
	// MinMemoryBytes are raised to it.
	MemoryBytes int `json:"memoryBytes"`
}

// DefaultLimits returns the default resource budget.
func DefaultLimits() Limits {
	return Limits{
		FileBytes:   1 << 20,
		Files:       1000,
		TotalBytes:  16 << 20,
		ParseTime:   5 * time.Second,
		MemoryBytes: 256 << 20,
	}
}

// File is one source file to analyze. Language overrides detection from the
// extension of Path.
type File struct {
	Path     string `json:"path"`
	Content  []byte `json:"content"`
	Language string `json:"language,omitempty"`
}

// Range uses one-based lines and UTF-16 columns, with an exclusive end column.
type Range struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
	EndLine     int `json:"endLine"`
	EndColumn   int `json:"endColumn"`
}

// Definition is a named declaration, such as a function or a class.
type Definition struct {
	Name string `json:"name"`

	// Kind is the suffix of the query's @definition capture, such as
	// function, method, class, interface, type, field, constant or variable.
	Kind string `json:"kind"`

	// NameRange covers the name; Range covers the whole declaration with its body.
	NameRange Range `json:"nameRange"`
	Range     Range `json:"range"`

	// Container names what the definition belongs to: the receiver type of
	// a method, or else the parent definition. It is empty at the top level.
	Container string `json:"container,omitempty"`

	// Parent is the index in Analysis.Definitions of the innermost
	// definition whose range encloses this one, or None.
	Parent int `json:"parent"`
}

// Reference is a use of a name, such as a call.
type Reference struct {
	Name string `json:"name"`

	// Kind is the suffix of the query's @reference capture, such as call or type.
	Kind string `json:"kind"`

	// Range covers the name.
	Range Range `json:"range"`

	// Enclosing is the index in Analysis.Definitions of the innermost
	// definition whose range contains the reference, or None.
	Enclosing int `json:"enclosing"`
}

// Analysis holds the definitions and references of one file, each in
// source order. Bytes is the length of the analyzed content.
type Analysis struct {
	Path        string       `json:"path"`
	Language    string       `json:"language"`
	Bytes       int          `json:"bytes"`
	Definitions []Definition `json:"definitions"`
	References  []Reference  `json:"references"`
}

// Parent returns the definition that encloses d, such as the class of a
// nested method.
func (a Analysis) Parent(d Definition) (Definition, bool) {
	return a.definition(d.Parent)
}

// Enclosing returns the definition that contains r.
func (a Analysis) Enclosing(r Reference) (Definition, bool) {
	return a.definition(r.Enclosing)
}

func (a Analysis) definition(index int) (Definition, bool) {
	if index < 0 || index >= len(a.Definitions) {
		return Definition{}, false
	}
	return a.Definitions[index], true
}

func (l Limits) withDefaults() Limits {
	defaults := DefaultLimits()

	if l.FileBytes <= 0 {
		l.FileBytes = defaults.FileBytes
	}
	if l.Files <= 0 {
		l.Files = defaults.Files
	}
	if l.TotalBytes <= 0 {
		l.TotalBytes = defaults.TotalBytes
	}
	if l.ParseTime <= 0 {
		l.ParseTime = defaults.ParseTime
	}
	if l.MemoryBytes <= 0 {
		l.MemoryBytes = defaults.MemoryBytes
	}
	l.MemoryBytes = max(l.MemoryBytes, MinMemoryBytes)
	return l
}
