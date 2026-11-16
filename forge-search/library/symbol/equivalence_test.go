package symbol

import (
	"context"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-search/library/sqlite"
)

// checkout is the code documents of the test source, by path.
type checkout map[string]search.Document

func (c checkout) documents() []search.Document {
	return slices.Collect(maps.Values(c))
}

// randomContent returns a document of random definitions, with now and
// then a line that makes the analyzer fail.
func randomContent(random *rand.Rand) string {
	words := []string{"handle", "Handler", "ServeHTTP", "x", "xy", "Ünïcode", "new_handler", "Parse"}
	kinds := []string{"function", "method", "class", "type"}

	lines := []string{}
	for range 1 + random.IntN(8) {
		name := words[random.IntN(len(words))] + words[random.IntN(len(words))]
		lines = append(lines, "@"+kinds[random.IntN(len(kinds))]+" "+name)
	}
	if random.IntN(10) == 0 {
		lines = append(lines, "TIMEOUT")
	}
	return strings.Join(lines, "\n")
}

// randomChange replaces, adds and removes documents of the checkout.
func randomChange(random *rand.Rand, current checkout) Change {
	paths := []string{"a.go", "b.go", "lib/c.py", "lib/d.go", "lib/e.ts", "f.md", "g.go", "lib/h.py"}
	change := Change{Source: testSource}

	for _, path := range paths {
		switch random.IntN(3) {
		case 0:
			document := doc(path, randomContent(random))
			current[path] = document
			change.Documents = append(change.Documents, document)
		case 1:
			if _, held := current[path]; held {
				delete(current, path)
				change.Removed = append(change.Removed, path)
			}
		}
	}
	return change
}

func equivalenceQueries() []Query {
	return []Query{
		{Text: "x"},
		{Text: "xy"},
		{Text: "andle"},
		{Text: "Handler"},
		{Text: "handlehandle", Exact: true},
		{Text: "Ünï"},
		{Text: "andle", Limit: 3},
		{Text: "a", Limit: 5},
		{Text: "andle", Filter: Filter{Kind: "method"}},
		{Text: "e", Filter: Filter{Language: "python"}},
		{Text: "e", Filter: Filter{PathPrefix: "lib/", Kind: "class"}, Limit: 2},
		{Text: "e", Filter: Filter{Path: "a.go", Source: testSource}},
		{Text: "e", Filter: Filter{Source: "other"}},
	}
}

func assertEquivalent(t *testing.T, engine *search.Engine, current checkout, round int) {
	t.Helper()

	for _, query := range equivalenceQueries() {
		direct := mustSearch(t, &fakeAnalyzer{}, current.documents(), query)
		indexed := mustSearchIndex(t, engine, query)

		if !slices.Equal(direct.Symbols, indexed.Symbols) || direct.State != indexed.State {
			t.Fatalf("round %d, %+v:\nunindexed %q %+v\nindexed   %q %+v",
				round, query, names(direct), direct.State, names(indexed), indexed.State)
		}
	}
}

func TestIndexedSearchEqualsUnindexed(t *testing.T) {
	stores := map[string]func(t *testing.T) search.Store{
		"memory": func(*testing.T) search.Store { return &search.MemoryStore{} },
		"sqlite": openSQLite,
	}

	for name, open := range stores {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			engine := search.NewEngine(open(t), search.IndexLimits{})
			if err := engine.CreateIndex(ctx, testIndex); err != nil {
				t.Fatalf("CreateIndex: %v", err)
			}

			random := rand.New(rand.NewPCG(1, 2))
			current := checkout{}
			for round := range 12 {
				mustUpdate(t, &fakeAnalyzer{}, engine, randomChange(random, current))
				assertEquivalent(t, engine, current, round)
			}
		})
	}
}

func openSQLite(t *testing.T) search.Store {
	t.Helper()

	store, err := sqlite.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return store
}

func TestEquivalenceCoversReplacedAndRemovedSymbols(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	current := checkout{}
	replaced, removed := 0, 0

	for range 12 {
		before := maps.Clone(current)
		change := randomChange(random, current)

		removed += len(change.Removed)
		for _, document := range change.Documents {
			if _, held := before[document.ID]; held {
				replaced++
			}
		}
	}
	if replaced == 0 || removed == 0 {
		t.Fatalf("the rounds replace %d and remove %d documents, want both", replaced, removed)
	}
}
