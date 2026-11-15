package checkout

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

func TestFileBytesSkipsLargeFiles(t *testing.T) {
	_, root := checkout(t, map[string]string{"fits.txt": "1234", "large.txt": "12345"})

	tree := read(t, root, Options{Limits: Limits{FileBytes: 4}})

	if got := ids(tree.Documents); !reflect.DeepEqual(got, []string{"fits.txt"}) {
		t.Errorf("paths = %q, want fits.txt", got)
	}
	if tree.State.TooLarge != 1 {
		t.Errorf("too large = %d, want 1", tree.State.TooLarge)
	}
}

func TestEntriesLimit(t *testing.T) {
	// Five entries: a, b, dir, dir/c and the skipped .git.
	files := map[string]string{"a": "a", "b": "b", "dir/c": "c", ".git": "gitdir: x"}

	_, root := checkout(t, files)
	if tree := read(t, root, Options{Limits: Limits{Entries: 5}}); len(tree.Documents) != 3 {
		t.Errorf("documents = %d at the limit, want 3", len(tree.Documents))
	}

	_, err := Read(context.Background(), root, Options{Limits: Limits{Entries: 4}})
	if !errors.Is(err, search.ErrTooLarge) {
		t.Errorf("error = %v, want ErrTooLarge", err)
	}
}

func TestDepthLimit(t *testing.T) {
	_, root := checkout(t, map[string]string{"top.txt": "x", "one/two/file.txt": "x"})

	if tree := read(t, root, Options{Limits: Limits{Depth: 2}}); len(tree.Documents) != 2 {
		t.Errorf("documents = %d at the limit, want 2", len(tree.Documents))
	}

	_, err := Read(context.Background(), root, Options{Limits: Limits{Depth: 1}})
	if !errors.Is(err, search.ErrTooLarge) {
		t.Fatalf("error = %v, want ErrTooLarge", err)
	}
	if !strings.Contains(err.Error(), `"one/two"`) {
		t.Errorf("error %q does not name the directory", err)
	}
}

func TestTotalBytesTruncates(t *testing.T) {
	_, root := checkout(t, map[string]string{"a": "aaaa", "b": "bbbbbb", "c": "c"})

	tree := read(t, root, Options{Limits: Limits{TotalBytes: 6}})

	// "c" would fit, but files are read in match order and "b" does not.
	if got := ids(tree.Documents); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("paths = %q, want a", got)
	}
	if !tree.State.Truncated {
		t.Error("truncated is not set")
	}

	if tree := read(t, root, Options{Limits: Limits{TotalBytes: 11}}); tree.State.Truncated || len(tree.Documents) != 3 {
		t.Errorf("state = %+v with %d documents, want all three", tree.State, len(tree.Documents))
	}
}

func TestDefaultLimits(t *testing.T) {
	if got := (Limits{}).withDefaults(DefaultLimits()); got != DefaultLimits() {
		t.Errorf("defaults = %+v", got)
	}
	if got := (Limits{Entries: 7}).withDefaults(DefaultSyncLimits()); got.Entries != 7 || got.TotalBytes != 1<<30 {
		t.Errorf("sync defaults = %+v", got)
	}
}
