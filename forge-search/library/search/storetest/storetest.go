// Package storetest provides the shared search.Store contract tests and
// benchmark. A storage adapter passes its store to Run to prove that an
// Engine behaves on it as it does on search.MemoryStore.
package storetest

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// Open returns a new, empty store. It is called once for every test, so the
// tests do not see each other's data.
type Open func(testing.TB) search.Store

// Run checks the search.Store contract and the behavior of an Engine on the
// store: results equal to the unindexed search, consistent results during
// concurrent updates, and no trace of removed content.
func Run(t *testing.T, open Open) {
	steps := []struct {
		name string
		step func(*run)
	}{
		{"indexes", (*run).indexes},
		{"markers", (*run).markers},
		{"entries", (*run).entries},
		{"order", (*run).order},
		{"selection", (*run).selection},
		{"capacity", (*run).capacity},
		{"removed content", (*run).removedContent},
		{"canceled context", (*run).canceled},
		{"atomic updates", (*run).atomicUpdates},
		{"mode equivalence", (*run).equivalence},
		{"concurrent searches", (*run).concurrentSearches},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			step.step(&run{t: t, ctx: context.Background(), store: open(t)})
		})
	}
}

// mainIndex is the index most steps work on.
const mainIndex = "main"

// run holds the state of one contract step.
type run struct {
	t     *testing.T
	ctx   context.Context
	store search.Store
}

// roomy is a capacity no step reaches by accident.
func roomy() search.Capacity {
	return search.Capacity{Documents: 1 << 20, Bytes: 1 << 40}
}

func entry(id, version, content string, terms ...search.Term) search.Entry {
	return search.Entry{
		Document: search.Document{
			Ref:     search.Ref{Kind: search.KindCode, Source: "checkout", ID: id, Version: version},
			Content: []byte(content),
		},
		Terms: terms,
	}
}

func terms(values ...search.Term) search.Selection {
	return search.Selection{Terms: values}
}

func (r *run) wantErr(err, want error) {
	r.t.Helper()
	if !errors.Is(err, want) {
		r.t.Fatalf("error = %v, want %v", err, want)
	}
}

func (r *run) create(names ...string) {
	r.t.Helper()
	for _, name := range names {
		r.wantErr(r.store.CreateIndex(r.ctx, name), nil)
	}
}

func (r *run) put(index string, entries ...search.Entry) {
	r.t.Helper()
	for _, stored := range entries {
		r.wantErr(r.store.Put(r.ctx, index, stored, roomy()), nil)
	}
}

func (r *run) info(index string) search.IndexInfo {
	r.t.Helper()
	info, err := r.store.Index(r.ctx, index)
	r.wantErr(err, nil)
	return info
}

func (r *run) wantInfo(index string, want search.IndexInfo) {
	r.t.Helper()
	if got := r.info(index); got != want {
		r.t.Fatalf("index = %+v, want %+v", got, want)
	}
}

func (r *run) refs(index string, selection search.Selection) []search.Ref {
	r.t.Helper()
	var refs []search.Ref

	err := r.store.Refs(r.ctx, index, selection, func(ref search.Ref) bool {
		refs = append(refs, ref)
		return true
	})
	r.wantErr(err, nil)
	return refs
}

// documents returns copies of the selected documents.
func (r *run) documents(index string, selection search.Selection) []search.Document {
	r.t.Helper()
	var documents []search.Document

	err := r.store.Documents(r.ctx, index, selection, func(document search.Document) bool {
		document.Content = bytes.Clone(document.Content)
		documents = append(documents, document)
		return true
	})
	r.wantErr(err, nil)
	return documents
}

// wantIDs checks which documents a selection picks, and that Refs and
// Documents agree on them and their order.
func (r *run) wantIDs(index string, selection search.Selection, want ...string) {
	r.t.Helper()

	var listed, read []string
	for _, ref := range r.refs(index, selection) {
		listed = append(listed, ref.ID)
	}
	for _, document := range r.documents(index, selection) {
		read = append(read, document.ID)
	}
	if !slices.Equal(listed, want) || !slices.Equal(read, want) {
		r.t.Fatalf("selection %+v: refs %q, documents %q, want %q", selection, listed, read, want)
	}
}
