package checkout_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/checkout"
	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-search/library/search/storetest"
	"github.com/r59q/forge-tools/forge-search/library/sqlite"
)

// BenchmarkCheckout measures both modes as the service runs them, on the
// corpus of storetest.Benchmark written to a directory: unindexed, Search
// reads and hashes every file; indexed, the tree is synced once into an
// SQLite store and searched there.
func BenchmarkCheckout(b *testing.B) {
	documents := storetest.Corpus(32<<20, 16<<10)
	root := writeCorpus(b, documents)
	engine := syncedEngine(b, root)

	queries := []struct{ name, text string }{
		{"identifier", storetest.Needle},
		{"phrase", lastLine(documents[len(documents)/2])},
		{"short", storetest.Needle[:2]},
	}

	for _, query := range queries {
		b.Run(query.name+"/unindexed", func(b *testing.B) {
			for b.Loop() {
				results, err := checkout.Search(b.Context(), root, search.Query{Text: query.text}, checkout.Options{})
				check(b, results.Results, err)
			}
		})
		b.Run(query.name+"/indexed", func(b *testing.B) {
			for b.Loop() {
				results, err := engine.Search(b.Context(), "main", search.Query{Text: query.text}, search.Filter{Kind: search.KindCode})
				check(b, results, err)
			}
		})
	}
}

// writeCorpus writes documents to a new directory, named by their IDs, and
// returns its open root.
func writeCorpus(b *testing.B, documents []search.Document) *os.Root {
	b.Helper()

	dir := b.TempDir()
	for _, document := range documents {
		path := filepath.Join(dir, filepath.FromSlash(document.ID))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, document.Content, 0o600); err != nil {
			b.Fatal(err)
		}
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = root.Close() })
	return root
}

// syncedEngine returns an engine over a new SQLite store, with the tree
// below root synced into the index "main".
func syncedEngine(b *testing.B, root *os.Root) *search.Engine {
	b.Helper()

	store, err := sqlite.Open(b.Context(), b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })

	engine := search.NewEngine(store, search.IndexLimits{})
	if err := engine.CreateIndex(b.Context(), "main"); err != nil {
		b.Fatal(err)
	}
	if _, err := checkout.Sync(b.Context(), root, engine, "main", "rev", checkout.Options{}); err != nil {
		b.Fatal(err)
	}
	return engine
}

func lastLine(document search.Document) string {
	lines := strings.Split(strings.TrimSpace(string(document.Content)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// check fails a benchmark whose search found nothing or was cut short.
func check(b *testing.B, results search.Results, err error) {
	b.Helper()
	if err != nil {
		b.Fatal(err)
	}
	if len(results.Matches) == 0 || results.State != (search.State{}) {
		b.Fatalf("%d matches, state %+v", len(results.Matches), results.State)
	}
}
