package storetest

import (
	"context"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// indexes checks creating, listing and deleting indexes.
func (r *run) indexes() {
	ctx, s := r.ctx, r.store

	if got, err := s.Indexes(ctx); err != nil || len(got) != 0 {
		r.t.Fatalf("Indexes = %+v, %v, want none", got, err)
	}

	r.create("b", "a")
	r.wantErr(s.CreateIndex(ctx, "a"), search.ErrExists)
	r.put("a", entry("x", "v1", "content", 1))

	got, err := s.Indexes(ctx)
	want := []search.IndexInfo{{Name: "a", Documents: 1, Bytes: 7}, {Name: "b"}}
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		r.t.Fatalf("Indexes = %+v, %v, want %+v", got, err, want)
	}

	r.missingIndex()

	r.wantErr(s.DeleteIndex(ctx, "a"), nil)
	r.wantErr(s.DeleteIndex(ctx, "a"), search.ErrNotFound)
	if got, err := s.Indexes(ctx); err != nil || len(got) != 1 || got[0] != want[1] {
		r.t.Fatalf("Indexes = %+v, %v, want %+v", got, err, want[1:])
	}

	r.create("a")
	r.wantInfo("a", search.IndexInfo{Name: "a"})
	r.wantIDs("a", search.Selection{})
}

// missingIndex checks that every method reports a missing index.
func (r *run) missingIndex() {
	ctx, s := r.ctx, r.store
	const missing = "missing"

	_, err := s.Index(ctx, missing)
	r.wantErr(err, search.ErrNotFound)
	r.wantErr(s.DeleteIndex(ctx, missing), search.ErrNotFound)
	r.wantErr(s.SetMarker(ctx, missing, "marker"), search.ErrNotFound)
	r.wantErr(s.Put(ctx, missing, entry("x", "v1", "content"), roomy()), search.ErrNotFound)
	r.wantErr(s.Remove(ctx, missing, entry("x", "", "").Document.Ref), search.ErrNotFound)
	r.wantErr(s.Refs(ctx, missing, search.Selection{}, func(search.Ref) bool { return true }), search.ErrNotFound)
	r.wantErr(s.Documents(ctx, missing, search.Selection{}, func(search.Document) bool { return true }), search.ErrNotFound)
}

// markers checks that a marker belongs to one index and outlives updates.
func (r *run) markers() {
	ctx, s := r.ctx, r.store
	r.create("a", "b")

	r.wantErr(s.SetMarker(ctx, "a", "revision-1"), nil)
	r.put("a", entry("x", "v1", "content", 1))
	r.wantErr(s.Remove(ctx, "a", entry("x", "", "").Document.Ref), nil)

	r.wantInfo("a", search.IndexInfo{Name: "a", Marker: "revision-1"})
	r.wantInfo("b", search.IndexInfo{Name: "b"})

	r.wantErr(s.SetMarker(ctx, "a", ""), nil)
	r.wantInfo("a", search.IndexInfo{Name: "a"})
}

// entries checks adding, replacing and removing entries.
func (r *run) entries() {
	ctx, s := r.ctx, r.store
	r.create(mainIndex, "other")

	first := entry("x", "v1", "one", 1, 2)
	r.put(mainIndex, first)
	r.put("other", entry("x", "elsewhere", "untouched", 1))
	first.Document.Content[0] = 'X'
	first.Terms[0] = 9

	r.wantDocument(mainIndex, terms(1, 2), "v1", "one")
	r.wantIDs(mainIndex, terms(9))
	r.wantInfo(mainIndex, search.IndexInfo{Name: mainIndex, Documents: 1, Bytes: 3})

	r.put(mainIndex, entry("x", "v2", "three!", 2, 3))
	r.wantDocument(mainIndex, terms(2, 3), "v2", "three!")
	r.wantIDs(mainIndex, terms(1))
	r.wantInfo(mainIndex, search.IndexInfo{Name: mainIndex, Documents: 1, Bytes: 6})

	ref := first.Document.Ref
	r.wantErr(s.Remove(ctx, mainIndex, ref), nil)
	r.wantErr(s.Remove(ctx, mainIndex, ref), search.ErrNotFound)
	r.wantIDs(mainIndex, search.Selection{})
	r.wantIDs(mainIndex, terms(2))
	r.wantInfo(mainIndex, search.IndexInfo{Name: mainIndex})

	r.wantDocument("other", terms(1), "elsewhere", "untouched")
}

