package sqlite_test

import (
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-search/library/search/storetest"
	"github.com/r59q/forge-tools/forge-search/library/sqlite"
)

func openStore(tb testing.TB) search.Store {
	return openIn(tb, tb.TempDir())
}

func openIn(tb testing.TB, dir string) *sqlite.Store {
	tb.Helper()

	store, err := sqlite.Open(tb.Context(), dir)
	if err != nil {
		tb.Fatalf("Open: %v", err)
	}
	tb.Cleanup(func() {
		if err := store.Close(); err != nil {
			tb.Errorf("Close: %v", err)
		}
	})
	return store
}

func TestStore(t *testing.T) { storetest.Run(t, openStore) }

func BenchmarkSearch(b *testing.B) { storetest.Benchmark(b, openStore) }
