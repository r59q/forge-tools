package treesitter_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-symbols/library/internal/grammars"
	"github.com/r59q/forge-tools/forge-symbols/library/internal/treesitter"
)

const memoryBytes = 64 << 20

func newEngine(t *testing.T, memoryBytes int) *treesitter.Engine {
	t.Helper()

	engine := treesitter.New(memoryBytes)
	t.Cleanup(func() {
		if err := engine.Close(context.Background()); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return engine
}

// goGrammar pairs the bundled Go parser with a query of the test.
func goGrammar(t *testing.T, query string) func() (treesitter.Grammar, error) {
	t.Helper()

	module, err := grammars.Languages()[0].Module()
	if err != nil {
		t.Fatal(err)
	}
	return func() (treesitter.Grammar, error) {
		return treesitter.Grammar{Module: module, Query: query}, nil
	}
}

func captureTexts(matches []treesitter.Match, source string) string {
	var texts []string
	for _, match := range matches {
		for _, capture := range match.Captures {
			texts = append(texts, capture.Name+"="+source[capture.Start:capture.End])
		}
	}
	return strings.Join(texts, " ")
}

func TestMatchesApplyPredicates(t *testing.T) {
	engine := newEngine(t, memoryBytes)
	query := `
(function_declaration name: (identifier) @name (#match? @name "^Test")) @definition.test
(call_expression function: (identifier) @name (#any-of? @name "panic" "recover") (#set! "kind" "builtin")) @reference.call
`
	language, err := engine.Language(context.Background(), "go", goGrammar(t, query))
	if err != nil {
		t.Fatal(err)
	}

	source := "package p\n\nfunc TestA() { panic(1) }\n\nfunc helper() { print(2); recover() }\n"
	matches, err := language.Matches(context.Background(), []byte(source))
	if err != nil {
		t.Fatal(err)
	}

	want := "definition.test=func TestA() { panic(1) } name=TestA " +
		"reference.call=panic(1) name=panic reference.call=recover() name=recover"
	if got := captureTexts(matches, source); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestQueryErrors(t *testing.T) {
	engine := newEngine(t, memoryBytes)

	queries := map[string]string{
		"syntax":      "(function_declaration",
		"node type":   "(no_such_node) @name",
		"predicate":   `((identifier) @name (#is-not? local))`,
		"bad pattern": `((identifier) @name (#match? @name "("))`,
	}
	for name, query := range queries {
		if _, err := engine.Language(context.Background(), name, goGrammar(t, query)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}

	// A failed language does not break the engine.
	if _, err := engine.Language(context.Background(), "go", goGrammar(t, "(identifier) @name")); err != nil {
		t.Error(err)
	}
}

func TestLanguageLoadsOnce(t *testing.T) {
	engine := newEngine(t, memoryBytes)
	grammar := goGrammar(t, "(identifier) @name")

	var mu sync.Mutex
	loads := 0
	load := func() (treesitter.Grammar, error) {
		mu.Lock()
		defer mu.Unlock()
		loads++
		return grammar()
	}

	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			language, err := engine.Language(context.Background(), "go", load)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := language.Matches(context.Background(), []byte("package p\nvar x = y\n")); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()

	if loads != 1 {
		t.Errorf("grammar loaded %d times, want once", loads)
	}
}

func TestLoadErrorIsReturned(t *testing.T) {
	engine := newEngine(t, memoryBytes)
	failure := errors.New("no grammar")

	_, err := engine.Language(context.Background(), "go", func() (treesitter.Grammar, error) {
		return treesitter.Grammar{}, failure
	})
	if !errors.Is(err, failure) {
		t.Errorf("got %v, want the load error", err)
	}

	_, err = engine.Language(context.Background(), "junk", func() (treesitter.Grammar, error) {
		return treesitter.Grammar{Module: []byte("not wasm")}, nil
	})
	if err == nil {
		t.Error("junk module: want an error")
	}
}

func TestMemoryLimit(t *testing.T) {
	engine := newEngine(t, 8<<20)

	language, err := engine.Language(context.Background(), "go", goGrammar(t, "(identifier) @name"))
	if err != nil {
		t.Fatal(err)
	}

	// Deeply nested input needs far more than 8 MiB.
	deep := bytes.Repeat([]byte("("), 1<<20)
	if _, err := language.Matches(context.Background(), deep); !errors.Is(err, treesitter.ErrMemory) {
		t.Errorf("deep input: got %v, want ErrMemory", err)
	}

	// Source that does not fit at all fails the same way.
	huge := make([]byte, 16<<20)
	if _, err := language.Matches(context.Background(), huge); !errors.Is(err, treesitter.ErrMemory) {
		t.Errorf("huge input: got %v, want ErrMemory", err)
	}

	if _, err := language.Matches(context.Background(), []byte("package p")); err != nil {
		t.Errorf("after the limit was hit: %v", err)
	}
}

func TestMatchesStopWithContext(t *testing.T) {
	engine := newEngine(t, memoryBytes)

	language, err := engine.Language(context.Background(), "go", goGrammar(t, "(identifier) @name"))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := language.Matches(ctx, []byte("package p\nvar x = y\n")); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

func TestClosedEngine(t *testing.T) {
	engine := treesitter.New(memoryBytes)
	grammar := goGrammar(t, "(identifier) @name")

	language, err := engine.Language(context.Background(), "go", grammar)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := engine.Language(context.Background(), "go", grammar); err == nil {
		t.Error("language of a closed engine: want an error")
	}
	if _, err := language.Matches(context.Background(), []byte("package p")); err == nil {
		t.Error("matches on a closed engine: want an error")
	}

	// An engine that never started closes without error.
	if err := treesitter.New(memoryBytes).Close(context.Background()); err != nil {
		t.Error(err)
	}
}