// wantDocument checks that a selection picks one document with this version
// and content.
func (r *run) wantDocument(index string, selection search.Selection, version, content string) {
	r.t.Helper()

	refs := r.refs(index, selection)
	documents := r.documents(index, selection)
	if len(refs) != 1 || len(documents) != 1 {
		r.t.Fatalf("selection %+v: %d refs, %d documents, want one", selection, len(refs), len(documents))
	}
	if refs[0] != documents[0].Ref || refs[0].Version != version || string(documents[0].Content) != content {
		r.t.Fatalf("selection %+v: version %q with %d bytes, want version %q", selection, refs[0].Version, len(documents[0].Content), version)
	}
}

// order checks that entries come ordered by kind, source and ID as bytes.
func (r *run) order() {
	r.create(mainIndex)

	sorted := []search.Ref{
		{Kind: "code", ID: "b"},
		{Kind: "code", Source: "a", ID: "Z"},
		{Kind: "code", Source: "a", ID: "a"},
		{Kind: "code", Source: "a", ID: "a/b"},
		{Kind: "code", Source: "a", ID: "ab"},
		{Kind: "code", Source: "a", ID: "é"},
		{Kind: "code", Source: "b", ID: "a"},
		{Kind: "issue", Source: "a", ID: "10"},
		{Kind: "issue", Source: "a", ID: "9"},
	}
	for _, position := range []int{4, 8, 0, 6, 2, 7, 1, 5, 3} {
		r.put(mainIndex, search.Entry{Document: search.Document{Ref: sorted[position]}})
	}

	got := r.refs(mainIndex, search.Selection{})
	documents := r.documents(mainIndex, search.Selection{})
	if len(got) != len(sorted) || len(documents) != len(sorted) {
		r.t.Fatalf("%d refs, %d documents, want %d", len(got), len(documents), len(sorted))
	}
	for position, want := range sorted {
		if got[position] != want || documents[position].Ref != want {
			r.t.Fatalf("position %d: ref %+v, document %+v, want %+v", position, got[position], documents[position].Ref, want)
		}
	}
}

// selection checks filters, terms and stopping early.
func (r *run) selection() {
	const (
		sourceA = "src/a.go"
		sourceB = "src/b.go"
		manual  = "docs/a.md"
		issue   = "src/1"
	)

	r.create(mainIndex)
	r.put(mainIndex,
		entry(sourceA, "v1", "a", 1, 2, 3),
		entry(sourceB, "v1", "b", 2, 3),
		entry(manual, "v1", "c", 3),
		search.Entry{
			Document: search.Document{Ref: search.Ref{Kind: search.KindIssue, Source: "forge", ID: issue}},
			Terms:    []search.Term{1, 3},
		},
	)

	code := search.Filter{Kind: search.KindCode}
	checks := []struct {
		selection search.Selection
		want      []string
	}{
		{search.Selection{}, []string{manual, sourceA, sourceB, issue}},
		{search.Selection{Filter: code}, []string{manual, sourceA, sourceB}},
		{search.Selection{Filter: search.Filter{Source: "forge"}}, []string{issue}},
		{search.Selection{Filter: search.Filter{ID: sourceA}}, []string{sourceA}},
		{search.Selection{Filter: search.Filter{ID: "src/a"}}, nil},
		{search.Selection{Filter: search.Filter{IDPrefix: "src/"}}, []string{sourceA, sourceB, issue}},
		{search.Selection{Filter: search.Filter{Kind: search.KindCode, Source: "checkout", IDPrefix: "src/"}}, []string{sourceA, sourceB}},
		{search.Selection{Filter: search.Filter{ID: sourceA, IDPrefix: "docs/"}}, nil},
		{terms(3), []string{manual, sourceA, sourceB, issue}},
		{terms(3, 1), []string{sourceA, issue}},
		{terms(1, 2, 3), []string{sourceA}},
		{terms(1, 4), nil},
		{terms(4), nil},
		{search.Selection{Filter: code, Terms: []search.Term{1}}, []string{sourceA}},
	}
	for _, check := range checks {
		r.wantIDs(mainIndex, check.selection, check.want...)
	}

	r.stopsEarly()
}

