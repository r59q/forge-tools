package search

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

const testIndex = "main"

// newIndex returns an engine on a memory store with one index holding the
// documents.
func newIndex(t *testing.T, limits IndexLimits, documents ...Document) (*Engine, *MemoryStore) {
	t.Helper()

	store := &MemoryStore{}
	engine := NewEngine(store, limits)
	if err := engine.CreateIndex(context.Background(), testIndex); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}
	for _, document := range documents {
		mustPut(t, engine, document)
	}
	return engine, store
}

func mustPut(t *testing.T, engine *Engine, document Document) {
	t.Helper()
	if err := engine.Put(context.Background(), testIndex, document); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func mustFind(t *testing.T, engine *Engine, query Query, filter Filter) Results {
	t.Helper()

	results, err := engine.Search(context.Background(), testIndex, query, filter)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	return results
}

// matched returns the ID of the document of every match.
func matched(results Results) []string {
	ids := []string{}
	for _, match := range results.Matches {
		ids = append(ids, match.Document.ID)
	}
	return ids
}

// wantError checks the category of an error and that it does not show
// document content.
func wantError(t *testing.T, err, want error) {
	t.Helper()

	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Errorf("error %q contains document content", err)
	}
}

func issue(id, content string) Document {
	return Document{Ref: Ref{Kind: KindIssue, Source: "forge", ID: id}, Content: []byte(content)}
}

func TestEngineIndexLifecycle(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(&MemoryStore{}, IndexLimits{})

	wantError(t, engine.CreateIndex(ctx, testIndex), nil)
	wantError(t, engine.CreateIndex(ctx, testIndex), ErrExists)
	wantError(t, engine.CreateIndex(ctx, "empty"), nil)

	main := code("cmd/main.go", "package main\n\nfunc main() {}\n")
	main.Version = "v1"
	mustPut(t, engine, main)
	mustPut(t, engine, code("docs/guide.md", "the main guide\n"))
	mustPut(t, engine, issue("7", "main crashes on start"))

	results := mustFind(t, engine, Query{Text: "main"}, Filter{})
	want := []Match{
		{Document: main.Ref, Range: span(1, 9, 13), Preview: "package main", PreviewColumn: 1},
		{Document: main.Ref, Range: span(3, 6, 10), Preview: "func main() {}", PreviewColumn: 1},
		{Document: code("docs/guide.md", "").Ref, Range: span(1, 5, 9), Preview: "the main guide", PreviewColumn: 1},
		{Document: issue("7", "").Ref, Range: span(1, 1, 5), Preview: "main crashes on start", PreviewColumn: 1},
	}
	if !slices.Equal(results.Matches, want) || results.State != (State{}) {
		t.Errorf("results = %+v, want %+v", results, want)
	}

	main.Content, main.Version = []byte("package renamed\n"), "v2"
	mustPut(t, engine, main)
	wantError(t, engine.Remove(ctx, testIndex, issue("7", "").Ref), nil)
	wantError(t, engine.Remove(ctx, testIndex, issue("7", "").Ref), ErrNotFound)
	wantError(t, engine.SetMarker(ctx, testIndex, "revision-2"), nil)

	if got := matched(mustFind(t, engine, Query{Text: "main"}, Filter{})); !slices.Equal(got, []string{"docs/guide.md"}) {
		t.Errorf("after update matched %q, want the guide only", got)
	}
	listed, err := engine.Documents(ctx, testIndex, Filter{})
	wantError(t, err, nil)
	if wantRefs := []Ref{main.Ref, code("docs/guide.md", "").Ref}; !slices.Equal(listed, wantRefs) {
		t.Errorf("documents = %+v, want %+v", listed, wantRefs)
	}

	wantInfo := IndexInfo{Name: testIndex, Marker: "revision-2", Documents: 2, Bytes: 31}
	if info, err := engine.Index(ctx, testIndex); err != nil || info != wantInfo {
		t.Errorf("Index = %+v, %v, want %+v", info, err, wantInfo)
	}
	if infos, err := engine.Indexes(ctx); err != nil || !slices.Equal(infos, []IndexInfo{{Name: "empty"}, wantInfo}) {
		t.Errorf("Indexes = %+v, %v", infos, err)
	}

	wantError(t, engine.DeleteIndex(ctx, testIndex), nil)
	wantError(t, engine.DeleteIndex(ctx, testIndex), ErrNotFound)
	_, err = engine.Search(ctx, testIndex, Query{Text: "main"}, Filter{})
	wantError(t, err, ErrNotFound)
	wantError(t, engine.Put(ctx, testIndex, main), ErrNotFound)
}

