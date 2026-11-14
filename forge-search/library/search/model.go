// Package search finds literal text in documents of several aggregates, such
// as code and issues.
//
// The rules for queries, ranges, previews and match order are the contract of
// every search mode. Unindexed searches the documents a call carries; it reads
// and stores nothing else.
package search

import "errors"

// Stable error categories. Match them with errors.Is; their messages never
// contain document content.
var (
	// ErrInvalid reports a malformed query, document identity or limit.
	ErrInvalid = errors.New("invalid search request")

	// ErrTooLarge reports a document or request over a size or count limit.
	ErrTooLarge = errors.New("content exceeds limit")
)

// MaxQueryBytes is the longest query text.
const MaxQueryBytes = 256

// MaxNameBytes is the longest kind, source, ID or version of a document.
const MaxNameBytes = 4096

// PreviewUnits is the most UTF-16 units of line text in a preview.
const PreviewUnits = 240

// Kind is the aggregate a document belongs to. Values are lower-case words.
type Kind string

// Known aggregates. Other non-empty kinds are accepted as they are.
const (
	// KindCode is source or text content, such as a file in a checkout.
	KindCode Kind = "code"

	// KindIssue is the text of an issue or one of its comments.
	KindIssue Kind = "issue"
)

// Ref identifies a document without its content. Kind, Source and ID together
// are unique; nothing in them has to be a file on disk.
type Ref struct {
	// Kind is the aggregate, such as KindCode. It is required.
	Kind Kind `json:"kind"`

	// Source is the collection the document belongs to, such as a checkout or
	// a repository. It may be empty.
	Source string `json:"source,omitempty"`

	// ID is the stable identity within Kind and Source, such as a path or an
	// issue number. It is required.
	ID string `json:"id"`

	// Version identifies the searched content, such as a commit or an ETag.
	// It is returned as given and may be empty.
	Version string `json:"version,omitempty"`
}

// Document is one text to search, with its identity.
type Document struct {
	Ref

	// Content is the text. Documents that hold a NUL byte or are not valid
	// UTF-8 are skipped and counted in State.Skipped.
	Content []byte `json:"content"`
}

// Query selects literal, case-sensitive text.
type Query struct {
	// Text is required, valid UTF-8 of at most MaxQueryBytes, without NUL,
	// "\r" or "\n".
	Text string `json:"text"`

	// Limit is the most matches to return. Zero uses Limits.Results; more
	// than Limits.Results is invalid.
	Limit int `json:"limit,omitempty"`
}

// Range uses one-based lines and UTF-16 columns, with an exclusive end column.
// Lines end at "\n".
type Range struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
	EndLine     int `json:"endLine"`
	EndColumn   int `json:"endColumn"`
}

// Match is one occurrence of the query text.
type Match struct {
	// Document is the identity of the matching document.
	Document Ref `json:"document"`

	// Range covers the matched text.
	Range Range `json:"range"`

	// Preview is the text of the matching line, at most PreviewUnits UTF-16
	// units, without a trailing "\r". Text cut at either end is marked with
	// "…". It shows the match even on long lines.
	Preview string `json:"preview"`

	// PreviewColumn is the UTF-16 column of the line where the preview text
	// starts, after any leading "…".
	PreviewColumn int `json:"previewColumn"`
}

// State describes an incomplete search.
type State struct {
	// Truncated is set when a match was not returned because of the result
	// limit, or a document was not scanned because of the total byte limit.
	Truncated bool `json:"truncated"`

	// Canceled is set when the context ended before the search did.
	Canceled bool `json:"canceled"`

	// Skipped counts documents not searched because they hold a NUL byte or
	// are not valid UTF-8.
	Skipped int `json:"skipped"`
}

// Results hold the matches in match order and the state of the search.
type Results struct {
	Matches []Match `json:"matches"`
	State   State   `json:"state"`
}

// Limits bound the work of one search. Zero fields use the default.
type Limits struct {
	// DocumentBytes is the largest document content.
	DocumentBytes int `json:"documentBytes"`

	// Documents is the most documents in one search.
	Documents int `json:"documents"`

	// TotalBytes is the most content one search scans.
	TotalBytes int `json:"totalBytes"`

	// Results is the most matches one search returns.
	Results int `json:"results"`
}

// DefaultLimits returns the default resource budget.
func DefaultLimits() Limits {
	return Limits{
		DocumentBytes: 1 << 20,
		Documents:     10000,
		TotalBytes:    64 << 20,
		Results:       1000,
	}
}

func (l Limits) withDefaults() Limits {
	defaults := DefaultLimits()

	if l.DocumentBytes <= 0 {
		l.DocumentBytes = defaults.DocumentBytes
	}
	if l.Documents <= 0 {
		l.Documents = defaults.Documents
	}
	if l.TotalBytes <= 0 {
		l.TotalBytes = defaults.TotalBytes
	}
	if l.Results <= 0 {
		l.Results = defaults.Results
	}
	return l
}