// stopsEarly checks that a visit ends when the visitor returns false.
func (r *run) stopsEarly() {
	visits := 0
	err := r.store.Refs(r.ctx, mainIndex, search.Selection{}, func(search.Ref) bool {
		visits++
		return false
	})
	r.wantErr(err, nil)

	err = r.store.Documents(r.ctx, mainIndex, search.Selection{}, func(search.Document) bool {
		visits++
		return false
	})
	r.wantErr(err, nil)

	if visits != 2 {
		r.t.Fatalf("%d visits after stopping, want 2", visits)
	}
}

// capacity checks that a full index rejects an entry and keeps what it had.
func (r *run) capacity() {
	ctx, s := r.ctx, r.store
	r.create(mainIndex)
	limit := search.Capacity{Documents: 2, Bytes: 10}

	r.wantErr(s.Put(ctx, mainIndex, entry("a", "v1", "1234", 1), limit), nil)
	r.wantErr(s.Put(ctx, mainIndex, entry("b", "v1", "1234", 1), limit), nil)
	full := search.IndexInfo{Name: mainIndex, Documents: 2, Bytes: 8}

	r.wantErr(s.Put(ctx, mainIndex, entry("c", "v1", "", 2), limit), search.ErrFull)
	r.wantErr(s.Put(ctx, mainIndex, entry("a", "v2", "1234567", 2), limit), search.ErrFull)
	r.wantInfo(mainIndex, full)
	r.wantIDs(mainIndex, terms(1), "a", "b")
	r.wantIDs(mainIndex, terms(2))
	r.wantDocument(mainIndex, search.Selection{Filter: search.Filter{ID: "a"}}, "v1", "1234")

	r.wantErr(s.Put(ctx, mainIndex, entry("a", "v2", "123456", 2), limit), nil)
	r.wantInfo(mainIndex, search.IndexInfo{Name: mainIndex, Documents: 2, Bytes: 10})
	r.wantIDs(mainIndex, terms(2), "a")
}

// canceled checks that an ended context returns its error and changes
// nothing.
func (r *run) canceled() {
	s := r.store
	r.create(mainIndex)
	r.put(mainIndex, entry("x", "v1", "content", 1))

	ctx, cancel := context.WithCancel(r.ctx)
	cancel()
	ref := entry("x", "", "").Document.Ref

	_, err := s.Indexes(ctx)
	r.wantErr(err, context.Canceled)
	_, err = s.Index(ctx, mainIndex)
	r.wantErr(err, context.Canceled)
	r.wantErr(s.CreateIndex(ctx, "new"), context.Canceled)
	r.wantErr(s.SetMarker(ctx, mainIndex, "marker"), context.Canceled)
	r.wantErr(s.Put(ctx, mainIndex, entry("x", "v2", "changed", 2), roomy()), context.Canceled)
	r.wantErr(s.Remove(ctx, mainIndex, ref), context.Canceled)
	r.wantErr(s.Refs(ctx, mainIndex, search.Selection{}, func(search.Ref) bool { return true }), context.Canceled)
	r.wantErr(s.Documents(ctx, mainIndex, search.Selection{}, func(search.Document) bool { return true }), context.Canceled)
	r.wantErr(s.DeleteIndex(ctx, mainIndex), context.Canceled)

	r.wantInfo(mainIndex, search.IndexInfo{Name: mainIndex, Documents: 1, Bytes: 7})
	r.wantDocument(mainIndex, terms(1), "v1", "content")
	if got, err := s.Indexes(r.ctx); err != nil || len(got) != 1 {
		r.t.Fatalf("Indexes = %+v, %v, want one", got, err)
	}
}
