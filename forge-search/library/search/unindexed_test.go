package search

import (
	"context"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

func code(id, content string) Document {
	return Document{Ref: Ref{Kind: KindCode, Source: "checkout", ID: id}, Content: []byte(content)}
}

func span(line, start, end int) Range {
	return Range{StartLine: line, StartColumn: start, EndLine: line, EndColumn: end}
}

func mustSearch(t *testing.T, query Query, documents []Document, limits Limits) Results {
	t.Helper()

	results, err := Unindexed(context.Background(), query, documents, limits)
	if err != nil {
		t.Fatalf("Unindexed: %v", err)
	}
	return results
}

func ranges(results Results) []Range {
	var got []Range
	for _, match := range results.Matches {
		got = append(got, match.Range)
	}
	return got
}

func TestUnindexedFindsLiteralText(t *testing.T) {
	document := code("main.go", "package main\n\nfunc main() {}\n")
	document.Version = "v1"

	results := mustSearch(t, Query{Text: "main"}, []Document{document}, Limits{})

	want := []Match{
		{Document: document.Ref, Range: span(1, 9, 13), Preview: "package main", PreviewColumn: 1},
		{Document: document.Ref, Range: span(3, 6, 10), Preview: "func main() {}", PreviewColumn: 1},
	}
	if !slices.Equal(results.Matches, want) {
		t.Errorf("matches = %+v, want %+v", results.Matches, want)
	}
	if results.State != (State{}) {
		t.Errorf("state = %+v, want zero", results.State)
	}
}

func TestUnindexedIsCaseSensitive(t *testing.T) {
	results := mustSearch(t, Query{Text: "Main"}, []Document{code("a", "main\nMAIN\nMain")}, Limits{})

	if got, want := ranges(results), []Range{span(3, 1, 5)}; !slices.Equal(got, want) {
		t.Errorf("ranges = %+v, want %+v", got, want)
	}
}

func TestUnindexedCountsUTF16Columns(t *testing.T) {
	// "é" is one unit in two bytes, "世" one unit in three bytes, "😀" two
	// units in four bytes.
	document := code("a", "é世😀needle\n😀😀needle😀")

	results := mustSearch(t, Query{Text: "needle"}, []Document{document}, Limits{})
	if got, want := ranges(results), []Range{span(1, 5, 11), span(2, 5, 11)}; !slices.Equal(got, want) {
		t.Errorf("ranges = %+v, want %+v", got, want)
	}

	results = mustSearch(t, Query{Text: "😀n"}, []Document{document}, Limits{})
	if got, want := ranges(results), []Range{span(1, 3, 6), span(2, 3, 6)}; !slices.Equal(got, want) {
		t.Errorf("astral ranges = %+v, want %+v", got, want)
	}
}

func TestUnindexedReportsLeftmostMatchPerLine(t *testing.T) {
	document := code("a", "aaaa\nxax\nbbb\na")

	results := mustSearch(t, Query{Text: "aa"}, []Document{document}, Limits{})
	if got, want := ranges(results), []Range{span(1, 1, 3)}; !slices.Equal(got, want) {
		t.Errorf("ranges = %+v, want %+v", got, want)
	}

	results = mustSearch(t, Query{Text: "a"}, []Document{document}, Limits{})
	if got, want := ranges(results), []Range{span(1, 1, 2), span(2, 2, 3), span(4, 1, 2)}; !slices.Equal(got, want) {
		t.Errorf("ranges = %+v, want %+v", got, want)
	}
}

func TestUnindexedHandlesLineEnds(t *testing.T) {
	document := code("a", "x\r\n\n\nx\r\nx")

	results := mustSearch(t, Query{Text: "x"}, []Document{document}, Limits{})

	if got, want := ranges(results), []Range{span(1, 1, 2), span(4, 1, 2), span(5, 1, 2)}; !slices.Equal(got, want) {
		t.Errorf("ranges = %+v, want %+v", got, want)
	}
	if got := results.Matches[0].Preview; got != "x" {
		t.Errorf("preview = %q, want trailing \\r removed", got)
	}
}

func TestUnindexedOrdersByKindSourceIDAndLine(t *testing.T) {
	issue := Document{Ref: Ref{Kind: KindIssue, Source: "a", ID: "1"}, Content: []byte("x")}
	documents := []Document{
		issue,
		code("b", "x\nx"),
		{Ref: Ref{Kind: KindCode, Source: "other", ID: "a"}, Content: []byte("x")},
		code("a", "x"),
		{Ref: Ref{Kind: KindCode, ID: "z"}, Content: []byte("x")},
		code("B", "x"),
	}
	original := slices.Clone(documents)

	results := mustSearch(t, Query{Text: "x"}, documents, Limits{})

	var got []string
	for _, match := range results.Matches {
		ref := match.Document
		got = append(got, string(ref.Kind)+"/"+ref.Source+"/"+ref.ID+":"+string(rune('0'+match.Range.StartLine)))
	}
	want := []string{"code//z:1", "code/checkout/B:1", "code/checkout/a:1", "code/checkout/b:1", "code/checkout/b:2",
		"code/other/a:1", "issue/a/1:1"}
	if !slices.Equal(got, want) {
		t.Errorf("order = %q, want %q", got, want)
	}
	if !slices.EqualFunc(documents, original, func(a, b Document) bool { return a.Ref == b.Ref }) {
		t.Error("documents were reordered")
	}
}

func TestUnindexedSkipsBinaryAndInvalidUTF8(t *testing.T) {
	documents := []Document{
		code("binary", "needle\x00needle"),
		code("invalid", "needle\xffneedle"),
		code("text", "needle"),
	}

	results := mustSearch(t, Query{Text: "needle"}, documents, Limits{})

	if len(results.Matches) != 1 || results.Matches[0].Document.ID != "text" {
		t.Errorf("matches = %+v, want only text", results.Matches)
	}
	if results.State != (State{Skipped: 2}) {
		t.Errorf("state = %+v, want two skipped", results.State)
	}
}

func TestUnindexedWithoutMatchesReturnsEmptyList(t *testing.T) {
	results := mustSearch(t, Query{Text: "x"}, []Document{code("a", ""), code("b", "\n")}, Limits{})

	if results.Matches == nil || len(results.Matches) != 0 {
		t.Errorf("matches = %#v, want empty list", results.Matches)
	}
}

func TestUnindexedHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results, err := Unindexed(ctx, Query{Text: "x"}, []Document{code("a", "x")}, Limits{})

	if err != nil {
		t.Fatalf("Unindexed: %v", err)
	}
	if len(results.Matches) != 0 || !results.State.Canceled {
		t.Errorf("results = %+v, want canceled without matches", results)
	}
}

// naive finds the leftmost match of each line by splitting the text.
func naive(text, query string) []Range {
	var got []Range
	for index, line := range strings.Split(text, "\n") {
		at := strings.Index(line, query)
		if at < 0 {
			continue
		}
		column := len(utf16.Encode([]rune(line[:at]))) + 1
		got = append(got, span(index+1, column, column+len(utf16.Encode([]rune(query)))))
	}
	return got
}

func TestUnindexedAgreesWithLineSplitting(t *testing.T) {
	alphabet := []string{"a", "b", "\n", "\r", "é", "😀", " "}
	queries := []string{"a", "ab", "bb", "😀a", "é😀", "a a"}
	random := rand.New(rand.NewPCG(1, 2))

	for range 2000 {
		var text strings.Builder
		for range random.IntN(40) {
			text.WriteString(alphabet[random.IntN(len(alphabet))])
		}
		query := queries[random.IntN(len(queries))]

		results := mustSearch(t, Query{Text: query}, []Document{code("a", text.String())}, Limits{})

		if got, want := ranges(results), naive(text.String(), query); !slices.Equal(got, want) {
			t.Fatalf("%q in %q = %+v, want %+v", query, text.String(), got, want)
		}
	}
}
