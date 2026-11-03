package explorer

import (
	"context"
	"errors"
	"path"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Search performs a bounded literal search or filename lookup. Cancellation returns
// accumulated matches with State.Canceled, rather than silently discarding them.
// Ignored and untracked files are included; .git and discovered symlinks are skipped.
func (w *Worktree) Search(ctx context.Context, query Query) (Results, error) {
	if err := validateQuery(query, w.source.Limits); err != nil {
		return Results{}, err
	}
	if query.Limit == 0 {
		query.Limit = w.source.Limits.Results
	}

	s := search{worktree: w, query: query, result: Results{Matches: []Match{}}}
	s.walk(ctx, ".", 0)
	return s.result, nil
}

func validateQuery(q Query, limits Limits) error {
	if q.Text == "" || len(q.Text) > 256 || !utf8.ValidString(q.Text) || strings.ContainsAny(q.Text, "\x00\n\r") ||
		len(q.PathGlob) > 256 || q.Limit < 0 || q.Limit > limits.Results {
		return ErrInvalid
	}
	if _, err := path.Match(q.PathGlob, ""); err != nil {
		return ErrInvalid
	}
	return nil
}

type search struct {
	worktree *Worktree
	query    Query
	result   Results
	visited  int
	bytes    int
}

func (s *search) stopped(ctx context.Context) bool {
	if ctx.Err() != nil {
		s.result.State.Canceled = true
	}
	if s.visited >= s.worktree.source.Limits.Entries || s.bytes >= s.worktree.source.Limits.SearchBytes ||
		len(s.result.Matches) >= s.query.Limit {
		s.result.State.Truncated = true
	}
	return s.result.State.Canceled || s.result.State.Truncated
}

func (s *search) walk(ctx context.Context, directory string, depth int) {
	if s.stopped(ctx) {
		return
	}
	if depth >= s.worktree.source.Limits.Depth {
		s.result.State.Skipped++
		return
	}

	remaining := s.worktree.source.Limits.Entries - s.visited
	entries, truncated, err := s.worktree.entries(ctx, directory, remaining)
	if err != nil {
		s.result.State.Skipped++
		s.stopped(ctx)
		return
	}
	for _, entry := range entries {
		if s.stopped(ctx) {
			return
		}
		s.visited++
		name := path.Join(directory, entry.Name())
		s.visit(ctx, name, entry.IsDir(), entry.Type().IsRegular(), depth)
	}
	s.result.State.Truncated = s.result.State.Truncated || truncated
}

func (s *search) visit(ctx context.Context, name string, directory, regular bool, depth int) {
	if !validPath(name) {
		s.result.State.Skipped++
		return
	}
	if directory {
		s.walk(ctx, name, depth+1)
		return
	}
	if !regular {
		s.result.State.Skipped++
		return
	}
	if !s.matchesPath(name) {
		return
	}

	file, err := s.worktree.Read(ctx, name, "")
	if err != nil {
		s.result.State.Skipped++
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			s.result.State.Canceled = true
		}
		return
	}
	s.bytes += len(file.Text)
	if s.bytes > s.worktree.source.Limits.SearchBytes {
		s.result.State.Truncated = true
		return
	}
	if s.query.Files {
		s.result.Matches = append(s.result.Matches, Match{Location: file.Location, Preview: name})
		return
	}
	s.matchLines(ctx, file)
}

func (s *search) matchesPath(name string) bool {
	if s.query.Files && !strings.Contains(name, s.query.Text) {
		return false
	}
	if s.query.PathGlob == "" {
		return true
	}
	ok, _ := path.Match(s.query.PathGlob, name)
	return ok
}

func (s *search) matchLines(ctx context.Context, file File) {
	lineNumber := 0
	for line := range strings.SplitSeq(file.Text, "\n") {
		lineNumber++
		if s.stopped(ctx) {
			return
		}
		index := strings.Index(line, s.query.Text)
		if index < 0 {
			continue
		}
		column := utf16Length(line[:index]) + 1
		location := file.Location
		location.Range = Range{StartLine: lineNumber, EndLine: lineNumber,
			StartColumn: column, EndColumn: column + utf16Length(s.query.Text)}
		s.result.Matches = append(s.result.Matches, Match{Location: location, Preview: preview(line)})
	}
}

func utf16Length(text string) int {
	n := 0
	for _, r := range text {
		n += utf16.RuneLen(r)
	}
	return n
}

func preview(line string) string {
	units := 0
	for index, r := range line {
		units += utf16.RuneLen(r)
		if units > 240 {
			return line[:index] + "…"
		}
	}
	return line
}
