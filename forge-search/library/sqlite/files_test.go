package sqlite_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-search/library/sqlite"
)

// secret is content that must leave the files of the store with its entry.
const secret = "secret-5d2e8b"

func document(id, content string) search.Document {
	return search.Document{
		Ref:     search.Ref{Kind: search.KindCode, Source: "repo", ID: id, Version: "v1"},
		Content: []byte(content),
	}
}

func wantErr(tb testing.TB, err, want error) {
	tb.Helper()
	if !errors.Is(err, want) || (want == nil) != (err == nil) {
		tb.Fatalf("error %v, want %v", err, want)
	}
}

func put(tb testing.TB, engine *search.Engine, index string, documents ...search.Document) {
	tb.Helper()
	for _, document := range documents {
		wantErr(tb, engine.Put(tb.Context(), index, document), nil)
	}
}

// snapshot is everything a client can read from an engine.
type snapshot struct {
	Indexes []search.IndexInfo
	Refs    map[string][]search.Ref
	Results map[string]search.Results
}

func read(tb testing.TB, engine *search.Engine, queries ...string) snapshot {
	tb.Helper()
	ctx := tb.Context()

	indexes, err := engine.Indexes(ctx)
	wantErr(tb, err, nil)
	got := snapshot{Indexes: indexes, Refs: map[string][]search.Ref{}, Results: map[string]search.Results{}}

	for _, index := range indexes {
		got.Refs[index.Name], err = engine.Documents(ctx, index.Name, search.Filter{})
		wantErr(tb, err, nil)
		for _, query := range queries {
			got.Results[index.Name+" "+query], err = engine.Search(ctx, index.Name, search.Query{Text: query}, search.Filter{})
			wantErr(tb, err, nil)
		}
	}
	return got
}

func TestReopenKeepsIndexes(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	queries := []string{"func", "needle", "a", "ünï"}

	store, err := sqlite.Open(ctx, dir)
	wantErr(t, err, nil)
	engine := search.NewEngine(store, search.IndexLimits{})
	wantErr(t, engine.CreateIndex(ctx, "one"), nil)
	wantErr(t, engine.CreateIndex(ctx, "two"), nil)
	wantErr(t, engine.SetMarker(ctx, "two", "rev-9"), nil)
	put(t, engine, "one",
		document("b.go", "func b() {}\nneedle\n"), document("a.go", "func a() {}\n"),
		document("ünï.txt", "ünï needle ünï\n"), document("bin", "\x00needle"))
	put(t, engine, "two", document("x", "needle a needle\n"))

	before := read(t, engine, queries...)
	wantErr(t, store.Close(), nil)

	after := read(t, search.NewEngine(openIn(t, dir), search.IndexLimits{}), queries...)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("after reopening:\n%+v\nbefore:\n%+v", after, before)
	}
	if len(before.Results["one func"].Matches) != 2 || before.Results["one needle"].State.Skipped != 1 {
		t.Fatalf("unexpected results before reopening: %+v", before.Results)
	}
}

func TestFilesArePrivate(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "data")
	wantErr(t, os.Mkdir(dir, 0o700), nil)

	store := openIn(t, dir)
	for _, name := range []string{"../escape", "/etc/escape", "a/../../b", "search.db", "C:\\x"} {
		wantErr(t, store.CreateIndex(t.Context(), name), nil)
		entry := search.Entry{Document: document("x", "text")}
		wantErr(t, store.Put(t.Context(), name, entry, search.Capacity{Documents: 1, Bytes: 4}), nil)
	}

	names := map[string]bool{}
	for _, file := range entries(t, dir) {
		names[file.Name()] = true
		info, err := file.Info()
		wantErr(t, err, nil)
		if mode := info.Mode(); mode != 0o600 {
			t.Errorf("%s has mode %v, want 0600", file.Name(), mode)
		}
	}
	want := map[string]bool{sqlite.FileName: true}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("files %v, want %v", names, want)
	}
	if files := entries(t, parent); len(files) != 1 {
		t.Errorf("%d files beside the data directory, want 1", len(files))
	}
}

func entries(tb testing.TB, dir string) []os.DirEntry {
	tb.Helper()
	files, err := os.ReadDir(dir)
	wantErr(tb, err, nil)
	return files
}

