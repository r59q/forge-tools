package explorer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func fixture(t *testing.T) (*Worktree, string) {
	t.Helper()
	dir := t.TempDir()
	w, err := Open("local", "Local files", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, dir
}

func put(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityAndStaleLocations(t *testing.T) {
	w, dir := fixture(t)
	put(t, dir, "a.go", "package old")
	a, err := w.Read(t.Context(), "a.go", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.Read(t.Context(), "a.go", a.Location.Version)
	if err != nil || a != b {
		t.Fatalf("repeat: %+v %v", b, err)
	}
	put(t, dir, "a.go", "package new")
	if _, err := w.Read(t.Context(), "a.go", a.Location.Version); !errors.Is(err, ErrChanged) {
		t.Fatal(err)
	}
	other, err := Open("local", "Reopened", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if other.Source().Version == w.Source().Version {
		t.Fatal("reopen must change source incarnation")
	}
	c, err := other.Read(t.Context(), "a.go", "")
	if err != nil || c.Location.Version == a.Location.Version {
		t.Fatalf("identity: %+v %v", c, err)
	}
}

func TestConfinedReadsAndContentPolicy(t *testing.T) {
	w, dir := fixture(t)
	outside := t.TempDir()
	put(t, outside, "secret", "outside")
	put(t, dir, "text", "safe")
	put(t, dir, "binary", "a\x00b")
	put(t, dir, "nonutf8", "\xff")
	put(t, dir, "large", strings.Repeat("x", w.Source().Limits.FileBytes+1))
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("text", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]error{
		"../secret": ErrInvalid, "/etc/passwd": ErrInvalid, "a\\b": ErrInvalid,
		".git/config": ErrInvalid, "missing": ErrNotFound, "escape": ErrForbidden,
		"binary": ErrUnsupported, "nonutf8": ErrUnsupported, "large": ErrTooLarge,
		".": ErrUnsupported, "fifo": ErrUnsupported,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := w.Read(t.Context(), name, ""); !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
		})
	}
	if f, err := w.Read(t.Context(), "alias", ""); err != nil || f.Text != "safe" {
		t.Fatalf("alias: %+v %v", f, err)
	}
}

func TestBrowseAndSearch(t *testing.T) {
	w, dir := fixture(t)
	put(t, dir, ".git/config", "needle")
	put(t, dir, ".gitignore", "ignored.txt\n")
	put(t, dir, "ignored.txt", "needle")
	put(t, dir, "untracked.go", "😀needle needle\nneedle")
	put(t, dir, "sub/a.go", "needle")
	put(t, dir, "binary", "needle\x00")
	if err := os.Symlink("untracked.go", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	d, err := w.Browse(t.Context(), ".")
	if err != nil || d.State.Skipped != 1 || len(d.Entries) != 6 {
		t.Fatalf("browse: %+v %v", d, err)
	}
	r, err := w.Search(t.Context(), Query{Text: "needle"})
	if err != nil || len(r.Matches) != 4 || r.State.Skipped != 3 {
		t.Fatalf("search: %+v %v", r, err)
	}
	for _, m := range r.Matches {
		if m.Location.Source != "local" || m.Location.Version == "" {
			t.Fatal(m)
		}
		if m.Location.Path == "untracked.go" && m.Location.Range.StartLine == 1 && m.Location.Range.StartColumn != 3 {
			t.Fatal("columns must use UTF-16", m)
		}
	}
	filtered, err := w.Search(t.Context(), Query{Text: "needle", PathGlob: "sub/*.go"})
	if err != nil || len(filtered.Matches) != 1 {
		t.Fatalf("filter: %+v %v", filtered, err)
	}
	lookup, err := w.Search(t.Context(), Query{Text: "tracked", Files: true})
	if err != nil || len(lookup.Matches) != 1 || lookup.Matches[0].Location.Range.StartLine != 0 {
		t.Fatal(lookup, err)
	}
	limited, err := w.Search(t.Context(), Query{Text: "needle", Limit: 1})
	if err != nil || !limited.State.Truncated || len(limited.Matches) != 1 {
		t.Fatal(limited, err)
	}
}

func TestBoundsAndCancellation(t *testing.T) {
	w, dir := fixture(t)
	put(t, dir, "a", "one\ntwo")
	for _, q := range []Query{{}, {Text: "x", PathGlob: "["}, {Text: "x", Limit: 201}, {Text: "a\nb"}} {
		if _, err := w.Search(t.Context(), q); !errors.Is(err, ErrInvalid) {
			t.Fatal(q, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r, err := w.Search(ctx, Query{Text: "one"})
	if err != nil || !r.State.Canceled {
		t.Fatal(r, err)
	}
	if _, err := w.Read(ctx, "a", ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	w.source.Limits.SearchBytes = 2
	r, err = w.Search(t.Context(), Query{Text: "one"})
	if err != nil || !r.State.Truncated || len(r.Matches) != 0 {
		t.Fatal(r, err)
	}
	w.source.Limits.Entries = 1
	r, err = w.Search(t.Context(), Query{Text: "one"})
	if err != nil || !r.State.Truncated {
		t.Fatal(r, err)
	}
}

func TestConcurrentReplacementAndSymlinkEscape(t *testing.T) {
	w, dir := fixture(t)
	outside := t.TempDir()
	put(t, outside, "secret", "outside")
	put(t, dir, "changing", "inside")
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 100 {
			// Atomically alternate a regular file and a link to an external file.
			name := filepath.Join(dir, "next")
			if err := os.WriteFile(name, []byte("inside"), 0600); err != nil {
				t.Error(err)
				return
			}
			if err := os.Rename(name, filepath.Join(dir, "changing")); err != nil {
				t.Error(err)
				return
			}
			if err := os.Symlink(filepath.Join(outside, "secret"), name); err != nil {
				t.Error(err)
				return
			}
			if err := os.Rename(name, filepath.Join(dir, "changing")); err != nil {
				t.Error(err)
				return
			}
		}
	})
	for range 100 {
		f, err := w.Read(t.Context(), "changing", "")
		if err == nil && (f.Text != "inside" || f.Location.Version != w.version("changing", []byte(f.Text))) {
			t.Fatal(f)
		}
		if err != nil && !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrChanged) {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestConcurrentWrites(t *testing.T) {
	w, dir := fixture(t)
	put(t, dir, "changing", strings.Repeat("a", 100000))
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 100 {
			put(t, dir, "changing", strings.Repeat("b", 100000))
		}
	})
	for range 100 {
		f, err := w.Read(t.Context(), "changing", "")
		if err == nil && f.Location.Version != w.version("changing", []byte(f.Text)) {
			t.Fatal("wrong byte identity")
		}
		if err != nil && !errors.Is(err, ErrChanged) {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
