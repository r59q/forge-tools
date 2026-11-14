package search

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLinePreview(t *testing.T) {
	long := strings.Repeat("a", 300)
	astral := strings.Repeat("😀", 150) // 300 UTF-16 units

	tests := []struct {
		name       string
		line       string
		at, length int
		want       string
		wantColumn int
	}{
		{"short", "find me", 5, 2, "find me", 1},
		{"carriage return", "x\r", 0, 1, "x", 1},
		{"cut at end", long, 0, 1, long[:PreviewUnits] + previewCut, 1},
		{"match near the end", long + "b", 300, 1, previewCut + long[:previewLead] + "b", 301 - previewLead},
		{"match in the middle", long + "b" + long, 300, 1,
			previewCut + long[:previewLead] + "b" + long[:PreviewUnits-previewLead-1] + previewCut, 301 - previewLead},
		{"astral cut", astral, 0, 4, strings.Repeat("😀", PreviewUnits/2) + previewCut, 1},
		{"long match", long, 0, 256, long[:PreviewUnits] + previewCut, 1},
		{"long match late", "b" + long, 1, 256, previewCut + long[:PreviewUnits] + previewCut, 2},
	}

	for _, test := range tests {
		preview, column := linePreview([]byte(test.line), test.at, test.length)

		if preview != test.want || column != test.wantColumn {
			t.Errorf("%s: preview = %q at %d, want %q at %d", test.name, preview, column, test.want, test.wantColumn)
		}
	}
}

func TestLinePreviewKeepsRunesWhole(t *testing.T) {
	line := strings.Repeat("a", 250) + strings.Repeat("😀", 100) + "x"
	at := strings.LastIndex(line, "x")

	preview, column := linePreview([]byte(line), at, 1)

	if !utf8.ValidString(preview) || !strings.HasSuffix(preview, "😀x") {
		t.Errorf("preview = %q, want whole runes ending in the match", preview)
	}
	if units := utf16Length([]byte(strings.TrimPrefix(preview, previewCut))); units > PreviewUnits {
		t.Errorf("preview has %d units, want at most %d", units, PreviewUnits)
	}
	// 250 + 200 units before "x", of which previewLead (an even number) are shown.
	if want := 451 - previewLead; column != want {
		t.Errorf("column = %d, want %d", column, want)
	}
}

func TestUnindexedPreviewShowsMatchOnLongLines(t *testing.T) {
	line := strings.Repeat("😀", 200) + "needle"

	results := mustSearch(t, Query{Text: "needle"}, []Document{code("a", line)}, Limits{})

	match := results.Matches[0]
	if match.Range != span(1, 401, 407) {
		t.Errorf("range = %+v", match.Range)
	}
	if !strings.HasSuffix(match.Preview, "needle") || match.PreviewColumn != 401-previewLead {
		t.Errorf("preview = %q at %d", match.Preview, match.PreviewColumn)
	}
}
