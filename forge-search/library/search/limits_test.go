package search

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const secret = "s3cr3t-content"

func searchError(t *testing.T, query Query, documents []Document, limits Limits, want error) {
	t.Helper()

	_, err := Unindexed(context.Background(), query, documents, limits)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error %q contains document content", err)
	}
}

func TestUnindexedRejectsInvalidQueries(t *testing.T) {
	documents := []Document{code("a", secret)}

	for _, text := range []string{"", "a\x00", "a\nb", "a\rb", "\xff", strings.Repeat("a", MaxQueryBytes+1)} {
		searchError(t, Query{Text: text}, documents, Limits{}, ErrInvalid)
	}
	for _, limit := range []int{-1, 11} {
		searchError(t, Query{Text: "a", Limit: limit}, documents, Limits{Results: 10}, ErrInvalid)
	}

	results := mustSearch(t, Query{Text: strings.Repeat("a", MaxQueryBytes)}, documents, Limits{})
	if len(results.Matches) != 0 {
		t.Errorf("matches = %+v, want none", results.Matches)
	}
}

func TestUnindexedRejectsInvalidDocuments(t *testing.T) {
	long := strings.Repeat("a", MaxNameBytes+1)
	refs := []Ref{
		{Kind: KindCode},
		{ID: "a"},
		{Kind: KindCode, ID: "a\x00"},
		{Kind: KindCode, ID: "\xff"},
		{Kind: KindCode, ID: long},
		{Kind: KindCode, ID: "a", Source: long},
		{Kind: KindCode, ID: "a", Version: long},
		{Kind: Kind(long), ID: "a"},
	}

	for _, ref := range refs {
		searchError(t, Query{Text: "a"}, []Document{{Ref: ref, Content: []byte(secret)}}, Limits{}, ErrInvalid)
	}
}

func TestUnindexedRejectsDuplicateDocuments(t *testing.T) {
	first := code("a", secret)
	second := code("a", secret)
	second.Version = "other"

	searchError(t, Query{Text: "a"}, []Document{first, code("b", ""), second}, Limits{}, ErrInvalid)
}

func TestUnindexedLimitsDocumentBytes(t *testing.T) {
	limits := Limits{DocumentBytes: len(secret)}

	mustSearch(t, Query{Text: "a"}, []Document{code("a", secret)}, limits)
	searchError(t, Query{Text: "a"}, []Document{code("a", secret+"!")}, limits, ErrTooLarge)
}

func TestUnindexedLimitsDocuments(t *testing.T) {
	documents := []Document{code("a", secret), code("b", secret)}

	mustSearch(t, Query{Text: "a"}, documents, Limits{Documents: 2})
	searchError(t, Query{Text: "a"}, documents, Limits{Documents: 1}, ErrTooLarge)
}

func TestUnindexedLimitsTotalBytes(t *testing.T) {
	documents := []Document{code("c", "x\n"), code("a", "x\n"), code("b", "x\n")}

	results := mustSearch(t, Query{Text: "x"}, documents, Limits{TotalBytes: 5})

	if len(results.Matches) != 2 || results.Matches[1].Document.ID != "b" {
		t.Errorf("matches = %+v, want a and b", results.Matches)
	}
	if !results.State.Truncated {
		t.Error("state is not truncated")
	}

	results = mustSearch(t, Query{Text: "x"}, documents, Limits{TotalBytes: 6})
	if len(results.Matches) != 3 || results.State.Truncated {
		t.Errorf("results = %+v, want all without truncation", results)
	}
}

func TestUnindexedLimitsResults(t *testing.T) {
	documents := []Document{code("b", "x\nx"), code("a", "x")}

	results := mustSearch(t, Query{Text: "x", Limit: 2}, documents, Limits{})
	if got, want := len(results.Matches), 2; got != want || !results.State.Truncated {
		t.Errorf("results = %+v, want %d truncated", results, want)
	}
	if got := results.Matches[1].Document.ID; got != "b" {
		t.Errorf("second match in %q, want b", got)
	}

	results = mustSearch(t, Query{Text: "x"}, documents, Limits{Results: 3})
	if len(results.Matches) != 3 || results.State.Truncated {
		t.Errorf("results = %+v, want exactly the limit without truncation", results)
	}
}

func TestDefaultLimits(t *testing.T) {
	if got, want := (Limits{}).withDefaults(), DefaultLimits(); got != want {
		t.Errorf("defaults = %+v, want %+v", got, want)
	}

	custom := Limits{DocumentBytes: 1, Documents: 2, TotalBytes: 3, Results: 4}
	if got := custom.withDefaults(); got != custom {
		t.Errorf("custom = %+v, want unchanged", got)
	}
}
