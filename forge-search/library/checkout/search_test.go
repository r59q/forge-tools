package checkout

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

func TestSearchDirectory(t *testing.T) {
	files := map[string]string{
		"cmd/main.go": "package main\n\nfunc main() {}\n",
		"lib.go":      "// main is elsewhere\n",
		"image.png":   "main\x00",
		"large.txt":   "main main main main main main main main main\n",
	}
	_, root := checkout(t, files)
	before := read(t, root, Options{})

	results, err := Search(context.Background(), root, search.Query{Text: "main"}, Options{Source: "c1", Limits: Limits{FileBytes: 40}})
	if err != nil {
		t.Fatal(err)
	}

	want := []search.Match{
		match(files, "cmd/main.go", 1, 9, "package main"),
		match(files, "cmd/main.go", 3, 6, "func main() {}"),
		match(files, "lib.go", 1, 4, "// main is elsewhere"),
	}
	if !reflect.DeepEqual(results.Matches, want) {
		t.Errorf("matches = %+v, want %+v", results.Matches, want)
	}
	if results.State.Skipped != 1 || results.Files.TooLarge != 1 || results.State.Truncated {
		t.Errorf("state = %+v, files = %+v, want one binary and one large file", results.State, results.Files)
	}

	if after := read(t, root, Options{}); !reflect.DeepEqual(before, after) {
		t.Error("search changed the directory")
	}
}

func match(files map[string]string, path string, line, column int, preview string) search.Match {
	return search.Match{
		Document:      search.Ref{Kind: search.KindCode, Source: "c1", ID: path, Version: contentVersion([]byte(files[path]))},
		Range:         search.Range{StartLine: line, StartColumn: column, EndLine: line, EndColumn: column + 4},
		Preview:       preview,
		PreviewColumn: 1,
	}
}

func TestSearchTruncated(t *testing.T) {
	_, root := checkout(t, map[string]string{"a": "x\n", "b": "x\n"})

	results, err := Search(context.Background(), root, search.Query{Text: "x"}, Options{Limits: Limits{TotalBytes: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results.Matches) != 1 || !results.State.Truncated || !results.Files.Truncated {
		t.Errorf("results = %+v, want one match and truncated", results)
	}
}

func TestSearchInvalidQuery(t *testing.T) {
	_, root := checkout(t, map[string]string{"a": "x"})

	_, err := Search(context.Background(), root, search.Query{Text: "a\nb"}, Options{})
	if !errors.Is(err, search.ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}
}

func TestSearchCanceled(t *testing.T) {
	_, root := checkout(t, map[string]string{"a": "x"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results, err := Search(ctx, root, search.Query{Text: "x"}, Options{})
	if err != nil || !results.State.Canceled || len(results.Matches) != 0 {
		t.Errorf("results = %+v, %v, want canceled", results, err)
	}
}

func TestSearchTooManyEntries(t *testing.T) {
	_, root := checkout(t, map[string]string{"a": "x", "b": "x"})

	_, err := Search(context.Background(), root, search.Query{Text: "x"}, Options{Limits: Limits{Entries: 1}})
	if !errors.Is(err, search.ErrTooLarge) {
		t.Errorf("error = %v, want ErrTooLarge", err)
	}
}
