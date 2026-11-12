package symbols

import (
	"slices"
	"unicode/utf16"
	"unicode/utf8"
)

// point is a one-based line and UTF-16 column.
type point struct {
	line   int
	column int
}

// positions maps byte offsets of one source to points.
type positions struct {
	offsets []int
	points  []point
}

// newPositions locates the offsets in one pass over the source. Lines end
// at "\n"; invalid UTF-8 counts one column per byte.
func newPositions(source []byte, offsets []int) positions {
	slices.Sort(offsets)
	offsets = slices.Compact(offsets)

	points := make([]point, len(offsets))
	current := point{line: 1, column: 1}

	for index, next := 0, 0; next < len(offsets); {
		for next < len(offsets) && offsets[next] <= index {
			points[next] = current
			next++
		}
		if index >= len(source) {
			break
		}

		char, size := utf8.DecodeRune(source[index:])
		index += size
		current = current.after(char)
	}
	return positions{offsets: offsets, points: points}
}

func (p point) after(char rune) point {
	if char == '\n' {
		return point{line: p.line + 1, column: 1}
	}
	return point{line: p.line, column: p.column + utf16.RuneLen(char)}
}

// span returns the range between two located byte offsets.
func (p positions) span(start, end int) Range {
	from, to := p.at(start), p.at(end)

	return Range{
		StartLine:   from.line,
		StartColumn: from.column,
		EndLine:     to.line,
		EndColumn:   to.column,
	}
}

func (p positions) at(offset int) point {
	index, _ := slices.BinarySearch(p.offsets, offset)
	return p.points[index]
}
