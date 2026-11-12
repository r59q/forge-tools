package symbols

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files from the current output")

// TestGolden analyzes every testdata/<language>/<case>.input and compares
// the result with <case>.golden. Run with -update to rewrite the golden files.
func TestGolden(t *testing.T) {
	inputs, err := filepath.Glob(filepath.Join("testdata", "*", "*.input"))
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) == 0 {
		t.Fatal("no golden inputs")
	}

	analyzer := newAnalyzer(t, Limits{})
	covered := map[string]bool{}

	for _, input := range inputs {
		language := filepath.Base(filepath.Dir(input))
		covered[language] = true

		name := language + "/" + strings.TrimSuffix(filepath.Base(input), ".input")
		t.Run(name, func(t *testing.T) {
			checkGolden(t, analyzer, language, input)
		})
	}

	for _, language := range Languages() {
		if !covered[language] {
			t.Errorf("language %s has no golden tests", language)
		}
	}
}

func checkGolden(t *testing.T, analyzer *Analyzer, language, input string) {
	t.Helper()

	content, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}

	analysis, err := analyzer.Analyze(context.Background(), File{Path: input, Content: content, Language: language})
	if err != nil {
		t.Fatal(err)
	}

	got := render(analysis)
	golden := strings.TrimSuffix(input, ".input") + ".golden"

	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run go test -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs (run go test -update to accept)\n--- got ---\n%s--- want ---\n%s", golden, got, want)
	}
}

// render prints an analysis one tag per line:
//
//	[index] kind name name-range range=full-range container=name parent=[index]
//	kind name range in=[index] enclosing-name
func render(analysis Analysis) string {
	var out strings.Builder

	out.WriteString("definitions:\n")
	for index, definition := range analysis.Definitions {
		fmt.Fprintf(&out, "  [%d] %s %s %s range=%s", index, definition.Kind, definition.Name,
			renderRange(definition.NameRange), renderRange(definition.Range))
		if definition.Container != "" {
			fmt.Fprintf(&out, " container=%s", definition.Container)
		}
		if definition.Parent != None {
			fmt.Fprintf(&out, " parent=[%d]", definition.Parent)
		}
		out.WriteString("\n")
	}

	out.WriteString("references:\n")
	for _, reference := range analysis.References {
		fmt.Fprintf(&out, "  %s %s %s", reference.Kind, reference.Name, renderRange(reference.Range))
		if enclosing, ok := analysis.Enclosing(reference); ok {
			fmt.Fprintf(&out, " in=[%d] %s", reference.Enclosing, enclosing.Name)
		}
		out.WriteString("\n")
	}
	return out.String()
}

func renderRange(r Range) string {
	return fmt.Sprintf("%d:%d-%d:%d", r.StartLine, r.StartColumn, r.EndLine, r.EndColumn)
}

func newAnalyzer(t testing.TB, limits Limits) *Analyzer {
	t.Helper()

	analyzer := New(limits)
	t.Cleanup(func() {
		if err := analyzer.Close(context.Background()); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return analyzer
}
