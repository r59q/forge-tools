// Package navigation answers IDE-style questions over files analyzed by
// package symbols: what a definition calls (Outgoing), what refers to it
// (Incoming), and where a name is defined (Definitions).
//
// It works on the analyses the caller supplies. It reads no files, keeps no
// index and caches nothing. Results match names, not resolved symbols, so
// every result is labelled Heuristic.
package navigation

import (
	"errors"

	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

// Stable error categories. Match them with errors.Is. Requests over a size
// limit return symbols.ErrTooLarge.
var (
	// ErrInvalidTarget reports a target with neither a name nor a path and
	// position.
	ErrInvalidTarget = errors.New("invalid target")

	// ErrNotFound reports a position that matches no definition or reference.
	ErrNotFound = errors.New("target not found")
)

// Precision tells how a result was found.
type Precision string

// Heuristic marks a result matched by name. Overloads and same-named
// symbols in different containers cannot be told apart.
const Heuristic Precision = "heuristic"

// Tier ranks a result by how close it is to the target. Lower tiers come
// first.
type Tier int

// Ranking tiers, in order.
const (
	// TierFile is a result in the target's file.
	TierFile Tier = 1

	// TierContainer is a result in another file whose container is the
	// target's container.
	TierContainer Tier = 2

	// TierOther is any other result.
	TierOther Tier = 3
)

// DefaultResults is the default of Limits.Results.
const DefaultResults = 1000

// Limits bound one request. Zero fields use the default.
type Limits struct {
	// Files is the most analyses in one request. The default is that of
	// symbols.Limits.
	Files int `json:"files"`

	// TotalBytes is the most analyzed content, summed over
	// symbols.Analysis.Bytes. The default is that of symbols.Limits.
	TotalBytes int `json:"totalBytes"`

	// Results is the most definitions and references in a result, group
	// heads included. The default is DefaultResults.
	Results int `json:"results"`
}

// DefinitionResult is a definition found in one file.
type DefinitionResult struct {
	Path string `json:"path"`

	// Index is the index in symbols.Analysis.Definitions, or symbols.None
	// for the file level.
	Index int `json:"index"`

	// Definition is zero for the file level.
	Definition symbols.Definition `json:"definition"`

	Tier      Tier      `json:"tier"`
	Precision Precision `json:"precision"`
}

// ReferenceResult is a reference found in one file.
type ReferenceResult struct {
	Path string `json:"path"`

	// Index is the index in symbols.Analysis.References.
	Index     int               `json:"index"`
	Reference symbols.Reference `json:"reference"`

	// Nested reports, in Outgoing, a reference inside a definition nested
	// in the target, such as a local function.
	Nested bool `json:"nested,omitempty"`

	Precision Precision `json:"precision"`
}

func (l Limits) withDefaults() Limits {
	defaults := symbols.DefaultLimits()

	if l.Files <= 0 {
		l.Files = defaults.Files
	}
	if l.TotalBytes <= 0 {
		l.TotalBytes = defaults.TotalBytes
	}
	if l.Results <= 0 {
		l.Results = DefaultResults
	}
	return l
}
