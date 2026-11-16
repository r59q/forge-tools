package symbol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

const (
	testIndex  = "symbols-1"
	testSource = "checkout-1"

	// secret is document content that must never appear in an error.
	secret = "s3cr3t-content"
)

// fakeAnalyzer defines a symbol for each line of the form
// "@kind name [container]", with the whole line as its range. Content
// holding one of the words of failures returns that error instead.
type fakeAnalyzer struct {
	mu       sync.Mutex
	analyzed []string
}

func failures() map[string]error {
	return map[string]error{
		"TIMEOUT":     symbols.ErrTimeout,
		"MEMORY":      symbols.ErrMemory,
		"HUGE":        symbols.ErrTooLarge,
		"UNSUPPORTED": symbols.ErrUnsupported,
		"BOOM":        errors.New("parser crashed"),
	}
}

func (a *fakeAnalyzer) Analyze(ctx context.Context, file symbols.File) (symbols.Analysis, error) {
	a.mu.Lock()
	a.analyzed = append(a.analyzed, file.Path)
	a.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return symbols.Analysis{}, err
	}
	for word, err := range failures() {
		if bytes.Contains(file.Content, []byte(word)) {
			return symbols.Analysis{}, err
		}
	}

	analysis := symbols.Analysis{Path: file.Path, Language: file.Language, Bytes: len(file.Content)}
	for number, text := range strings.Split(string(file.Content), "\n") {
		if definition, ok := fakeDefinition(number+1, text); ok {
			analysis.Definitions = append(analysis.Definitions, definition)
		}
	}
	return analysis, nil
}

func fakeDefinition(line int, text string) (symbols.Definition, bool) {
	fields := strings.Fields(strings.TrimSuffix(text, "\r"))
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "@") {
		return symbols.Definition{}, false
	}

	start := strings.Index(text, " "+fields[1]) + 2
	definition := symbols.Definition{
		Name:      fields[1],
		Kind:      fields[0][1:],
		NameRange: symbols.Range{StartLine: line, StartColumn: start, EndLine: line, EndColumn: start + len(fields[1])},
		Range:     symbols.Range{StartLine: line, StartColumn: 1, EndLine: line, EndColumn: len(text) + 1},
		Parent:    symbols.None,
	}
	if len(fields) > 2 {
		definition.Container = fields[2]
	}
	return definition, true
}

func (a *fakeAnalyzer) calls() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string{}, a.analyzed...)
}

func doc(path, content string) search.Document {
	return search.Document{
		Ref:     search.Ref{Kind: search.KindCode, Source: testSource, ID: path, Version: version(content)},
		Content: []byte(content),
	}
}

func newEngine(t *testing.T) *search.Engine {
	t.Helper()

	engine := search.NewEngine(&search.MemoryStore{}, search.IndexLimits{})
	if err := engine.CreateIndex(context.Background(), testIndex); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}
	return engine
}

func mustSearch(t *testing.T, analyzer Analyzer, documents []search.Document, query Query) Results {
	t.Helper()

	results, err := Search(context.Background(), analyzer, documents, query, Limits{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	return results
}

func mustSearchIndex(t *testing.T, engine *search.Engine, query Query) Results {
	t.Helper()

	results, err := SearchIndex(context.Background(), engine, testIndex, query, Limits{})
	if err != nil {
		t.Fatalf("SearchIndex: %v", err)
	}
	return results
}

func mustUpdate(t *testing.T, analyzer Analyzer, engine *search.Engine, change Change) UpdateResult {
	t.Helper()

	result, err := Update(context.Background(), analyzer, engine, testIndex, change, Limits{})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	return result
}

// names returns "path:name" for each symbol.
func names(results Results) []string {
	found := []string{}
	for _, symbol := range results.Symbols {
		found = append(found, symbol.Document.ID+":"+symbol.Name)
	}
	return found
}

// wantError checks the category of an error and that it shows no content.
func wantError(t *testing.T, err, want error) {
	t.Helper()

	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Errorf("error %q contains document content", err)
	}
}

// fixedAnalyzer returns the same definitions for every file.
type fixedAnalyzer []symbols.Definition

func definitions(list ...symbols.Definition) fixedAnalyzer {
	return list
}

func (a fixedAnalyzer) Analyze(_ context.Context, file symbols.File) (symbols.Analysis, error) {
	return symbols.Analysis{Path: file.Path, Language: file.Language, Definitions: a}, nil
}

// definition returns a one-line definition whose name starts at a column.
func definition(name, kind string, line, column int) symbols.Definition {
	name16 := len(utf16.Encode([]rune(name)))

	return symbols.Definition{
		Name:      name,
		Kind:      kind,
		NameRange: symbols.Range{StartLine: line, StartColumn: column, EndLine: line, EndColumn: column + name16},
		Range:     symbols.Range{StartLine: line, StartColumn: column, EndLine: line, EndColumn: column + name16},
		Parent:    symbols.None,
	}
}

func version(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:8])
}
