package checkout

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

const (
	testIndex  = "checkout-1"
	testSource = "c1"
)

// recordingStore counts the documents written and can act before each Put.
type recordingStore struct {
	*search.MemoryStore

	puts      []string
	removes   []string
	beforePut func(entry search.Entry)
}

func (s *recordingStore) Put(ctx context.Context, index string, entry search.Entry, capacity search.Capacity) error {
	if s.beforePut != nil {
		s.beforePut(entry)
	}
	s.puts = append(s.puts, entry.Document.ID)
	return s.MemoryStore.Put(ctx, index, entry, capacity)
}

func (s *recordingStore) Remove(ctx context.Context, index string, ref search.Ref) error {
	s.removes = append(s.removes, ref.ID)
	return s.MemoryStore.Remove(ctx, index, ref)
}

func (s *recordingStore) reset() {
	s.puts, s.removes = nil, nil
}

func newIndex(t *testing.T) (*search.Engine, *recordingStore) {
	t.Helper()

	store := &recordingStore{MemoryStore: &search.MemoryStore{}}
	engine := search.NewEngine(store, search.IndexLimits{})
	if err := engine.CreateIndex(context.Background(), testIndex); err != nil {
		t.Fatal(err)
	}
	return engine, store
}

func syncTree(t *testing.T, engine *search.Engine, dir, revision string) SyncResult {
	t.Helper()

	root := openRoot(t, dir)
	result, err := Sync(context.Background(), root, engine, testIndex, revision, Options{Source: testSource})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return result
}

// assertSameResults checks that the index and the directory give the same
// results for queries that match in many files and lines.
func assertSameResults(t *testing.T, engine *search.Engine, dir string) {
	t.Helper()

	root := openRoot(t, dir)
	queries := []string{"a", "ab", "func", "main", "line", "é", "missing"}
	matched := 0

	for _, text := range queries {
		query := search.Query{Text: text}

		want, err := Search(context.Background(), root, query, Options{Source: testSource, Limits: DefaultSyncLimits()})
		if err != nil {
			t.Fatal(err)
		}
		got, err := engine.Search(context.Background(), testIndex, query, filter(testSource))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want.Results) {
			t.Errorf("query %q: indexed %+v, unindexed %+v", text, got, want.Results)
		}
		matched += len(got.Matches)
	}

	if matched == 0 {
		t.Error("no query matched")
	}
}

func TestSyncMatchesUnindexedSearch(t *testing.T) {
	dir, _ := checkout(t, map[string]string{
		"main.go":        "package main\n\nfunc main() {}\n",
		"a-b.txt":        "ab line\nabc\n",
		"a/b.txt":        "a line é\n",
		"keep/file.go":   "func keep() {}\n",
		"binary.bin":     "func\x00main",
		"drop/old.go":    "func old() { main() }\n",
		"vendor/v.go":    "package v // main\n",
		".git/HEAD":      "main\n",
		"long/lines.txt": "",
	})
	engine, _ := newIndex(t)

	syncTree(t, engine, dir, "rev-1")
	assertSameResults(t, engine, dir)

	// Change, add and remove files between syncs.
	write(t, dir, map[string]string{
		"main.go":        "package main\n\nfunc main() { line() }\n",
		"new/added.go":   "func added() { main() }\n",
		"binary.bin":     "now text: main\n",
		"long/lines.txt": "ab ab ab\nmain main\n",
	})
	remove(t, dir, "drop", "a-b.txt")
	symlink(t, "main.go", filepath.Join(dir, "link.go"))

	syncTree(t, engine, dir, "rev-2")
	assertSameResults(t, engine, dir)

	// A file becomes binary and another disappears.
	write(t, dir, map[string]string{"keep/file.go": "func\xffkeep\n"})
	remove(t, dir, "vendor")

	syncTree(t, engine, dir, "rev-3")
	assertSameResults(t, engine, dir)
}

func TestSyncWritesOnlyChanges(t *testing.T) {
	dir, _ := checkout(t, map[string]string{"a.go": "a main\n", "b.go": "b main\n", "c.go": "c main\n"})
	engine, store := newIndex(t)

	first := syncTree(t, engine, dir, "rev-1")
	want := SyncResult{
		Revision: "rev-1", Added: 3,
		ChangedPaths: []string{"a.go", "b.go", "c.go"}, RemovedPaths: []string{},
	}
	if !reflect.DeepEqual(first, want) {
		t.Errorf("first sync = %+v, want %+v", first, want)
	}

	store.reset()
	write(t, dir, map[string]string{"b.go": "b changed\n", "d.go": "d main\n"})
	remove(t, dir, "c.go")

	second := syncTree(t, engine, dir, "rev-2")
	want = SyncResult{
		Revision: "rev-2", Added: 1, Replaced: 1, Removed: 1, Unchanged: 1,
		ChangedPaths: []string{"b.go", "d.go"}, RemovedPaths: []string{"c.go"},
	}
	if !reflect.DeepEqual(second, want) {
		t.Errorf("second sync = %+v, want %+v", second, want)
	}
	if !slices.Equal(store.puts, []string{"b.go", "d.go"}) || !slices.Equal(store.removes, []string{"c.go"}) {
		t.Errorf("writes = %q, removes = %q, want only the changes", store.puts, store.removes)
	}

	results, err := engine.Search(context.Background(), testIndex, search.Query{Text: "c main"}, search.Filter{})
	if err != nil || len(results.Matches) != 0 {
		t.Errorf("removed file still matches: %+v, %v", results, err)
	}

	store.reset()
	third := syncTree(t, engine, dir, "rev-2")
	if len(store.puts) != 0 || len(store.removes) != 0 || third.Unchanged != 3 {
		t.Errorf("unchanged sync wrote %q and removed %q: %+v", store.puts, store.removes, third)
	}
}

func TestSyncRemovesFilesLeftOut(t *testing.T) {
	dir, _ := checkout(t, map[string]string{"small.txt": "x", "grows.txt": "x"})
	engine, _ := newIndex(t)
	syncTree(t, engine, dir, "rev-1")

	write(t, dir, map[string]string{"grows.txt": string(make([]byte, 2<<20))})
	result := syncTree(t, engine, dir, "rev-2")

	if !slices.Equal(result.RemovedPaths, []string{"grows.txt"}) || result.Files.TooLarge != 1 {
		t.Errorf("result = %+v, want the large file removed and counted", result)
	}
}

func TestSyncLeavesOtherDocuments(t *testing.T) {
	dir, _ := checkout(t, map[string]string{"a.go": "a"})
	engine, _ := newIndex(t)

	others := []search.Document{
		{Ref: search.Ref{Kind: search.KindIssue, Source: testSource, ID: "1"}, Content: []byte("issue")},
		{Ref: search.Ref{Kind: search.KindCode, Source: "other", ID: "b.go"}, Content: []byte("other")},
	}
	for _, document := range others {
		if err := engine.Put(context.Background(), testIndex, document); err != nil {
			t.Fatal(err)
		}
	}

	syncTree(t, engine, dir, "rev-1")

	refs, err := engine.Documents(context.Background(), testIndex, search.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 3 {
		t.Errorf("documents = %+v, want the issue, the other source and a.go", refs)
	}
}
