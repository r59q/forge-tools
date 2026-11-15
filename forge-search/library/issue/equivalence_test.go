package issue

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-issues/library/issues"
	"github.com/r59q/forge-tools/forge-search/library/search"
)

// assertEquivalent checks that searching the reader's issues unindexed
// returns what searching the synced index returns.
func assertEquivalent(t *testing.T, reader Reader, engine *search.Engine, text string, options Options) {
	t.Helper()

	ctx := context.Background()
	query := search.Query{Text: text}

	direct, err := SearchRepository(ctx, reader, testRepository(), query, options)
	if err != nil {
		t.Fatalf("search repository %q: %v", text, err)
	}
	indexed, err := SearchIndex(ctx, engine, testIndex, query, options)
	if err != nil {
		t.Fatalf("search index %q: %v", text, err)
	}

	if !reflect.DeepEqual(direct.Matches, indexed.Matches) || direct.State != indexed.State {
		t.Fatalf("%q %+v:\nunindexed %v %+v\nindexed   %v %+v", text, options.Filter,
			numbers(direct), direct.State, numbers(indexed), indexed.State)
	}
}

func TestIndexedSearchEqualsUnindexed(t *testing.T) {
	rounds := [][]issues.Issue{
		{
			newIssue(1, "Crash on start", "It crashes.\nEvery time.", issues.Open),
			newIssue(2, "Docs", "crash course", issues.Closed),
			newIssue(10, "crash in parser", "", issues.Open),
			newIssue(11, "binary", "nul \x00", issues.Open),
		},
		{
			newIssue(1, "Crash on start", "Fixed now.", issues.Closed),
			newIssue(10, "crash in parser", "Stack:\r\ncrash()", issues.Open),
			newIssue(12, "New crash", "crash crash", issues.Open),
		},
		{
			newIssue(12, "New crash", "crash crash", issues.Closed),
			newIssue(3, "Älter crash", "ünïcode crash", issues.Open),
		},
		{},
	}
	queries := []string{"crash", "Crash", "every time", "nul", "ünïcode"}
	filters := []Filter{
		{},
		{State: issues.Open},
		{State: issues.Closed},
		{Source: "github", Repository: "owner/name"},
		{Repository: "owner/other"},
	}
	limits := []Limits{{}, {Results: 1}, {Results: 3}}

	engine, _ := newEngine(t)
	reader := &fakeReader{}
	for round, list := range rounds {
		reader.set(list...)
		mustSync(t, reader, engine)

		for _, text := range queries {
			for _, filter := range filters {
				for _, limit := range limits {
					t.Run("", func(t *testing.T) {
						t.Logf("round %d", round)
						assertEquivalent(t, reader, engine, text, Options{Filter: filter, Limits: limit})
					})
				}
			}
		}
	}
}

func TestSearchDuringSyncSeesWholeIssues(t *testing.T) {
	engine, _ := newEngine(t)
	old := []issues.Issue{}
	updated := []issues.Issue{}
	for number := int64(1); number <= 50; number++ {
		old = append(old, newIssue(number, "old bug", "old body", issues.Open))
		updated = append(updated, newIssue(number, "new bug", "new body", issues.Closed))
	}

	reader := &fakeReader{issues: old}
	mustSync(t, reader, engine)

	var group sync.WaitGroup
	group.Go(func() {
		for round := range 10 {
			if round%2 == 0 {
				reader.set(updated...)
			} else {
				reader.set(old...)
			}
			mustSync(t, reader, engine)
		}
	})

	for range 50 {
		results := searchIndex(t, engine, "bug", Options{})
		for _, match := range results.Matches {
			wantState := issues.Open
			if match.Issue.Title == "new bug" {
				wantState = issues.Closed
			}
			if match.Issue.State != wantState {
				t.Fatalf("issue %d: title %q with state %q", match.Issue.Number, match.Issue.Title, match.Issue.State)
			}
		}
	}
	group.Wait()
}
