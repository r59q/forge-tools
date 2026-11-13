package navigation

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

// shop is a Go package spread over several files. Store, Cart and Ledger
// all have a log method.
var shop = []symbols.File{
	{Path: "store.go", Content: []byte(`package shop

type Store struct{}

func (s *Store) Open() error {
	type state struct{ owner *Store }
	s.load()
	s.log("open")
	return validate(s)
}

func (s *Store) Close() error {
	s.log("close")
	return nil
}

func (s *Store) load() {}

func (s *Store) log(message string) {}
`)},
	{Path: "cart.go", Content: []byte(`package shop

type Cart struct{}

func (c *Cart) Open() error {
	c.log("cart")
	return validate(c)
}

func (c *Cart) log(message string) {}
`)},
	{Path: "store_extra.go", Content: []byte(`package shop

func (s *Store) Reset() {
	s.log("reset")
	s.log("again")
}

type Ledger struct{}

func (l Ledger) log(message string) {}
`)},
	{Path: "checks.go", Content: []byte(`package shop

func validate(value any) error { return nil }
`)},
}

// printer has an overloaded Java method.
var printer = []symbols.File{
	{Path: "Printer.java", Content: []byte(`class Printer {
    void print(String text) { write(text); }

    void print(int number) { print(String.valueOf(number)); }

    void write(String text) {}
}
`)},
}

