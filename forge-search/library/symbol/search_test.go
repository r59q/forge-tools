package symbol

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

func corpus() []search.Document {
	other := doc("lib/a.go", "@function OtherHandler")
	other.Source = "checkout-2"

	return []search.Document{
		doc("cmd/main.py", "@function handler\n@class Handler\n@method handle Handler"),
		doc("lib/b.go", "x := 1\n  @function NewHandler\n@type HandlerFunc"),
		doc("lib/a.go", "@method ServeHTTP Handler\n@function handleAll"),
		other,
	}
}

func TestSearchMatchesNamesInOrder(t *testing.T) {
	analyzer := &fakeAnalyzer{}

	tests := []struct {
		query Query
		want  []string
	}{
		{Query{Text: "andle"}, []string{
			"cmd/main.py:handler", "cmd/main.py:Handler", "cmd/main.py:handle",
			"lib/a.go:handleAll", "lib/b.go:NewHandler", "lib/b.go:HandlerFunc", "lib/a.go:OtherHandler",
		}},
		{Query{Text: "Handler"}, []string{
			"cmd/main.py:Handler", "lib/b.go:NewHandler", "lib/b.go:HandlerFunc", "lib/a.go:OtherHandler",
		}},
		{Query{Text: "Handler", Exact: true}, []string{"cmd/main.py:Handler"}},
		{Query{Text: "handler", Exact: true}, []string{"cmd/main.py:handler"}},
		{Query{Text: "Serve"}, []string{"lib/a.go:ServeHTTP"}},
		{Query{Text: "andle", Filter: Filter{Kind: "method"}}, []string{"cmd/main.py:handle"}},
		{Query{Text: "andle", Filter: Filter{Language: "go", Source: testSource}}, []string{
			"lib/a.go:handleAll", "lib/b.go:NewHandler", "lib/b.go:HandlerFunc",
		}},
		{Query{Text: "andle", Filter: Filter{Path: "lib/a.go"}}, []string{"lib/a.go:handleAll", "lib/a.go:OtherHandler"}},
		{Query{Text: "andle", Filter: Filter{PathPrefix: "lib/", Source: "checkout-2"}}, []string{"lib/a.go:OtherHandler"}},
		{Query{Text: "andle", Limit: 2}, []string{"cmd/main.py:handler", "cmd/main.py:Handler"}},
		{Query{Text: "missing"}, []string{}},
	}
	for _, test := range tests {
		got := names(mustSearch(t, analyzer, corpus(), test.query))
		if !slices.Equal(got, test.want) {
			t.Errorf("Search(%+v) = %q, want %q", test.query, got, test.want)
		}
	}
}

func TestSearchReturnsRecords(t *testing.T) {
	documents := []search.Document{doc("lib/b.go", "x := 1\n  @method Close File\r\n")}

	results := mustSearch(t, &fakeAnalyzer{}, documents, Query{Text: "Close"})
	want := Symbol{
		Document:      documents[0].Ref,
		Name:          "Close",
		Kind:          "method",
		Language:      "go",
		NameRange:     search.Range{StartLine: 2, StartColumn: 11, EndLine: 2, EndColumn: 16},
		Range:         search.Range{StartLine: 2, StartColumn: 1, EndLine: 2, EndColumn: 22},
		Container:     "File",
		Preview:       "@method Close File",
		PreviewColumn: 3,
	}
	if len(results.Symbols) != 1 || results.Symbols[0] != want {
		t.Errorf("Search = %+v, want %+v", results.Symbols, want)
	}
}

func TestSearchTruncatesAtLimit(t *testing.T) {
	results := mustSearch(t, &fakeAnalyzer{}, corpus(), Query{Text: "Handler", Limit: 4})
	if results.State.Truncated || len(results.Symbols) != 4 {
		t.Errorf("all symbols: %d, %+v, want 4 and not truncated", len(results.Symbols), results.State)
	}

	results = mustSearch(t, &fakeAnalyzer{}, corpus(), Query{Text: "Handler", Limit: 3})
	if !results.State.Truncated || len(results.Symbols) != 3 {
		t.Errorf("limited: %d, %+v, want 3 and truncated", len(results.Symbols), results.State)
	}
}

