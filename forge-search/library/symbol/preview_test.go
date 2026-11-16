package symbol

import (
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

func TestDeclarationPreview(t *testing.T) {
	long := strings.Repeat("x", 300)

	tests := []struct {
		name       string
		content    string
		definition symbols.Definition
		preview    string
		column     int
	}{
		{
			name:       "range on the name line",
			content:    "type T struct{}; func Fn() {}",
			definition: span(definition("Fn", "function", 1, 23), 1, 18, 1, 30),
			preview:    "func Fn() {}",
			column:     18,
		},
		{
			name:       "range from an earlier line",
			content:    "@Override\n\tpublic void run() {\n\t}",
			definition: span(definition("run", "method", 2, 14), 1, 1, 3, 3),
			preview:    "public void run() {",
			column:     2,
		},
		{
			name:       "range ends on the name line",
			content:    "  x = 1; y = 2",
			definition: span(definition("x", "variable", 1, 3), 1, 3, 1, 8),
			preview:    "x = 1",
			column:     3,
		},
		{
			name:       "carriage return",
			content:    "def f():\r\n  pass",
			definition: span(definition("f", "function", 1, 5), 1, 1, 2, 7),
			preview:    "def f():",
			column:     1,
		},
		{
			name:       "UTF-16 columns",
			content:    "const 𝔸 = 1, ö = 2",
			definition: definition("ö", "constant", 1, 15),
			preview:    "ö",
			column:     15,
		},
		{
			name:       "long line",
			content:    "var " + long + "Name = 1",
			definition: span(definition("Name", "variable", 1, 305), 1, 1, 1, 313),
			preview:    "…" + strings.Repeat("x", 40) + "Name = 1",
			column:     265,
		},
	}
	for _, test := range tests {
		content := []byte(test.content)
		preview, column, ok := declaration(content, lineStarts(content), test.definition)

		if !ok || preview != test.preview || column != test.column {
			t.Errorf("%s: preview = %q at %d, %v, want %q at %d", test.name, preview, column, ok, test.preview, test.column)
		}
	}
}

func TestDeclarationRejectsNamesOutsideContent(t *testing.T) {
	content := []byte("func a() {}\n")

	bad := []symbols.Definition{
		definition("a", "function", 0, 1),
		definition("a", "function", 3, 1),
		definition("a", "function", 1, 20),
		span(definition("a", "function", 1, 6), 1, 1, 2, 1),
	}
	bad[3].NameRange.EndLine = 2

	for _, each := range bad {
		if preview, _, ok := declaration(content, lineStarts(content), each); ok {
			t.Errorf("declaration(%+v) = %q, want none", each.NameRange, preview)
		}
	}
}

// span sets the range of a definition.
func span(d symbols.Definition, startLine, startColumn, endLine, endColumn int) symbols.Definition {
	d.Range = symbols.Range{StartLine: startLine, StartColumn: startColumn, EndLine: endLine, EndColumn: endColumn}
	return d
}