func analyze(t *testing.T, files []symbols.File) []symbols.Analysis {
	t.Helper()

	analyzer := symbols.New(symbols.Limits{})
	t.Cleanup(func() {
		if err := analyzer.Close(context.Background()); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	analyses, err := analyzer.AnalyzeFiles(context.Background(), files)
	if err != nil {
		t.Fatal(err)
	}
	return analyses
}

// renderDefinition prints "tier path:line container.name kind".
func renderDefinition(result DefinitionResult) string {
	definition := result.Definition
	name := definition.Name
	if definition.Container != "" {
		name = definition.Container + "." + name
	}
	if result.Index == symbols.None {
		name = "(file)"
	}
	return fmt.Sprintf("%d %s:%d %s %s", result.Tier, result.Path, definition.NameRange.StartLine, name, definition.Kind)
}

// renderReferences prints "kind name line:column", marking nested ones.
func renderReferences(results []ReferenceResult) string {
	var rendered []string
	for _, result := range results {
		reference := result.Reference
		line := fmt.Sprintf("%s %s %d:%d", reference.Kind, reference.Name, reference.Range.StartLine, reference.Range.StartColumn)
		if result.Nested {
			line += " nested"
		}
		rendered = append(rendered, line)
	}
	return strings.Join(rendered, ", ")
}

func renderDefinitions(results []DefinitionResult) []string {
	var rendered []string
	for _, result := range results {
		rendered = append(rendered, renderDefinition(result))
	}
	return rendered
}

func renderOutgoing(result OutgoingResult) []string {
	var rendered []string
	for _, group := range result.Groups {
		rendered = append(rendered, renderDefinition(group.Definition)+": "+renderReferences(group.References))
	}
	return rendered
}

func renderIncoming(result IncomingResult) []string {
	var rendered []string
	for _, group := range result.Groups {
		rendered = append(rendered, renderDefinition(group.Caller)+": "+renderReferences(group.References))
	}
	return rendered
}

func check(t *testing.T, got, want []string) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Errorf("got\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
}

func checkHeuristic(t *testing.T, definitions []DefinitionResult, references []ReferenceResult) {
	t.Helper()

	for _, result := range definitions {
		if result.Precision != Heuristic {
			t.Errorf("definition %s has precision %q", renderDefinition(result), result.Precision)
		}
	}
	for _, result := range references {
		if result.Precision != Heuristic {
			t.Errorf("reference %s has precision %q", renderReferences([]ReferenceResult{result}), result.Precision)
		}
	}
}

func TestOutgoing(t *testing.T) {
	files := analyze(t, shop)

	tests := []struct {
		name   string
		target Target
		want   []string
	}{
		{
			name:   "position on the name",
			target: Target{Path: "store.go", Line: 5, Column: 18},
			want: []string{
				"1 store.go:5 Store.Open method: type Store 5:10, type error 5:24, type Store 6:28 nested, " +
					"call load 7:4, call log 8:4, call validate 9:9",
			},
		},
		{
			name:   "position in the body",
			target: Target{Path: "store.go", Line: 13, Column: 1},
			want:   []string{"1 store.go:12 Store.Close method: type Store 12:10, type error 12:25, call log 13:4"},
		},
		{
			name:   "name narrows the position",
			target: Target{Name: "Open", Path: "store.go", Line: 7, Column: 4},
			want: []string{
				"1 store.go:5 Store.Open method: type Store 5:10, type error 5:24, type Store 6:28 nested, " +
					"call load 7:4, call log 8:4, call validate 9:9",
			},
		},
		{
			name:   "name in several containers",
			target: Target{Name: "Open"},
			want: []string{
				"3 cart.go:5 Cart.Open method: type Cart 5:10, type error 5:23, call log 6:4, call validate 7:9",
				"3 store.go:5 Store.Open method: type Store 5:10, type error 5:24, type Store 6:28 nested, " +
					"call load 7:4, call log 8:4, call validate 9:9",
			},
		},
		{
			name:   "position on a call targets its definitions",
			target: Target{Path: "store.go", Line: 9, Column: 9},
			want:   []string{"3 checks.go:3 validate function: type any 3:21, type error 3:26"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Outgoing(files, test.target, Limits{})
			if err != nil {
				t.Fatal(err)
			}

			check(t, renderOutgoing(result), test.want)
			if result.Truncated {
				t.Error("truncated")
			}
			for _, group := range result.Groups {
				checkHeuristic(t, []DefinitionResult{group.Definition}, group.References)
			}
		})
	}
}

func TestIncomingRanksCallersAcrossFiles(t *testing.T) {
	files := analyze(t, shop)

	// The name of Store.log: callers in store.go, then methods of Store in
	// other files, then the rest.
	result, err := Incoming(files, Target{Path: "store.go", Line: 19, Column: 17}, Limits{})
	if err != nil {
		t.Fatal(err)
	}

	check(t, renderIncoming(result), []string{
		"1 store.go:5 Store.Open method: call log 8:4",
		"1 store.go:12 Store.Close method: call log 13:4",
		"2 store_extra.go:3 Store.Reset method: call log 4:4, call log 5:4",
		"3 cart.go:5 Cart.Open method: call log 6:4",
	})
	if result.Name != "log" || result.Truncated {
		t.Errorf("name %q, truncated %v", result.Name, result.Truncated)
	}
	for _, group := range result.Groups {
		checkHeuristic(t, []DefinitionResult{group.Caller}, group.References)
	}
}

func TestDefinitionsRanksTiers(t *testing.T) {
	files := analyze(t, shop)

	tests := []struct {
		name   string
		target Target
		want   []string
	}{
		{
			name:   "from a call in a method of Store",
			target: Target{Path: "store_extra.go", Line: 4, Column: 5},
			want: []string{
				"1 store_extra.go:10 Ledger.log method",
				"2 store.go:19 Store.log method",
				"3 cart.go:10 Cart.log method",
			},
		},
		{
			name:   "name only",
			target: Target{Name: "log"},
			want: []string{
				"3 cart.go:10 Cart.log method",
				"3 store.go:19 Store.log method",
				"3 store_extra.go:10 Ledger.log method",
			},
		},
		{
			name:   "name and path",
			target: Target{Name: "log", Path: "store.go"},
			want: []string{
				"1 store.go:19 Store.log method",
				"3 cart.go:10 Cart.log method",
				"3 store_extra.go:10 Ledger.log method",
			},
		},
		{
			name:   "kind filter",
			target: Target{Name: "Store", Kind: "struct"},
			want:   []string{"3 store.go:3 Store struct"},
		},
		{
			name:   "kind without matches",
			target: Target{Name: "Store", Kind: "function"},
		},
		{
			name:   "unknown name",
			target: Target{Name: "missing"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Definitions(files, test.target, Limits{})
			if err != nil {
				t.Fatal(err)
			}

			check(t, renderDefinitions(result.Definitions), test.want)
			checkHeuristic(t, result.Definitions, nil)
		})
	}
}

func TestOverloadsAreAllCandidates(t *testing.T) {
	files := analyze(t, printer)
	target := Target{Name: "print"}

	definitions, err := Definitions(files, target, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	check(t, renderDefinitions(definitions.Definitions), []string{
		"3 Printer.java:2 Printer.print method",
		"3 Printer.java:4 Printer.print method",
	})

	outgoing, err := Outgoing(files, target, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	check(t, renderOutgoing(outgoing), []string{
		"3 Printer.java:2 Printer.print method: type String 2:16, call write 2:31",
		"3 Printer.java:4 Printer.print method: call print 4:30, call valueOf 4:43",
	})

	// From the call in the second overload: it cannot tell which one it calls.
	incoming, err := Incoming(files, Target{Path: "Printer.java", Line: 4, Column: 30}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	check(t, renderIncoming(incoming), []string{"1 Printer.java:4 Printer.print method: call print 4:30"})
}