func TestRemovedContentLeavesFiles(t *testing.T) {
	ctx, dir := t.Context(), t.TempDir()
	store, err := sqlite.Open(ctx, dir)
	wantErr(t, err, nil)
	engine := search.NewEngine(store, search.IndexLimits{})

	large := strings.Repeat("line of text\n", 20000) + secret
	for _, index := range []string{"a", "b"} {
		wantErr(t, engine.CreateIndex(ctx, index), nil)
		put(t, engine, index, document("small", "before "+secret+" after"), document("large", large),
			document("kept", "kept text\n"))
	}
	wantErr(t, store.Checkpoint(ctx), nil)
	if traces(t, dir) == 0 {
		t.Fatal("the files do not hold the secret before removing it")
	}

	put(t, engine, "a", document("small", "clean"))
	wantErr(t, engine.Remove(ctx, "a", document("large", "").Ref), nil)
	wantErr(t, engine.DeleteIndex(ctx, "b"), nil)

	results, err := engine.Search(ctx, "a", search.Query{Text: secret}, search.Filter{})
	wantErr(t, err, nil)
	if len(results.Matches) != 0 {
		t.Fatalf("search found removed content: %+v", results.Matches)
	}

	if got := traces(t, dir); got != 0 {
		t.Fatalf("%d files hold removed content after commit", got)
	}
	wantErr(t, store.Close(), nil)
	if got := traces(t, dir); got != 0 {
		t.Fatalf("%d files hold removed content after Close", got)
	}
}

// traces counts the files in dir that hold the secret.
func traces(tb testing.TB, dir string) int {
	tb.Helper()

	count := 0
	for _, file := range entries(tb, dir) {
		content, err := os.ReadFile(filepath.Join(dir, file.Name()))
		wantErr(tb, err, nil)
		if bytes.Contains(content, []byte(secret)) {
			count++
		}
	}
	return count
}

func TestOpenErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), secret)
	wantErr(t, os.WriteFile(file, nil, 0o600), nil)

	readOnly := t.TempDir()
	wantErr(t, os.Chmod(readOnly, 0o500), nil)
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

	cases := []struct {
		name string
		dir  string
		want error
	}{
		{"empty", "", search.ErrInvalid},
		{"missing", filepath.Join(t.TempDir(), secret, "data"), sqlite.ErrStorage},
		{"file", file, sqlite.ErrStorage},
		{"read-only", readOnly, sqlite.ErrReadOnly},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if errors.Is(test.want, sqlite.ErrReadOnly) && os.Geteuid() == 0 {
				t.Skip("root writes to read-only directories")
			}
			store, err := sqlite.Open(context.Background(), test.dir)
			if err == nil {
				_ = store.Close()
			}
			wantErr(t, err, test.want)
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "/") {
				t.Errorf("error %q shows a path", err)
			}
		})
	}
}

func TestReadOnlyDatabase(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to read-only files")
	}
	ctx, dir := t.Context(), t.TempDir()
	store, err := sqlite.Open(ctx, dir)
	wantErr(t, err, nil)
	wantErr(t, store.Close(), nil)

	wantErr(t, os.Chmod(filepath.Join(dir, sqlite.FileName), 0o400), nil)
	store, err = sqlite.Open(ctx, dir)
	if err == nil {
		_ = store.Close()
	}
	wantErr(t, err, sqlite.ErrReadOnly)
}

func TestEngineLimits(t *testing.T) {
	ctx := t.Context()
	engine := search.NewEngine(openStore(t), search.IndexLimits{DocumentBytes: 8, Documents: 2, Bytes: 12})
	wantErr(t, engine.CreateIndex(ctx, "main"), nil)

	put(t, engine, "main", document("a", "12345678"))
	wantErr(t, engine.Put(ctx, "main", document("b", "123456789")), search.ErrTooLarge)
	wantErr(t, engine.Put(ctx, "main", document("b", "12345")), search.ErrFull)
	put(t, engine, "main", document("b", "1234"))
	wantErr(t, engine.Put(ctx, "main", document("c", "1")), search.ErrFull)
	put(t, engine, "main", document("a", "1234"), document("b", "12345678"))

	info, err := engine.Index(ctx, "main")
	wantErr(t, err, nil)
	if info.Documents != 2 || info.Bytes != 12 {
		t.Fatalf("index holds %d documents of %d bytes, want 2 of 12", info.Documents, info.Bytes)
	}
}
