package symbol

import (
	"context"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

func TestLimitsDefault(t *testing.T) {
	if got := (Limits{}).withDefaults(DefaultLimits()); got != DefaultLimits() {
		t.Errorf("search defaults = %+v", got)
	}
	if got := (Limits{}).withDefaults(DefaultUpdateLimits()); got != DefaultUpdateLimits() {
		t.Errorf("update defaults = %+v", got)
	}

	search, update := DefaultLimits(), DefaultUpdateLimits()
	if search.FileBytes != update.FileBytes || search.Symbols != update.Symbols {
		t.Errorf("searches and updates leave out different documents: %+v, %+v", search, update)
	}
}

func TestCallsOverLimitAnalyzeNothing(t *testing.T) {
	ctx := context.Background()
	documents := []search.Document{doc("a.go", "@function A"), doc("b.go", "@function B"), doc("c.md", "ignored")}

	tests := []Limits{
		{Files: 1},
		{TotalBytes: len("@function A")},
	}
	for _, limits := range tests {
		analyzer := &fakeAnalyzer{}

		_, err := Search(ctx, analyzer, documents, Query{Text: "a"}, limits)
		wantError(t, err, search.ErrTooLarge)

		engine := newEngine(t)
		_, err = Update(ctx, analyzer, engine, testIndex, Change{Source: testSource, Documents: documents}, limits)
		wantError(t, err, search.ErrTooLarge)

		if calls := analyzer.calls(); len(calls) != 0 {
			t.Errorf("limits %+v: analyzed %q", limits, calls)
		}
	}

	_, err := Update(ctx, &fakeAnalyzer{}, newEngine(t), testIndex, Change{Removed: []string{"a", "b"}}, Limits{Files: 1})
	wantError(t, err, search.ErrTooLarge)
}

func TestLimitsCountOnlyAnalyzedDocuments(t *testing.T) {
	documents := []search.Document{doc("a.go", "@function A"), doc("b.md", "x"), doc("c.go", strings.Repeat("x", 200))}

	limits := Limits{Files: 1, TotalBytes: 100, FileBytes: 100}
	results, err := Search(context.Background(), &fakeAnalyzer{}, documents, Query{Text: "A"}, limits)
	if err != nil || len(results.Symbols) != 1 {
		t.Errorf("Search = %+v, %v, want one symbol", results, err)
	}

	filtered := Query{Text: "A", Filter: Filter{PathPrefix: "a"}}
	documents = append(documents, doc("d.go", "@function D"))
	if _, err := Search(context.Background(), &fakeAnalyzer{}, documents, filtered, limits); err != nil {
		t.Errorf("Search of a filtered path: %v", err)
	}
}

func TestDocumentsOverRecordLimitAreSkipped(t *testing.T) {
	line := "@function " + strings.Repeat("N", MaxNameBytes) + "\n"
	content := strings.Repeat(line, MaxRecordBytes/len(line)+1)
	documents := []search.Document{doc("big.go", content)}

	limits := Limits{FileBytes: 4 * MaxRecordBytes, TotalBytes: 4 * MaxRecordBytes}
	results, err := Search(context.Background(), &fakeAnalyzer{}, documents, Query{Text: "N"}, limits)
	if err != nil || len(results.Symbols) != 0 || results.Skipped.TooLarge != 1 {
		t.Errorf("Search = %d symbols, %+v, %v, want the document skipped", len(results.Symbols), results.Skipped, err)
	}
}

func TestSearchRejectsBadRequests(t *testing.T) {
	ctx := context.Background()
	engine := newEngine(t)
	issue := doc("a.go", secret)
	issue.Kind = search.KindIssue

	queries := []Query{
		{},
		{Text: "a\nb"},
		{Text: strings.Repeat("a", search.MaxQueryBytes+1)},
		{Text: "a", Limit: -1},
		{Text: "a", Limit: DefaultLimits().Results + 1},
		{Text: "a", Filter: Filter{Kind: "bad\x00"}},
		{Text: "a", Filter: Filter{PathPrefix: "bad\xff"}},
	}
	for _, query := range queries {
		_, err := Search(ctx, &fakeAnalyzer{}, nil, query, Limits{})
		wantError(t, err, search.ErrInvalid)

		_, err = SearchIndex(ctx, engine, testIndex, query, Limits{})
		wantError(t, err, search.ErrInvalid)
	}

	bad := [][]search.Document{
		{issue},
		{doc("a.go", secret), doc("a.go", secret)},
		{{Ref: search.Ref{Kind: search.KindCode, ID: "a.go", Version: "bad\x00"}}},
	}
	for _, documents := range bad {
		_, err := Search(ctx, &fakeAnalyzer{}, documents, Query{Text: "a"}, Limits{})
		wantError(t, err, search.ErrInvalid)
	}
}

func TestSearchIndexRejectsCorruptRecords(t *testing.T) {
	ctx := context.Background()
	engine := newEngine(t)

	corrupt := []string{"no tab " + secret, "name\t{" + secret}
	for _, content := range corrupt {
		document := search.Document{Ref: search.Ref{Kind: KindSymbol, ID: "a.go"}, Content: []byte(content)}
		if err := engine.Put(ctx, testIndex, document); err != nil {
			t.Fatalf("Put: %v", err)
		}

		_, err := SearchIndex(ctx, engine, testIndex, Query{Text: "a"}, Limits{})
		wantError(t, err, search.ErrInvalid)
	}
}
