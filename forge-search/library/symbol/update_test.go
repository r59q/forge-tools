package symbol

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// stored returns the ID of each stored symbol document, and fails when one
// still holds text.
func stored(t *testing.T, engine *search.Engine, gone string) []string {
	t.Helper()

	ids := []string{}
	err := engine.Read(context.Background(), testIndex, "", search.Filter{}, func(document search.Document) bool {
		if strings.Contains(string(document.Content), gone) {
			t.Errorf("document %s still holds %q", document.ID, gone)
		}
		ids = append(ids, string(document.Kind)+":"+document.ID)
		return true
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return ids
}

func TestUpdateStoresSymbols(t *testing.T) {
	analyzer := &fakeAnalyzer{}
	engine := newEngine(t)

	result := mustUpdate(t, analyzer, engine, Change{Source: testSource, Documents: corpus()[:3]})
	want := UpdateResult{Stored: 3, Symbols: 7}
	if result != want {
		t.Errorf("Update = %+v, want %+v", result, want)
	}

	got := names(mustSearchIndex(t, engine, Query{Text: "Handler"}))
	if !slices.Equal(got, []string{"cmd/main.py:Handler", "lib/b.go:NewHandler", "lib/b.go:HandlerFunc"}) {
		t.Errorf("SearchIndex = %q", got)
	}

	refs, err := engine.Documents(context.Background(), testIndex, search.Filter{})
	if err != nil || len(refs) != 3 || refs[0] != storedRef(corpus()[0].Ref) {
		t.Errorf("Documents = %+v, %v, want the stored symbols with the code versions", refs, err)
	}
}

func TestUpdateReplacesAndRemovesSymbols(t *testing.T) {
	analyzer := &fakeAnalyzer{}
	engine := newEngine(t)
	mustUpdate(t, analyzer, engine, Change{Source: testSource, Documents: []search.Document{
		doc("a.go", "@function Gone1"),
		doc("b.go", "@function Gone2"),
		doc("c.go", "@function Gone3"),
		doc("d.go", "@function Kept"),
	}})

	result := mustUpdate(t, analyzer, engine, Change{
		Source: testSource,
		Documents: []search.Document{
			doc("a.go", "@function Fresh"),
			doc("b.go", "no symbols"),
			doc("e.go", "@function Added"),
		},
		Removed: []string{"c.go", "never-stored.go"},
	})
	want := UpdateResult{Stored: 2, Symbols: 2, Removed: 2}
	if result != want {
		t.Errorf("Update = %+v, want %+v", result, want)
	}

	if got := names(mustSearchIndex(t, engine, Query{Text: "Gone"})); len(got) != 0 {
		t.Errorf("replaced and removed symbols still match: %q", got)
	}
	got := stored(t, engine, "Gone")
	if !slices.Equal(got, []string{"symbol:a.go", "symbol:d.go", "symbol:e.go"}) {
		t.Errorf("stored = %q", got)
	}
}

func TestUpdateClearsDocumentsThatAreSkipped(t *testing.T) {
	analyzer := &fakeAnalyzer{}
	engine := newEngine(t)
	paths := []string{"a.go", "b.go", "c.go", "d.go"}

	first := []search.Document{}
	for _, path := range paths {
		first = append(first, doc(path, "@function Gone"))
	}
	mustUpdate(t, analyzer, engine, Change{Source: testSource, Documents: first})

	result := mustUpdate(t, analyzer, engine, Change{Source: testSource, Documents: []search.Document{
		doc("a.go", "@function Gone\x00"),
		doc("b.go", "@function Gone TIMEOUT"),
		doc("c.go", "@function Gone HUGE"),
		doc("d.go", "@function Gone UNSUPPORTED"),
	}})

	want := UpdateResult{Removed: 4, Skipped: Skipped{Unsupported: 2, TooLarge: 1, Failed: 1}}
	if result != want {
		t.Errorf("Update = %+v, want %+v", result, want)
	}
	if got := stored(t, engine, "Gone"); len(got) != 0 {
		t.Errorf("stored = %q, want nothing", got)
	}
}

func TestUpdateLeavesOtherDocumentsAlone(t *testing.T) {
	analyzer := &fakeAnalyzer{}
	engine := newEngine(t)
	ctx := context.Background()

	code := search.Document{Ref: search.Ref{Kind: search.KindCode, Source: testSource, ID: "a.go"}, Content: []byte("Shared")}
	if err := engine.Put(ctx, testIndex, code); err != nil {
		t.Fatalf("Put: %v", err)
	}
	other := corpus()[3]
	mustUpdate(t, analyzer, engine, Change{Source: other.Source, Documents: []search.Document{other}})
	mustUpdate(t, analyzer, engine, Change{Source: testSource, Documents: []search.Document{doc("a.go", "@function Shared")}})

	mustUpdate(t, analyzer, engine, Change{Source: testSource, Removed: []string{"a.go"}})

	got := stored(t, engine, "Gone")
	if !slices.Equal(got, []string{"code:a.go", "symbol:lib/a.go"}) {
		t.Errorf("stored = %q, want the code document and the other source", got)
	}
}

func TestDeleteIndexRemovesSymbols(t *testing.T) {
	ctx := context.Background()
	store := &search.MemoryStore{}
	engine := search.NewEngine(store, search.IndexLimits{})
	if err := engine.CreateIndex(ctx, testIndex); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}
	mustUpdate(t, &fakeAnalyzer{}, engine, Change{Source: testSource, Documents: corpus()[:3]})

	if err := engine.DeleteIndex(ctx, testIndex); err != nil {
		t.Fatalf("DeleteIndex: %v", err)
	}
	_, err := SearchIndex(ctx, engine, testIndex, Query{Text: "andle"}, Limits{})
	wantError(t, err, search.ErrNotFound)

	if err := engine.CreateIndex(ctx, testIndex); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}
	if got := stored(t, engine, "andle"); len(got) != 0 {
		t.Errorf("a new index of the same name holds %q", got)
	}
}

func TestUpdateRejectsBadChanges(t *testing.T) {
	engine := newEngine(t)
	other := doc("b.go", secret)
	other.Source = "checkout-2"
	issue := doc("c.go", secret)
	issue.Kind = search.KindIssue

	changes := []Change{
		{Source: "bad\x00source"},
		{Source: testSource, Documents: []search.Document{doc("a.go", secret), doc("a.go", secret)}},
		{Source: testSource, Documents: []search.Document{other}},
		{Source: testSource, Documents: []search.Document{issue}},
		{Source: testSource, Documents: []search.Document{doc("", secret)}},
		{Source: testSource, Documents: []search.Document{doc("a.go", secret)}, Removed: []string{"a.go"}},
		{Source: testSource, Removed: []string{"x.go", "x.go"}},
		{Source: testSource, Removed: []string{""}},
	}
	for _, change := range changes {
		_, err := Update(context.Background(), &fakeAnalyzer{}, engine, testIndex, change, Limits{})
		wantError(t, err, search.ErrInvalid)
	}
}

func TestUpdateNeedsAnIndex(t *testing.T) {
	engine := search.NewEngine(&search.MemoryStore{}, search.IndexLimits{})

	_, err := Update(context.Background(), &fakeAnalyzer{}, engine, testIndex, Change{Source: testSource}, Limits{})
	wantError(t, err, search.ErrNotFound)
}
