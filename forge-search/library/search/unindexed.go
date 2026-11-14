package search

import (
	"bytes"
	"context"
	"unicode/utf8"
)

// Unindexed searches the given documents for the query text. It performs no
// I/O and keeps nothing after it returns.
//
// Matches come in match order: by document kind, source and ID, compared as
// bytes, then by line. A line gives at most one match, its leftmost
// occurrence. The first Query.Limit matches in that order are returned.
//
// A bad query or document identity returns ErrInvalid; too many documents or
// a document over Limits.DocumentBytes returns ErrTooLarge. Documents are
// scanned in match order until Limits.TotalBytes is reached; the rest are not
// scanned and State.Truncated is set. A context that ends returns the matches
// found so far with State.Canceled.
func Unindexed(ctx context.Context, query Query, documents []Document, limits Limits) (Results, error) {
	limits = limits.withDefaults()

	query, err := validateQuery(query, limits)
	if err != nil {
		return Results{}, err
	}
	sorted, err := ordered(documents, limits)
	if err != nil {
		return Results{}, err
	}

	s := scan{
		query:  []byte(query.Text),
		limit:  query.Limit,
		budget: limits.TotalBytes,
		result: Results{Matches: []Match{}},
	}
	for _, document := range sorted {
		if s.stopped(ctx) {
			break
		}
		s.document(ctx, document)
	}
	return s.result, nil
}

// scan holds the progress of one search.
type scan struct {
	query  []byte
	limit  int
	budget int
	done   bool
	result Results
}

// stopped reports whether the search must end, and records why.
func (s *scan) stopped(ctx context.Context) bool {
	if ctx.Err() != nil {
		s.result.State.Canceled = true
		s.done = true
	}
	return s.done
}

func (s *scan) document(ctx context.Context, document *Document) {
	content := document.Content

	if len(content) > s.budget {
		s.result.State.Truncated = true
		s.done = true
		return
	}
	s.budget -= len(content)

	if !searchable(content) {
		s.result.State.Skipped++
		return
	}
	s.lines(ctx, document.Ref, content)
}

// lines adds the leftmost match of each line, in order.
func (s *scan) lines(ctx context.Context, ref Ref, content []byte) {
	line, start := 1, 0

	for !s.stopped(ctx) {
		found := bytes.Index(content[start:], s.query)
		if found < 0 {
			return
		}
		at := start + found

		before := content[start:at]
		if newlines := bytes.Count(before, []byte("\n")); newlines > 0 {
			line += newlines
			start += bytes.LastIndexByte(before, '\n') + 1
		}

		end := lineEnd(content, at)
		s.add(ref, line, content[start:end], at-start)

		if end == len(content) {
			return
		}
		line, start = line+1, end+1
	}
}

// add records a match at byte offset at of the line, or ends the search when
// the result limit is full.
func (s *scan) add(ref Ref, line int, text []byte, at int) {
	if len(s.result.Matches) >= s.limit {
		s.result.State.Truncated = true
		s.done = true
		return
	}

	column := utf16Length(text[:at]) + 1
	preview, previewColumn := linePreview(text, at, len(s.query))

	s.result.Matches = append(s.result.Matches, Match{
		Document: ref,
		Range: Range{
			StartLine:   line,
			StartColumn: column,
			EndLine:     line,
			EndColumn:   column + utf16Length(text[at:at+len(s.query)]),
		},
		Preview:       preview,
		PreviewColumn: previewColumn,
	})
}

// searchable reports whether content is text: valid UTF-8 without NUL bytes.
func searchable(content []byte) bool {
	return utf8.Valid(content) && bytes.IndexByte(content, 0) < 0
}

func lineEnd(content []byte, offset int) int {
	if next := bytes.IndexByte(content[offset:], '\n'); next >= 0 {
		return offset + next
	}
	return len(content)
}
