package symbol

import (
	"context"
	"slices"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

const goSource = `package store

// Store keeps items.
type Store struct{}

func (s *Store) Get(key string) (string, error) {
	return "", nil
}

func NewStore() *Store { return &Store{} }
`

const pythonSource = `class Store:
    def get(self, key):
        return None


def new_store():
    return Store()
`

// TestRealAnalyzer runs forge-symbols on a Go and a Python document, in both
// search modes. It is the only test that starts the WebAssembly parsers.
func TestRealAnalyzer(t *testing.T) {
	ctx := context.Background()
	analyzer := symbols.New(symbols.Limits{})
	t.Cleanup(func() {
		if err := analyzer.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	documents := []search.Document{doc("store.go", goSource), doc("store.py", pythonSource), doc("README.md", "# Store")}
	engine := newEngine(t)

	result := mustUpdate(t, analyzer, engine, Change{Source: testSource, Documents: documents})
	if result.Stored != 2 || result.Skipped != (Skipped{Unsupported: 1}) {
		t.Errorf("Update = %+v, want 2 documents stored and README.md unsupported", result)
	}

	query := Query{Text: "tore"}
	direct := mustSearch(t, analyzer, documents, query)
	if indexed := mustSearchIndex(t, engine, query); !slices.Equal(direct.Symbols, indexed.Symbols) {
		t.Errorf("indexed %q, unindexed %q", names(indexed), names(direct))
	}

	want := []Symbol{
		analyzed("store.go", "Store", "struct", "go", "", "Store struct{}", rng(4, 6, 4, 11), rng(4, 6, 4, 20)),
		analyzed("store.go", "NewStore", "function", "go", "", "func NewStore() *Store { return &Store{} }",
			rng(10, 6, 10, 14), rng(10, 1, 10, 43)),
		analyzed("store.py", "Store", "class", "python", "", "class Store:", rng(1, 7, 1, 12), rng(1, 1, 3, 20)),
		analyzed("store.py", "new_store", "function", "python", "", "def new_store():", rng(6, 5, 6, 14), rng(6, 1, 7, 19)),
	}
	assertSymbols(t, direct.Symbols, want)

	method := mustSearch(t, analyzer, documents, Query{Text: "et", Filter: Filter{Kind: "method"}})
	assertSymbols(t, method.Symbols, []Symbol{
		analyzed("store.go", "Get", "method", "go", "Store", "func (s *Store) Get(key string) (string, error) {",
			rng(6, 17, 6, 20), rng(6, 1, 8, 2)),
		analyzed("store.py", "get", "method", "python", "Store", "def get(self, key):", rng(2, 9, 2, 12), rng(2, 5, 3, 20)),
	})
}

func assertSymbols(t *testing.T, got, want []Symbol) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("symbols %q, want %d", names(Results{Symbols: got}), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("symbol %d =\n%+v, want\n%+v", index, got[index], want[index])
		}
	}
}

func analyzed(path, name, kind, language, container, preview string, nameRange, full search.Range) Symbol {
	source := map[string]string{"store.go": goSource, "store.py": pythonSource}[path]
	column := nameRange.StartColumn
	if full.StartLine == nameRange.StartLine {
		column = full.StartColumn
	}

	return Symbol{
		Document:      doc(path, source).Ref,
		Name:          name,
		Kind:          kind,
		Language:      language,
		NameRange:     nameRange,
		Range:         full,
		Container:     container,
		Preview:       preview,
		PreviewColumn: column,
	}
}

func rng(startLine, startColumn, endLine, endColumn int) search.Range {
	return search.Range{StartLine: startLine, StartColumn: startColumn, EndLine: endLine, EndColumn: endColumn}
}
