// Package symbol makes the definitions in code documents searchable by name:
// functions, methods, types and the like, with their location and
// declaration line. Definitions come from the forge-symbols library.
//
// Search analyzes the documents a call carries and keeps nothing. Update
// stores the symbols of changed documents in an index of the search package,
// and drops those of removed ones; SearchIndex searches what it stored. Both
// modes return the same symbols in the same order.
//
// Code is untrusted. Documents, bytes, symbols per document, results and
// previews are bounded, and errors never contain document content.
package symbol

import (
	"context"

	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

// KindSymbol is the kind of the index documents that hold the symbols of one
// code document. Literal searches that should not see them filter by kind.
const KindSymbol search.Kind = "symbol"

// MaxNameBytes is the longest symbol name, kind or container. Definitions
// with longer ones are left out.
const MaxNameBytes = 256

// MaxRecordBytes is the most bytes the symbols of one document take in an
// index. Documents whose symbols take more are left out and counted in
// Skipped.TooLarge. An index must accept documents of this size.
const MaxRecordBytes = 1 << 20

// Analyzer finds the definitions of one source file. *symbols.Analyzer
// implements it; its owner creates it once, shares it and closes it. Tests
// may fake it.
//
// Analyze returns symbols.ErrUnsupported, ErrTooLarge, ErrTimeout and
// ErrMemory for files it cannot analyze, and ctx.Err() when ctx ends.
type Analyzer interface {
	Analyze(ctx context.Context, file symbols.File) (symbols.Analysis, error)
}

// Symbol is one definition in a code document.
type Symbol struct {
	// Document is the code document; its ID is the path. Version is the
	// document's, as given.
	Document search.Ref `json:"document"`

	Name string `json:"name"`

	// Kind is the forge-symbols kind, such as function, method, class or
	// type.
	Kind string `json:"kind"`

	// Language is the forge-symbols language, such as go or python.
	Language string `json:"language"`

	// NameRange covers the name; Range covers the whole declaration with
	// its body. Both follow the rules of search.Range.
	NameRange search.Range `json:"nameRange"`
	Range     search.Range `json:"range"`

	// Container names what the definition belongs to, such as the receiver
	// type of a method or the class around it. It is empty at the top level.
	Container string `json:"container,omitempty"`

	// Preview is the declaration line: the source line of the name, cut to
	// the declaration where it starts or ends on that line, without leading
	// blanks. It follows the rules of search.Match.Preview, with the name
	// as the match, so it shows the name even on long lines.
	Preview string `json:"preview"`

	// PreviewColumn is the UTF-16 column of the line where the preview text
	// starts, after any leading "…".
	PreviewColumn int `json:"previewColumn"`
}

// Filter selects symbols. Empty fields select everything.
type Filter struct {
	// Source selects the documents of one source, such as a checkout.
	Source string `json:"source,omitempty"`

	// Path selects one document; PathPrefix those whose path starts with it.
	Path       string `json:"path,omitempty"`
	PathPrefix string `json:"pathPrefix,omitempty"`

	// Kind selects one symbol kind, such as function.
	Kind string `json:"kind,omitempty"`

	// Language selects one language, such as go.
	Language string `json:"language,omitempty"`
}

// Query selects symbols by name. Matching is literal and case-sensitive.
type Query struct {
	// Text follows the rules of search.Query.Text. A symbol matches when
	// its name contains it.
	Text string `json:"text"`

	// Exact matches only symbols named Text.
	Exact bool `json:"exact,omitempty"`

	Filter Filter `json:"filter"`

	// Limit is the most symbols to return. Zero uses Limits.Results; more
	// than Limits.Results is invalid.
	Limit int `json:"limit,omitempty"`
}

// Limits bound one call. Zero fields use the default: DefaultLimits for
// searches, DefaultUpdateLimits for Update.
type Limits struct {
	// FileBytes is the largest document analyzed. Larger ones are left out
	// and counted in Skipped.TooLarge.
	FileBytes int `json:"fileBytes"`

	// Files is the most documents one call analyzes, and the most paths one
	// Update removes. More return search.ErrTooLarge.
	Files int `json:"files"`

	// TotalBytes is the most content one call analyzes. More return
	// search.ErrTooLarge.
	TotalBytes int `json:"totalBytes"`

	// Symbols is the most definitions of one document. Documents with more
	// are left out and counted in Skipped.TooLarge.
	Symbols int `json:"symbols"`

	// Results is the most symbols one search returns. More set
	// State.Truncated.
	Results int `json:"results"`
}

// DefaultLimits returns the bounds of one search: 1 MiB per document, 1000
// documents and 16 MiB in all, as in forge-symbols, 10000 symbols per
// document and 1000 results.
func DefaultLimits() Limits {
	return Limits{FileBytes: 1 << 20, Files: 1000, TotalBytes: 16 << 20, Symbols: 10000, Results: 1000}
}

// DefaultUpdateLimits returns the bounds of one update: as DefaultLimits,
// but 10000 documents and 256 MiB in all.
func DefaultUpdateLimits() Limits {
	return Limits{FileBytes: 1 << 20, Files: 10000, TotalBytes: 256 << 20, Symbols: 10000, Results: 1000}
}

func (l Limits) withDefaults(defaults Limits) Limits {
	if l.FileBytes <= 0 {
		l.FileBytes = defaults.FileBytes
	}
	if l.Files <= 0 {
		l.Files = defaults.Files
	}
	if l.TotalBytes <= 0 {
		l.TotalBytes = defaults.TotalBytes
	}
	if l.Symbols <= 0 {
		l.Symbols = defaults.Symbols
	}
	if l.Results <= 0 {
		l.Results = defaults.Results
	}
	return l
}

// Skipped counts the documents whose symbols were left out.
type Skipped struct {
	// Unsupported counts documents in a language forge-symbols does not
	// support, by their path, and binary documents: those with a NUL byte
	// or invalid UTF-8.
	Unsupported int `json:"unsupported"`

	// TooLarge counts documents over Limits.FileBytes, Limits.Symbols or
	// MaxRecordBytes, or over a size limit of the analyzer.
	TooLarge int `json:"tooLarge"`

	// Failed counts documents the analyzer gave up on: those over its parse
	// time or memory limit.
	Failed int `json:"failed"`
}

// State describes an incomplete search.
type State struct {
	// Truncated is set when a symbol was not returned because of the
	// result limit.
	Truncated bool `json:"truncated"`

	// Canceled is set when the context ended before the search did.
	Canceled bool `json:"canceled"`
}

// Results hold the symbols in match order and the state of the search.
type Results struct {
	Symbols []Symbol `json:"symbols"`
	State   State    `json:"state"`

	// Skipped counts the documents an unindexed search left out, up to
	// where it stopped. Indexed searches leave it zero; Update reports them
	// instead.
	Skipped Skipped `json:"skipped"`
}
