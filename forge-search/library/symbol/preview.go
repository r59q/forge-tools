package symbol

import (
	"unicode/utf16"
	"unicode/utf8"

	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

// lineStarts returns the byte offset of each line of content. Lines end at
// "\n".
func lineStarts(content []byte) []int {
	starts := []int{0}
	for offset, value := range content {
		if value == '\n' {
			starts = append(starts, offset+1)
		}
	}
	return starts
}

// line returns the text of a one-based line, without its "\n".
func line(content []byte, starts []int, number int) ([]byte, bool) {
	if number < 1 || number > len(starts) {
		return nil, false
	}

	start := starts[number-1]
	end := len(content)
	if number < len(starts) {
		end = starts[number] - 1
	}
	return content[start:end], true
}

// declaration returns the preview of a definition and the column where it
// starts: the line of its name, cut to the declaration where it starts or
// ends on that line, without leading blanks. It reports false when the
// name is not on one line of content.
func declaration(content []byte, starts []int, definition symbols.Definition) (string, int, bool) {
	name := definition.NameRange
	text, ok := line(content, starts, name.StartLine)
	if !ok || name.EndLine != name.StartLine {
		return "", 0, false
	}

	at, atOK := offset(text, name.StartColumn)
	end, endOK := offset(text, name.EndColumn)
	if !atOK || !endOK || end < at {
		return "", 0, false
	}

	span := definition.Range
	from, to := 0, len(text)
	if span.StartLine == name.StartLine {
		from = min(at, clamp(text, span.StartColumn, 0))
	}
	if span.EndLine == name.StartLine {
		to = max(end, clamp(text, span.EndColumn, len(text)))
	}
	for from < at && (text[from] == ' ' || text[from] == '\t') {
		from++
	}

	preview, column := search.LinePreview(text[from:to], at-from, end-at)
	return preview, column + units(text[:from]), true
}

// clamp returns the byte offset of a column of text, or fallback when the
// column is not in it.
func clamp(text []byte, column, fallback int) int {
	if at, ok := offset(text, column); ok {
		return at
	}
	return fallback
}

// offset returns the byte offset of a one-based UTF-16 column of text. The
// column just after the end is valid.
func offset(text []byte, column int) (int, bool) {
	remaining := column - 1
	at := 0

	for remaining > 0 && at < len(text) {
		char, size := utf8.DecodeRune(text[at:])
		remaining -= utf16.RuneLen(char)
		at += size
	}
	return at, remaining == 0
}

// units counts the UTF-16 units of valid UTF-8 text.
func units(text []byte) int {
	count := 0
	for len(text) > 0 {
		char, size := utf8.DecodeRune(text)
		count += utf16.RuneLen(char)
		text = text[size:]
	}
	return count
}