func TestEngineSelectsByKindSourceAndID(t *testing.T) {
	other := code("src/a.go", "needle")
	other.Source = "other"

	engine, _ := newIndex(t, IndexLimits{},
		code("src/a.go", "needle"),
		code("src/deep/b.go", "needle"),
		code("docs/a.md", "needle"),
		other,
		issue("src/1", "needle"),
	)

	tests := []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"everything", Filter{}, []string{"docs/a.md", "src/a.go", "src/deep/b.go", "src/a.go", "src/1"}},
		{"kind", Filter{Kind: KindIssue}, []string{"src/1"}},
		{"source", Filter{Source: "other"}, []string{"src/a.go"}},
		{"path", Filter{Kind: KindCode, IDPrefix: "src/"}, []string{"src/a.go", "src/deep/b.go", "src/a.go"}},
		{"path and source", Filter{Source: "checkout", IDPrefix: "src/deep/"}, []string{"src/deep/b.go"}},
		{"one document", Filter{Kind: KindCode, Source: "checkout", ID: "src/a.go"}, []string{"src/a.go"}},
		{"nothing", Filter{Kind: "wiki"}, []string{}},
	}
	for _, test := range tests {
		for _, text := range []string{"needle", "ne"} {
			if got := matched(mustFind(t, engine, Query{Text: text}, test.filter)); !slices.Equal(got, test.want) {
				t.Errorf("%s, query %q: matched %q, want %q", test.name, text, got, test.want)
			}
		}
	}
}

func TestEngineFindsQueriesTooShortToNarrow(t *testing.T) {
	texts := []string{
		"ab",
		"abc",
		"a",
		"xaby\nab\n\nba ab",
		"é",
		"café éé",
		"日本語\n本",
		"x",
		"",
	}

	var documents []Document
	for index, text := range texts {
		documents = append(documents, code(string(rune('a'+index)), text))
	}
	engine, _ := newIndex(t, IndexLimits{}, documents...)

	for _, text := range []string{"a", "b", "ab", "é", "本", "y\n"[:1], "ba", " a", "abc", "aby"} {
		results := mustFind(t, engine, Query{Text: text}, Filter{})

		var want []Match
		for _, document := range documents {
			for _, found := range naive(string(document.Content), text) {
				want = append(want, Match{Document: document.Ref, Range: found})
			}
		}
		if len(want) == 0 {
			t.Fatalf("query %q matches nothing; the test needs a match", text)
		}
		if len(results.Matches) != len(want) {
			t.Fatalf("query %q: %d matches, want %d", text, len(results.Matches), len(want))
		}
		for position, match := range results.Matches {
			if match.Document != want[position].Document || match.Range != want[position].Range {
				t.Errorf("query %q: match %d = %+v, want %+v", text, position, match, want[position])
			}
		}

		unindexed := mustSearch(t, Query{Text: text}, documents, Limits{})
		if !slices.Equal(results.Matches, unindexed.Matches) || results.State != unindexed.State {
			t.Errorf("query %q: indexed %+v, unindexed %+v", text, results, unindexed)
		}
	}
}

func TestEngineCountsBinaryDocumentsWithoutStoringThem(t *testing.T) {
	ctx := context.Background()
	engine, store := newIndex(t, IndexLimits{},
		code("a", "needle"),
		code("b", secret+"\x00needle"),
		code("c", secret+"\xffneedle"),
		code("d", "needle"),
		issue("e", "\x00"),
	)

	for _, text := range []string{"needle", "ne"} {
		results := mustFind(t, engine, Query{Text: text}, Filter{Kind: KindCode})
		if got := matched(results); !slices.Equal(got, []string{"a", "d"}) || results.State != (State{Skipped: 2}) {
			t.Errorf("query %q: matched %q with state %+v", text, got, results.State)
		}

		results = mustFind(t, engine, Query{Text: text, Limit: 1}, Filter{})
		if want := (State{Truncated: true, Skipped: 2}); results.State != want {
			t.Errorf("query %q, limit 1: state %+v, want %+v", text, results.State, want)
		}
	}

	listed, err := engine.Documents(ctx, testIndex, Filter{})
	wantError(t, err, nil)
	if len(listed) != 5 {
		t.Errorf("%d documents listed, want 5", len(listed))
	}
	if info, err := engine.Index(ctx, testIndex); err != nil || info.Documents != 5 || info.Bytes != 12 {
		t.Errorf("Index = %+v, %v, want 5 documents with 12 bytes", info, err)
	}
	if holdsContent(store, secret) {
		t.Error("the store holds binary content")
	}

	mustPut(t, engine, code("b", "needle now text"))
	if got := mustFind(t, engine, Query{Text: "needle"}, Filter{}); got.State.Skipped != 2 || len(got.Matches) != 3 {
		t.Errorf("after replacing a binary document: %+v", got)
	}
}

func TestEngineHonoursCancellation(t *testing.T) {
	engine, _ := newIndex(t, IndexLimits{}, code("a", "needle"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results, err := engine.Search(ctx, testIndex, Query{Text: "needle"}, Filter{})
	if err != nil || !results.State.Canceled || len(results.Matches) != 0 || results.Matches == nil {
		t.Errorf("Search = %+v, %v, want canceled without matches", results, err)
	}
}

// holdsContent reports whether any index of the store still holds text, as
// document content or as the postings of all its terms.
func holdsContent(store *MemoryStore, text string) bool {
	store.mu.RLock()
	defer store.mu.RUnlock()

	for _, index := range store.indexes {
		for _, entry := range index.entries {
			if bytes.Contains(entry.document.Content, []byte(text)) {
				return true
			}
		}
		for _, term := range contentTerms([]byte(text)) {
			if len(index.postings[term]) > 0 {
				return true
			}
		}
	}
	return false
}