func TestSearchStopsAnalyzingAtLimit(t *testing.T) {
	analyzer := &fakeAnalyzer{}

	mustSearch(t, analyzer, corpus(), Query{Text: "andle", Limit: 1})
	if got := analyzer.calls(); !slices.Equal(got, []string{"cmd/main.py"}) {
		t.Errorf("analyzed %q, want only the first document", got)
	}
}

func TestSearchSkipsDocuments(t *testing.T) {
	analyzer := &fakeAnalyzer{}
	documents := []search.Document{
		doc("a.md", "@function Markdown"),
		doc("b.go", "@function Binary\x00"),
		doc("c.go", "@function Invalid\xff"),
		doc("d.go", "@function Large "+strings.Repeat("x", 100)),
		doc("e.go", "@function Timeout TIMEOUT"),
		doc("f.go", "@function Memory MEMORY"),
		doc("g.go", "@function Huge HUGE"),
		doc("h.go", "@function Unsupported UNSUPPORTED"),
		doc("i.go", "@function Many\n@function Many2\n@function Many3"),
		doc("j.go", "@function Kept"),
	}

	results, err := Search(context.Background(), analyzer, documents, Query{Text: "e"}, Limits{FileBytes: 100, Symbols: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if got := names(results); !slices.Equal(got, []string{"j.go:Kept"}) {
		t.Errorf("symbols = %q, want only j.go:Kept", got)
	}
	want := Skipped{Unsupported: 4, TooLarge: 3, Failed: 2}
	if results.Skipped != want {
		t.Errorf("skipped = %+v, want %+v", results.Skipped, want)
	}
	if got := analyzer.calls(); !slices.Equal(got, []string{"e.go", "f.go", "g.go", "h.go", "i.go", "j.go"}) {
		t.Errorf("analyzed %q, want no unsupported, binary or large document", got)
	}
}

func TestSearchLeavesOutBadDefinitions(t *testing.T) {
	analyzer := definitions(
		definition("", "function", 1, 1),
		definition("tab\tname", "function", 1, 1),
		definition("Long", strings.Repeat("k", MaxNameBytes+1), 1, 1),
		definition("Outside", "function", 9, 1),
		definition("Kept", "function", 1, 1),
	)

	results := mustSearch(t, analyzer, []search.Document{doc("a.go", "Kept Long Outside")}, Query{Text: "e"})
	if got := names(results); !slices.Equal(got, []string{"a.go:Kept"}) {
		t.Errorf("symbols = %q, want only a.go:Kept", got)
	}
}

func TestSearchReturnsUnexpectedAnalyzerErrors(t *testing.T) {
	documents := []search.Document{doc("a.go", "BOOM "+secret)}

	_, err := Search(context.Background(), &fakeAnalyzer{}, documents, Query{Text: "a"}, Limits{})
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "a.go") {
		t.Errorf("error = %v, want an error without content or path", err)
	}
}

func TestSearchReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results, err := Search(ctx, &fakeAnalyzer{}, corpus(), Query{Text: "andle"}, Limits{})
	if err != nil || !results.State.Canceled || len(results.Symbols) != 0 {
		t.Errorf("Search = %+v, %v, want no symbols and canceled", results, err)
	}

	engine := newEngine(t)
	mustUpdate(t, &fakeAnalyzer{}, engine, Change{Source: testSource, Documents: corpus()[:3]})

	results, err = SearchIndex(ctx, engine, testIndex, Query{Text: "andle"}, Limits{})
	if err != nil || !results.State.Canceled || len(results.Symbols) != 0 {
		t.Errorf("SearchIndex = %+v, %v, want no symbols and canceled", results, err)
	}
}
