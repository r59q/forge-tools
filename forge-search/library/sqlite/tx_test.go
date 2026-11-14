package sqlite

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

func open(t *testing.T, dir string) *Store {
	t.Helper()
	store, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

func TestDiskFull(t *testing.T) {
	ctx := t.Context()
	store := open(t, t.TempDir())
	defer func() { _ = store.Close() }()

	if err := store.CreateIndex(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	var pages int
	if err := store.writer.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writer.ExecContext(ctx, "PRAGMA max_page_count = "+strconv.Itoa(pages+1)); err != nil {
		t.Fatal(err)
	}

	secret := "secret " + strings.Repeat("x", 1<<20)
	entry := search.Entry{
		Document: search.Document{Ref: search.Ref{Kind: search.KindCode, Source: "s", ID: "a"}, Content: []byte(secret)},
	}
	err := store.Put(ctx, "main", entry, search.Capacity{Documents: 1, Bytes: 1 << 30})
	if !errors.Is(err, ErrDiskFull) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("Put on a full disk: %v, want %v", err, ErrDiskFull)
	}

	info, err := store.Index(ctx, "main")
	if err != nil || info.Documents != 0 {
		t.Fatalf("index after a failed Put: %+v, %v", info, err)
	}
}

func TestNewerSchema(t *testing.T) {
	dir := t.TempDir()
	store := open(t, dir)
	if _, err := store.writer.ExecContext(t.Context(), "PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(t.Context(), dir)
	if err == nil {
		_ = store.Close()
	}
	if !errors.Is(err, ErrStorage) {
		t.Fatalf("Open of a newer schema: %v, want %v", err, ErrStorage)
	}
}

func TestReopenKeepsSchema(t *testing.T) {
	dir := t.TempDir()
	if err := open(t, dir).Close(); err != nil {
		t.Fatal(err)
	}
	store := open(t, dir)
	defer func() { _ = store.Close() }()

	var version, pageSize int
	if err := store.writer.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := store.writer.QueryRowContext(t.Context(), "PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || pageSize != 1<<16 {
		t.Fatalf("version %d with pages of %d bytes, want %d with 65536", version, pageSize, schemaVersion)
	}
}
