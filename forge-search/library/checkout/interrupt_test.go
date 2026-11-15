package checkout

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

func revision(t *testing.T, engine *search.Engine) string {
	t.Helper()

	marker, err := Revision(context.Background(), engine, testIndex)
	if err != nil {
		t.Fatal(err)
	}
	return marker
}

func TestSyncRecordsRevision(t *testing.T) {
	dir, _ := checkout(t, map[string]string{"a.go": "a"})
	engine, _ := newIndex(t)

	if got := revision(t, engine); got != "" {
		t.Errorf("revision before a sync = %q", got)
	}
	syncTree(t, engine, dir, "0beb85ba5a99f6bef77835d60baf0ebcd32c15be")
	if got := revision(t, engine); got != "0beb85ba5a99f6bef77835d60baf0ebcd32c15be" {
		t.Errorf("revision = %q", got)
	}
}

func TestCanceledSyncIsCorrectedByTheNext(t *testing.T) {
	files := map[string]string{"a.go": "func a\n", "b.go": "func b\n", "c.go": "func c\n", "d.go": "func d\n"}
	dir, root := checkout(t, files)
	engine, store := newIndex(t)
	syncTree(t, engine, dir, "rev-1")

	write(t, dir, map[string]string{"a.go": "func a2\n", "b.go": "func b2\n", "c.go": "func c2\n", "e.go": "func e\n"})
	remove(t, dir, "d.go")

	store.reset()

	// Cancel while the second document is written.
	ctx, cancel := context.WithCancel(context.Background())
	store.beforePut = func(search.Entry) {
		if len(store.puts) == 1 {
			cancel()
		}
	}

	result, err := Sync(ctx, root, engine, testIndex, "rev-2", Options{Source: testSource})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if result.Revision != "" || revision(t, engine) != "" {
		t.Errorf("canceled sync recorded %q, marker %q", result.Revision, revision(t, engine))
	}
	if result.Removed != 1 || result.Replaced != 1 {
		t.Errorf("result = %+v, want the removal and one replacement so far", result)
	}

	store.beforePut = nil
	syncTree(t, engine, dir, "rev-2")
	assertSameResults(t, engine, dir)
	if got := revision(t, engine); got != "rev-2" {
		t.Errorf("revision = %q, want rev-2", got)
	}
}

func TestFileChangedDuringSync(t *testing.T) {
	dir, root := checkout(t, map[string]string{"a.go": "func a\n", "b.go": "func b\n"})
	engine, store := newIndex(t)

	// b.go changes after the listing, while a.go is written.
	store.beforePut = func(entry search.Entry) {
		if entry.Document.ID == "a.go" {
			write(t, dir, map[string]string{"b.go": "func b changed\n"})
		}
	}

	_, err := Sync(context.Background(), root, engine, testIndex, "rev-1", Options{Source: testSource})
	if !errors.Is(err, ErrChanged) || !strings.Contains(err.Error(), "b.go") {
		t.Fatalf("error = %v, want ErrChanged for b.go", err)
	}
	noHostPath(t, err, dir, "func b")
	if got := revision(t, engine); got != "" {
		t.Errorf("revision = %q after a failed sync", got)
	}

	store.beforePut = nil
	syncTree(t, engine, dir, "rev-1")
	assertSameResults(t, engine, dir)
}

func TestSyncLimitsChangeNothing(t *testing.T) {
	dir, root := checkout(t, map[string]string{"a.go": "aaaa", "b.go": "bbbb", "deep/er/c.go": "c"})
	engine, store := newIndex(t)

	limits := []Limits{{TotalBytes: 8}, {Entries: 4}, {Depth: 1}}
	for _, limit := range limits {
		_, err := Sync(context.Background(), root, engine, testIndex, "rev-1", Options{Limits: limit})
		if !errors.Is(err, search.ErrTooLarge) {
			t.Errorf("limits %+v: error = %v, want ErrTooLarge", limit, err)
		}
		noHostPath(t, err, dir)
	}
	if len(store.puts) != 0 || revision(t, engine) != "" {
		t.Errorf("a sync over its limits wrote %q", store.puts)
	}
}

func TestSyncInvalidRequests(t *testing.T) {
	_, root := checkout(t, map[string]string{"a.go": "a"})
	engine, _ := newIndex(t)

	cases := map[string]struct {
		index, revision string
		options         Options
		want            error
	}{
		"missing index": {index: "missing", want: search.ErrNotFound},
		"index name":    {index: "Bad", want: search.ErrInvalid},
		"revision":      {index: testIndex, revision: strings.Repeat("r", search.MaxMarkerBytes+1), want: search.ErrInvalid},
		"source":        {index: testIndex, options: Options{Source: "bad\x00"}, want: search.ErrInvalid},
	}
	for name, test := range cases {
		_, err := Sync(context.Background(), root, engine, test.index, test.revision, test.options)
		if !errors.Is(err, test.want) {
			t.Errorf("%s: error = %v, want %v", name, err, test.want)
		}
	}
}
