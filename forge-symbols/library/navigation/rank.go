package navigation

import (
	"cmp"
	"fmt"

	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

// rank orders results: by tier, then path, then position, then the order
// of the request's files and of their tags.
type rank struct {
	tier  Tier
	path  string
	at    position
	file  int
	index int
}

func compareRanks(a, b rank) int {
	return cmp.Or(
		cmp.Compare(a.tier, b.tier),
		cmp.Compare(a.path, b.path),
		cmp.Compare(a.at.line, b.at.line),
		cmp.Compare(a.at.column, b.at.column),
		cmp.Compare(a.file, b.file),
		cmp.Compare(a.index, b.index),
	)
}

// tier ranks a result in path whose container is given. An empty container
// matches nothing, so top-level results do not share one.
func (a anchor) tier(path, container string) Tier {
	switch {
	case a.path != "" && path == a.path:
		return TierFile
	case a.container != "" && container == a.container:
		return TierContainer
	}
	return TierOther
}

func start(r symbols.Range) position {
	return position{line: r.StartLine, column: r.StartColumn}
}

// prepare applies the default limits, checks the request against them and
// resolves the target.
func prepare(files []symbols.Analysis, target Target, limits Limits) (anchor, Limits, error) {
	limits = limits.withDefaults()

	if len(files) > limits.Files {
		return anchor{}, limits, fmt.Errorf("%w: request has %d files, limit is %d", symbols.ErrTooLarge, len(files), limits.Files)
	}

	total := 0
	for _, file := range files {
		total += file.Bytes
	}
	if total > limits.TotalBytes {
		return anchor{}, limits, fmt.Errorf("%w: request has %d bytes, limit is %d", symbols.ErrTooLarge, total, limits.TotalBytes)
	}

	resolved, err := resolve(files, target)
	return resolved, limits, err
}

// budget counts the results left under Limits.Results.
type budget struct {
	left      int
	truncated bool
}

// group takes one group head and up to size results for its members, and
// returns how many members fit. It reports false, taking nothing, when the
// head and a first member do not fit.
func (b *budget) group(size int) (int, bool) {
	if b.left < 1+min(size, 1) {
		b.truncated = true
		return 0, false
	}
	b.left--
	return b.take(size), true
}

// take takes up to size results and returns how many fit.
func (b *budget) take(size int) int {
	taken := min(size, b.left)
	b.left -= taken

	if taken < size {
		b.truncated = true
	}
	return taken
}
