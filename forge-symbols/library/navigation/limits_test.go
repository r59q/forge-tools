package navigation

import (
	"errors"
	"testing"

	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

func span(startLine, startColumn, endLine, endColumn int) symbols.Range {
	return symbols.Range{StartLine: startLine, StartColumn: startColumn, EndLine: endLine, EndColumn: endColumn}
}

// script is a hand-built analysis with calls of run at the file level,
// in run itself, and with an enclosing index out of range.
func script() symbols.Analysis {
	return symbols.Analysis{
		Path:  "main.py",
		Bytes: 60,
		Definitions: []symbols.Definition{
			{Name: "run", Kind: "function", NameRange: span(3, 5, 3, 8), Range: span(3, 1, 4, 10), Parent: symbols.None},
		},
		References: []symbols.Reference{
			{Name: "run", Kind: "call", Range: span(1, 1, 1, 4), Enclosing: symbols.None},
			{Name: "run", Kind: "call", Range: span(4, 5, 4, 8), Enclosing: 0},
			{Name: "run", Kind: "call", Range: span(6, 1, 6, 4), Enclosing: symbols.None},
			{Name: "run", Kind: "call", Range: span(7, 1, 7, 4), Enclosing: 9},
			{Name: "stop", Kind: "call", Range: span(8, 1, 8, 5), Enclosing: symbols.None},
		},
	}
}

func TestIncomingGroupsFileLevelReferences(t *testing.T) {
	result, err := Incoming([]symbols.Analysis{script()}, Target{Name: "run"}, Limits{})
	if err != nil {
		t.Fatal(err)
	}

	check(t, renderIncoming(result), []string{
		"3 main.py:0 (file) : call run 1:1, call run 6:1, call run 7:1",
		"3 main.py:3 run function: call run 4:5",
	})
	for _, group := range result.Groups {
		checkHeuristic(t, []DefinitionResult{group.Caller}, group.References)
	}
}

func TestOutgoingEndsOnParentCycles(t *testing.T) {
	analysis := symbols.Analysis{
		Path: "cycle.go",
		Definitions: []symbols.Definition{
			{Name: "a", Kind: "function", NameRange: span(1, 6, 1, 7), Range: span(1, 1, 1, 20), Parent: 1},
			{Name: "b", Kind: "function", NameRange: span(2, 6, 2, 7), Range: span(2, 1, 2, 20), Parent: 0},
			{Name: "c", Kind: "function", NameRange: span(3, 6, 3, 7), Range: span(3, 1, 3, 20), Parent: symbols.None},
		},
		References: []symbols.Reference{
			{Name: "x", Kind: "call", Range: span(1, 10, 1, 11), Enclosing: 0},
		},
	}

	result, err := Outgoing([]symbols.Analysis{analysis}, Target{Name: "c"}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	check(t, renderOutgoing(result), []string{"3 cycle.go:3 c function: "})
}

func TestResultLimit(t *testing.T) {
	files := analyze(t, shop)
	log := Target{Path: "store.go", Line: 19, Column: 17}

	tests := []struct {
		name      string
		results   int
		want      []string
		truncated bool
	}{
		{
			name:    "everything fits",
			results: 9,
			want: []string{
				"1 store.go:5 Store.Open method: call log 8:4",
				"1 store.go:12 Store.Close method: call log 13:4",
				"2 store_extra.go:3 Store.Reset method: call log 4:4, call log 5:4",
				"3 cart.go:5 Cart.Open method: call log 6:4",
			},
		},
		{
			name:    "last group dropped",
			results: 8,
			want: []string{
				"1 store.go:5 Store.Open method: call log 8:4",
				"1 store.go:12 Store.Close method: call log 13:4",
				"2 store_extra.go:3 Store.Reset method: call log 4:4, call log 5:4",
			},
			truncated: true,
		},
		{
			name:    "group cut",
			results: 6,
			want: []string{
				"1 store.go:5 Store.Open method: call log 8:4",
				"1 store.go:12 Store.Close method: call log 13:4",
				"2 store_extra.go:3 Store.Reset method: call log 4:4",
			},
			truncated: true,
		},
		{
			name:      "nothing fits",
			results:   1,
			truncated: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Incoming(files, log, Limits{Results: test.results})
			if err != nil {
				t.Fatal(err)
			}

			check(t, renderIncoming(result), test.want)
			if result.Truncated != test.truncated {
				t.Errorf("truncated = %v, want %v", result.Truncated, test.truncated)
			}
		})
	}
}

func TestResultLimitOfOutgoingAndDefinitions(t *testing.T) {
	files := analyze(t, shop)

	outgoing, err := Outgoing(files, Target{Name: "Open"}, Limits{Results: 5})
	if err != nil {
		t.Fatal(err)
	}
	check(t, renderOutgoing(outgoing), []string{
		"3 cart.go:5 Cart.Open method: type Cart 5:10, type error 5:23, call log 6:4, call validate 7:9",
	})
	if !outgoing.Truncated {
		t.Error("outgoing not truncated")
	}

	definitions, err := Definitions(files, Target{Name: "log"}, Limits{Results: 2})
	if err != nil {
		t.Fatal(err)
	}
	check(t, renderDefinitions(definitions.Definitions), []string{
		"3 cart.go:10 Cart.log method",
		"3 store.go:19 Store.log method",
	})
	if !definitions.Truncated {
		t.Error("definitions not truncated")
	}

	exact, err := Definitions(files, Target{Name: "log"}, Limits{Results: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(exact.Definitions) != 3 || exact.Truncated {
		t.Errorf("got %d definitions, truncated %v", len(exact.Definitions), exact.Truncated)
	}
}

// helpers runs each helper and returns its error.
var helpers = map[string]func([]symbols.Analysis, Target, Limits) error{
	"Outgoing": func(files []symbols.Analysis, target Target, limits Limits) error {
		_, err := Outgoing(files, target, limits)
		return err
	},
	"Incoming": func(files []symbols.Analysis, target Target, limits Limits) error {
		_, err := Incoming(files, target, limits)
		return err
	},
	"Definitions": func(files []symbols.Analysis, target Target, limits Limits) error {
		_, err := Definitions(files, target, limits)
		return err
	},
}

func TestRequestLimits(t *testing.T) {
	files := analyze(t, shop)
	target := Target{Name: "log"}

	tests := []struct {
		name   string
		limits Limits
		want   error
	}{
		{"defaults", Limits{}, nil},
		{"files at the limit", Limits{Files: len(files)}, nil},
		{"too many files", Limits{Files: len(files) - 1}, symbols.ErrTooLarge},
		{"too many bytes", Limits{TotalBytes: 100}, symbols.ErrTooLarge},
	}
	for _, test := range tests {
		for name, helper := range helpers {
			t.Run(test.name+"/"+name, func(t *testing.T) {
				if err := helper(files, target, test.limits); !errors.Is(err, test.want) {
					t.Errorf("error = %v, want %v", err, test.want)
				}
			})
		}
	}
}

func TestTargetErrors(t *testing.T) {
	files := analyze(t, shop)

	tests := []struct {
		name   string
		target Target
		want   error
	}{
		{"empty", Target{}, ErrInvalidTarget},
		{"line without column", Target{Path: "store.go", Line: 5}, ErrInvalidTarget},
		{"column without line", Target{Name: "log", Column: 5}, ErrInvalidTarget},
		{"negative position", Target{Path: "store.go", Line: -1, Column: -1}, ErrInvalidTarget},
		{"position without path", Target{Line: 5, Column: 18}, ErrInvalidTarget},
		{"unknown path", Target{Path: "missing.go", Line: 5, Column: 18}, ErrNotFound},
		{"position outside any tag", Target{Path: "store.go", Line: 1, Column: 1}, ErrNotFound},
		{"name not at the position", Target{Name: "load", Path: "store.go", Line: 5, Column: 18}, ErrNotFound},
		{"kind not at the position", Target{Kind: "function", Path: "store.go", Line: 5, Column: 18}, ErrNotFound},
	}
	for _, test := range tests {
		for name, helper := range helpers {
			t.Run(test.name+"/"+name, func(t *testing.T) {
				if err := helper(files, test.target, Limits{}); !errors.Is(err, test.want) {
					t.Errorf("error = %v, want %v", err, test.want)
				}
			})
		}
	}
}
