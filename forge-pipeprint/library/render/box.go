package render

import (
	"strings"
	"unicode/utf8"
)

// Box size limits, in characters inside the border.
const (
	minInner = 12
	maxInner = 40
)

// charset is the characters used for drawing.
type charset struct {
	topLeft, topRight, bottomLeft, bottomRight string
	horizontal, vertical                       string
	arrow, needs, step, ellipsis, separator    string
}

func unicodeChars() charset {
	return charset{
		topLeft: "┌", topRight: "┐", bottomLeft: "└", bottomRight: "┘",
		horizontal: "─", vertical: "│",
		arrow: "▼", needs: "←", step: "•", ellipsis: "…", separator: "·",
	}
}

func asciiChars() charset {
	return charset{
		topLeft: "+", topRight: "+", bottomLeft: "+", bottomRight: "+",
		horizontal: "-", vertical: "|",
		arrow: "v", needs: "<-", step: "*", ellipsis: "~", separator: "-",
	}
}

// box draws lines in a border, padded to a common width.
func box(lines []string, c charset) []string {
	inner := minInner
	for _, line := range lines {
		inner = max(inner, width(line))
	}

	inner = min(inner, maxInner)
	edge := strings.Repeat(c.horizontal, inner+2)

	out := []string{c.topLeft + edge + c.topRight}
	for _, line := range lines {
		line = truncate(line, inner, c.ellipsis)
		out = append(out, c.vertical+" "+pad(line, inner)+" "+c.vertical)
	}

	return append(out, c.bottomLeft+edge+c.bottomRight)
}

// row places boxes side by side, wrapping before limit columns.
func row(boxes [][]string, limit int) []string {
	var out []string
	var current [][]string
	used := 0
	for _, b := range boxes {
		w := width(b[0])
		if len(current) > 0 && used+1+w > limit {
			out = append(out, join(current)...)
			current, used = nil, 0
		}

		if len(current) > 0 {
			used++
		}

		current = append(current, b)
		used += w
	}

	return append(out, join(current)...)
}

// join places boxes side by side, padding shorter ones.
func join(boxes [][]string) []string {
	height := 0
	for _, b := range boxes {
		height = max(height, len(b))
	}

	lines := make([]string, height)
	for i := range lines {
		parts := make([]string, len(boxes))
		for j, b := range boxes {
			parts[j] = strings.Repeat(" ", width(b[0]))
			if i < len(b) {
				parts[j] = b[i]
			}
		}

		lines[i] = strings.TrimRight(strings.Join(parts, " "), " ")
	}

	return lines
}

func width(s string) int {
	return utf8.RuneCountInString(s)
}

func pad(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-width(s)))
}

func truncate(s string, w int, ellipsis string) string {
	if width(s) <= w {
		return s
	}

	runes := []rune(s)

	return string(runes[:w-width(ellipsis)]) + ellipsis
}
