package search

import (
	"context"
	"fmt"
	"strings"
)

// MaxIndexNameBytes is the longest index name.
const MaxIndexNameBytes = 64

// MaxMarkerBytes is the longest index marker.
const MaxMarkerBytes = 256

// IndexLimits bound the indexes of an Engine. Zero fields use the default.
type IndexLimits struct {
	// DocumentBytes is the largest document content an index accepts.
	DocumentBytes int `json:"documentBytes"`

	// Documents is the most documents in one index.
	Documents int `json:"documents"`

	// Bytes is the most document content in one index.
	Bytes int64 `json:"bytes"`

	// Results is the most matches one search returns.
	Results int `json:"results"`
}

// DefaultIndexLimits returns the default bounds of an index.
func DefaultIndexLimits() IndexLimits {
	return IndexLimits{
		DocumentBytes: 1 << 20,
		Documents:     100000,
		Bytes:         1 << 30,
		Results:       1000,
	}
}

func (l IndexLimits) withDefaults() IndexLimits {
	defaults := DefaultIndexLimits()

	if l.DocumentBytes <= 0 {
		l.DocumentBytes = defaults.DocumentBytes
	}
	if l.Documents <= 0 {
		l.Documents = defaults.Documents
	}
	if l.Bytes <= 0 {
		l.Bytes = defaults.Bytes
	}
	if l.Results <= 0 {
		l.Results = defaults.Results
	}
	return l
}

// Engine maintains named indexes in a Store and searches them. It applies
// the search rules and limits, so every store behaves the same. An Engine is
// safe for concurrent use.
//
// An index name is 1 to MaxIndexNameBytes bytes of a-z, 0-9, ".", "-" and
// "_", starts with a letter or digit and holds no "..". Any other name
// returns ErrInvalid without reaching the store. A missing index returns
// ErrNotFound.
type Engine struct {
	store  Store
	limits IndexLimits
}

// NewEngine returns an engine that keeps its indexes in store.
func NewEngine(store Store, limits IndexLimits) *Engine {
	return &Engine{store: store, limits: limits.withDefaults()}
}

// CreateIndex adds an empty index. A name in use returns ErrExists.
func (e *Engine) CreateIndex(ctx context.Context, name string) error {
	if err := checkIndexName(name); err != nil {
		return err
	}
	return e.store.CreateIndex(ctx, name)
}

// DeleteIndex removes an index and everything stored for it.
func (e *Engine) DeleteIndex(ctx context.Context, name string) error {
	if err := checkIndexName(name); err != nil {
		return err
	}
	return e.store.DeleteIndex(ctx, name)
}

// Indexes lists every index, ordered by name.
func (e *Engine) Indexes(ctx context.Context) ([]IndexInfo, error) {
	return e.store.Indexes(ctx)
}

// Index describes one index.
func (e *Engine) Index(ctx context.Context, name string) (IndexInfo, error) {
	if err := checkIndexName(name); err != nil {
		return IndexInfo{}, err
	}
	return e.store.Index(ctx, name)
}

// SetMarker replaces the marker of an index: a value of the caller's
// choosing, such as the revision the index was last synchronized to. It is
// valid UTF-8 without NUL, at most MaxMarkerBytes bytes.
func (e *Engine) SetMarker(ctx context.Context, name, marker string) error {
	if err := checkIndexName(name); err != nil {
		return err
	}
	if len(marker) > MaxMarkerBytes || !validName(marker) {
		return fmt.Errorf("index marker: %w", ErrInvalid)
	}
	return e.store.SetMarker(ctx, name, marker)
}

// Put adds a document to an index, replacing the document with the same
// kind, source and ID. A search sees the old document or the new one, never
// a mix.
//
// A bad identity returns ErrInvalid and content over
// IndexLimits.DocumentBytes returns ErrTooLarge. When the index would exceed
// IndexLimits.Documents or IndexLimits.Bytes, Put returns ErrFull and the
// index keeps what it had. The content of a binary document is not stored;
// searches count the document in State.Skipped.
func (e *Engine) Put(ctx context.Context, index string, document Document) error {
	if err := checkIndexName(index); err != nil {
		return err
	}
	if !validRef(document.Ref) {
		return fmt.Errorf("document: %w", ErrInvalid)
	}
	if len(document.Content) > e.limits.DocumentBytes {
		return fmt.Errorf("document: %w", ErrTooLarge)
	}

	capacity := Capacity{Documents: e.limits.Documents, Bytes: e.limits.Bytes}
	return e.store.Put(ctx, index, newEntry(document), capacity)
}

// Remove deletes the document with the kind, source and ID of ref from an
// index. A document the index does not hold returns ErrNotFound.
func (e *Engine) Remove(ctx context.Context, index string, ref Ref) error {
	if err := checkIndexName(index); err != nil {
		return err
	}
	if !validRef(ref) {
		return fmt.Errorf("document: %w", ErrInvalid)
	}
	return e.store.Remove(ctx, index, ref)
}

// Documents lists the identity and version of the documents the filter
// selects, in match order, without their content.
func (e *Engine) Documents(ctx context.Context, index string, filter Filter) ([]Ref, error) {
	if err := checkSelection(index, filter); err != nil {
		return nil, err
	}
	return e.refs(ctx, index, Selection{Filter: filter})
}

func (e *Engine) refs(ctx context.Context, index string, selection Selection) ([]Ref, error) {
	refs := []Ref{}

	err := e.store.Refs(ctx, index, selection, func(ref Ref) bool {
		refs = append(refs, ref)
		return true
	})
	if err != nil {
		return nil, err
	}
	return refs, nil
}

func checkSelection(index string, filter Filter) error {
	if err := checkIndexName(index); err != nil {
		return err
	}
	if !filter.valid() {
		return fmt.Errorf("filter: %w", ErrInvalid)
	}
	return nil
}

func checkIndexName(name string) error {
	if !validIndexName(name) {
		return fmt.Errorf("index name: %w", ErrInvalid)
	}
	return nil
}

func validIndexName(name string) bool {
	if name == "" || len(name) > MaxIndexNameBytes || strings.Contains(name, "..") {
		return false
	}
	if !alphanumeric(rune(name[0])) {
		return false
	}
	return !strings.ContainsFunc(name, func(char rune) bool {
		return !alphanumeric(char) && !strings.ContainsRune("._-", char)
	})
}

func alphanumeric(char rune) bool {
	return char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
}
