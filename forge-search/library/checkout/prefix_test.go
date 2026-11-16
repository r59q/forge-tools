package checkout

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

func TestReadPrefix(t *testing.T) {
	dir, root := checkout(t, map[string]string{
		"cmd/main.go":       "main",
		"cmd/tool/tool.go":  "main",
		"cmdline.go":        "main",
		"command/x.go":      "main",
		"lib/big.go":        "main main",
		"lib/deep/deep.go":  "main",
		"cmd/large.go":      "main main main",
		"other/sub/file.go": "main",
	})
	symlink(t, "../cmd/main.go", filepath.Join(dir, "lib", "link.go"))

	cases := map[string][]string{
		"":         {"cmd/main.go", "cmd/tool/tool.go", "cmdline.go", "command/x.go", "lib/big.go", "lib/deep/deep.go", "other/sub/file.go"},
		"cmd":      {"cmd/main.go", "cmd/tool/tool.go", "cmdline.go"},
		"c":        {"cmd/main.go", "cmd/tool/tool.go", "cmdline.go", "command/x.go"},
		"cmd/":     {"cmd/main.go", "cmd/tool/tool.go"},
		"cmd/tool": {"cmd/tool/tool.go"},
		"other/s":  {"other/sub/file.go"},
		"missing/": {},
	}
	for prefix, want := range cases {
		tree := read(t, root, Options{Prefix: prefix, Limits: Limits{FileBytes: 10}})
		if got := ids(tree.Documents); !reflect.DeepEqual(got, want) {
			t.Errorf("prefix %q: documents = %q, want %q", prefix, got, want)
		}
	}

	// Entries outside the prefix are not counted as left out.
	tree := read(t, root, Options{Prefix: "other/", Limits: Limits{FileBytes: 10}})
	if tree.State != (State{}) {
		t.Errorf("state = %+v, want nothing left out", tree.State)
	}
	tree = read(t, root, Options{Prefix: "cmd/", Limits: Limits{FileBytes: 10}})
	if tree.State != (State{TooLarge: 1}) {
		t.Errorf("state = %+v, want one large file", tree.State)
	}
}

func TestSearchPrefixMatchesIndexFilter(t *testing.T) {
	dir, root := checkout(t, map[string]string{
		"cmd/main.go": "package main\n\nfunc main() {}\n",
		"cmdline.go":  "// main\n",
		"lib/lib.go":  "// main is in cmd\n",
	})
	engine, _ := newIndex(t)
	syncTree(t, engine, dir, "rev-1")

	for _, prefix := range []string{"", "cmd", "cmd/", "lib/lib.go", "x"} {
		query := search.Query{Text: "main"}
		want, err := Search(context.Background(), root, query, Options{Source: testSource, Prefix: prefix})
		if err != nil {
			t.Fatal(err)
		}
		got, err := engine.Search(context.Background(), testIndex, query, search.Filter{Kind: search.KindCode, Source: testSource, IDPrefix: prefix})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want.Results) {
			t.Errorf("prefix %q: indexed %+v, unindexed %+v", prefix, got, want.Results)
		}
	}
}

func TestPrefixSkipsOtherDirectories(t *testing.T) {
	// The root holds a, b and big; of them only big is visited.
	files := map[string]string{"a/x": "x", "b/y": "y", "big/1": "1", "big/2": "2", "big/3": "3"}
	_, root := checkout(t, files)

	// a and b are not visited, so three root entries and three in big fit.
	tree := read(t, root, Options{Prefix: "big/", Limits: Limits{Entries: 6}})
	if len(tree.Documents) != 3 {
		t.Errorf("documents = %d, want 3", len(tree.Documents))
	}
	if _, err := Read(context.Background(), root, Options{Limits: Limits{Entries: 6}}); !errors.Is(err, search.ErrTooLarge) {
		t.Errorf("error = %v without prefix, want ErrTooLarge", err)
	}
}

func TestPrefixInvalid(t *testing.T) {
	_, root := checkout(t, map[string]string{"a": "x"})
	engine, _ := newIndex(t)

	if _, err := Read(context.Background(), root, Options{Prefix: "bad\x00"}); !errors.Is(err, search.ErrInvalid) {
		t.Errorf("Read error = %v, want ErrInvalid", err)
	}
	if _, err := Sync(context.Background(), root, engine, testIndex, "", Options{Prefix: "a"}); !errors.Is(err, search.ErrInvalid) {
		t.Errorf("Sync error = %v, want ErrInvalid", err)
	}
}
