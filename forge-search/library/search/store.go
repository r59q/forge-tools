package search

import (
	"context"
	"errors"
	"strings"
)

// Storage error categories. Match them with errors.Is.
var (
	// ErrNotFound reports a missing index or document.
	ErrNotFound = errors.New("not found")

	// ErrExists reports an index name that is already taken.
	ErrExists = errors.New("index exists")

	// ErrFull reports a document that does not fit in its index.
	ErrFull = errors.New("index is full")
)

// Term is an opaque key the engine derives from document content. A store
// keeps the terms of each document and selects documents by them; it never
// needs to know what a term means.
type Term uint32

// IndexInfo describes one index.
type IndexInfo struct {
	Name string `json:"name"`

	// Marker is a small value the caller sets, such as the revision the
	// index was last synchronized to.
	Marker string `json:"marker,omitempty"`

	// Documents is the number of documents in the index.
	Documents int `json:"documents"`

	// Bytes is the total content size of those documents.
	Bytes int64 `json:"bytes"`
}

// Filter selects documents of an index. Empty fields select everything.
type Filter struct {
	Kind   Kind   `json:"kind,omitempty"`
	Source string `json:"source,omitempty"`

	// ID selects the one document with this ID.
	ID string `json:"id,omitempty"`

	// IDPrefix selects documents whose ID starts with it, such as the files
	// below a directory.
	IDPrefix string `json:"idPrefix,omitempty"`
}

// Matches reports whether the filter selects the document.
func (f Filter) Matches(ref Ref) bool {
	return (f.Kind == "" || f.Kind == ref.Kind) &&
		(f.Source == "" || f.Source == ref.Source) &&
		(f.ID == "" || f.ID == ref.ID) &&
		strings.HasPrefix(ref.ID, f.IDPrefix)
}

func (f Filter) valid() bool {
	return validName(string(f.Kind)) && validName(f.Source) && validName(f.ID) && validName(f.IDPrefix)
}

// Selection picks stored documents: those the filter selects that hold every
// one of the terms. Without terms the filter alone decides.
type Selection struct {
	Filter

	Terms []Term
}

// Entry is one document as it is stored: its content and its terms.
type Entry struct {
	Document Document

	// Terms are sorted and distinct.
	Terms []Term
}

// Capacity bounds one index.
type Capacity struct {
	// Documents is the most documents the index may hold.
	Documents int

	// Bytes is the most content the index may hold.
	Bytes int64
}

// Store keeps the documents of named indexes for an Engine. It needs no
// knowledge of the search rules: it stores entries, selects them by identity
// and terms, and returns them in match order.
//
// The engine validates index names, identities and sizes before it calls a
// store. Implementations must be safe for concurrent use, copy the content
// they are given, and return an error when the context has ended. A method
// that names a missing index returns ErrNotFound. Errors never contain
// document content.
//
// Every method is atomic: a reader sees an entry entirely as it was before a
// Put or Remove, or entirely as it is after, content and terms together. One
// transaction per method call is enough.
type Store interface {
	// CreateIndex adds an empty index, or returns ErrExists.
	CreateIndex(ctx context.Context, name string) error

	// DeleteIndex removes an index with its entries and terms.
	DeleteIndex(ctx context.Context, name string) error

	// Indexes returns every index, ordered by name.
	Indexes(ctx context.Context) ([]IndexInfo, error)

	// Index returns one index.
	Index(ctx context.Context, name string) (IndexInfo, error)

	// SetMarker replaces the marker of an index.
	SetMarker(ctx context.Context, name, marker string) error

	// Put stores an entry, replacing the content and terms of the entry
	// with the same kind, source and ID. When the index would then exceed
	// the capacity, it returns ErrFull and changes nothing.
	Put(ctx context.Context, index string, entry Entry, capacity Capacity) error

	// Remove deletes the entry with the kind, source and ID of ref, with
	// its content and terms. A missing entry returns ErrNotFound.
	Remove(ctx context.Context, index string, ref Ref) error

	// Refs calls visit with the identity and version of each selected
	// entry, ordered by kind, source and ID, compared as bytes, until visit
	// returns false.
	Refs(ctx context.Context, index string, selection Selection, visit func(Ref) bool) error

	// Documents calls visit with each selected document, in the order of
	// Refs, until visit returns false. The content is only valid during
	// the call and must not be changed.
	Documents(ctx context.Context, index string, selection Selection, visit func(Document) bool) error
}
