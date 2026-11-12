package symbols

import (
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-symbols/library/internal/treesitter"
)

// match builds a query match from name:start-end triples.
func match(captures ...treesitter.Capture) treesitter.Match {
	return treesitter.Match{Captures: captures}
}

func capture(name string, start, end int) treesitter.Capture {
	return treesitter.Capture{Name: name, Start: start, End: end}
}

func TestNewAnalysisNestsAndMerges(t *testing.T) {
	//         0         1         2         3
	//         0123456789012345678901234567890123456789
	source := "outer{ a b inner{ use } } after free"

	matches := []treesitter.Match{
		// Out of source order, to prove sorting.
		match(capture("name", 26, 31), capture("definition.function", 26, 31)),
		match(capture("name", 11, 16), capture("definition.class", 11, 23)),
		match(capture("name", 0, 5), capture("definition.module", 0, 25)),
		// Two names in one node are siblings, not parent and child.
		match(capture("name", 7, 8), capture("definition.field", 7, 10)),
		match(capture("name", 9, 10), capture("definition.field", 7, 10)),
		// The same node matched twice merges, keeping the container.
		match(capture("name", 11, 16), capture("definition.class", 11, 23), capture("container", 26, 31)),
		// A reference on a definition name is dropped; duplicates merge.
		match(capture("name", 11, 16), capture("reference.type", 11, 16)),
		match(capture("name", 18, 21), capture("reference.call", 18, 21)),
		match(capture("name", 18, 21), capture("reference.type", 18, 21)),
		match(capture("name", 32, 36), capture("reference.call", 32, 36)),
		// Matches without a name or a role are not tags.
		match(capture("definition.function", 0, 5)),
		match(capture("name", 0, 5)),
		match(capture("name", 0, 5), capture("doc", 0, 5)),
	}

	got := render(newAnalysis(matches, []byte(source)))
	want := strings.Join([]string{
		"definitions:",
		"  [0] module outer 1:1-1:6 range=1:1-1:26",
		"  [1] field a 1:8-1:9 range=1:8-1:11 container=outer parent=[0]",
		"  [2] field b 1:10-1:11 range=1:8-1:11 container=outer parent=[0]",
		"  [3] class inner 1:12-1:17 range=1:12-1:24 container=after parent=[0]",
		"  [4] function after 1:27-1:32 range=1:27-1:32",
		"references:",
		"  call use 1:19-1:22 in=[3] inner",
		"  call free 1:33-1:37",
		"",
	}, "\n")

	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// When patterns tag the same name, the first pattern of the query wins,
// whatever order the matches come in.
func TestNewAnalysisPrefersEarlierPatterns(t *testing.T) {
	//         0         1
	//         01234567890123
	source := "Box b = new T;"

	matches := []treesitter.Match{
		{Pattern: 3, Captures: []treesitter.Capture{capture("name", 0, 3), capture("definition.type", 0, 14)}},
		{Pattern: 1, Captures: []treesitter.Capture{capture("name", 0, 3), capture("definition.class", 0, 14)}},
		{Pattern: 2, Captures: []treesitter.Capture{capture("name", 12, 13), capture("reference.type", 12, 13)}},
		{Pattern: 0, Captures: []treesitter.Capture{capture("name", 12, 13), capture("reference.new", 8, 13)}},
	}

	got := render(newAnalysis(matches, []byte(source)))
	want := strings.Join([]string{
		"definitions:",
		"  [0] class Box 1:1-1:4 range=1:1-1:15",
		"references:",
		"  new T 1:13-1:14 in=[0] Box",
		"",
	}, "\n")

	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestNewAnalysisOfNoMatches(t *testing.T) {
	analysis := newAnalysis(nil, nil)

	if analysis.Definitions == nil || len(analysis.Definitions) != 0 {
		t.Errorf("definitions = %#v, want an empty slice", analysis.Definitions)
	}
	if analysis.References == nil || len(analysis.References) != 0 {
		t.Errorf("references = %#v, want an empty slice", analysis.References)
	}
}
