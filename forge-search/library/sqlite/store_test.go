package sqlite_test

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// BenchmarkSearchDuringUpdates measures a search for a line of one document
// on an idle store, with one search per CPU at a time, and while another
// goroutine replaces documents of the same index without pause. Searches
// read a snapshot, so they should not wait for each other or the updates.
func BenchmarkSearchDuringUpdates(b *testing.B) {
	documents := storetest.Corpus(4<<20, 16<<10)
	engine := search.NewEngine(openStore(b), search.IndexLimits{})
	wantErr(b, engine.CreateIndex(b.Context(), "main"), nil)
	put(b, engine, "main", documents...)

	lines := strings.Split(strings.TrimSpace(string(documents[len(documents)/2].Content)), "\n")
	query := search.Query{Text: strings.TrimSpace(lines[len(lines)-1])}
	searching := func(b *testing.B) {
		for b.Loop() {
			results, err := engine.Search(b.Context(), "main", query, search.Filter{})
			if err != nil || len(results.Matches) == 0 {
				b.Fatalf("%d matches, error %v", len(results.Matches), err)
			}
		}
	}

	b.Run("idle", searching)
	b.Run("parallel", func(b *testing.B) {
		b.RunParallel(func(loop *testing.PB) {
			for loop.Next() {
				_, err := engine.Search(b.Context(), "main", query, search.Filter{})
				wantErr(b, err, nil)
			}
		})
	})
	b.Run("updating", func(b *testing.B) {
		var (
			stop    atomic.Bool
			updates int
			done    sync.WaitGroup
		)
		start := time.Now()
		done.Go(func() {
			for ; !stop.Load(); updates++ {
				document := documents[updates%len(documents)]
				document.Version = strconv.Itoa(updates)
				wantErr(b, engine.Put(b.Context(), "main", document), nil)
			}
		})

		searching(b)
		stop.Store(true)
		done.Wait()
		b.ReportMetric(float64(updates)/time.Since(start).Seconds(), "updates/s")
	})
}
