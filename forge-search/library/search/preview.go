package search

import (
	"bytes"
	"unicode/utf16"
	"unicode/utf8"
)

// previewLead is the most UTF-16 units shown before a match that does not fit
// in a preview from the start of its line.
const previewLead = 40

// previewCut marks text cut from a preview.
const previewCut = "…"

// linePreview returns the preview of a match of length bytes at byte offset
// at of line, and the UTF-16 column where the preview text starts.
func linePreview(line []byte, at, length int) (string, int) {
	line = bytes.TrimSuffix(line, []byte("\r"))

	start := 0
	if utf16Length(line[:at+length]) > PreviewUnits {
		lead := max(0, min(previewLead, PreviewUnits-utf16Length(line[at:at+length])))
		start = backUnits(line, at, lead)
	}
	end := forwardUnits(line, start, PreviewUnits)

	preview := string(line[start:end])
	if start > 0 {
		preview = previewCut + preview
	}
	if end < len(line) {
		preview += previewCut
	}
	return preview, utf16Length(line[:start]) + 1
}

// backUnits returns the earliest rune boundary before offset that keeps at
// most units UTF-16 units between it and offset.
func backUnits(text []byte, offset, units int) int {
	for offset > 0 {
		char, size := utf8.DecodeLastRune(text[:offset])

		units -= utf16.RuneLen(char)
		if units < 0 {
			break
		}
		offset -= size
	}
	return offset
}

// forwardUnits returns the latest rune boundary after offset that keeps at
// most units UTF-16 units between offset and it.
func forwardUnits(text []byte, offset, units int) int {
	for offset < len(text) {
		char, size := utf8.DecodeRune(text[offset:])

		units -= utf16.RuneLen(char)
		if units < 0 {
			break
		}
		offset += size
	}
	return offset
}

// utf16Length counts the UTF-16 units of valid UTF-8 text.
func utf16Length(text []byte) int {
	units := 0
	for len(text) > 0 {
		char, size := utf8.DecodeRune(text)
		units += utf16.RuneLen(char)
		text = text[size:]
	}
	return units
}
