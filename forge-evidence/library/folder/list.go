package folder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
	"github.com/r59q/forge-tools/forge-evidence/library/storage"
)

// List returns matching metadata in bytewise order by commit SHA, then by
// entry name. A missing .evidence folder gives an empty list. Every commit
// folder and entry is checked before filtering. A bad path or unreadable
// entry fails the whole list. Add's staging folders are ignored.
// Limits.Entries caps the children of .evidence and, separately, the total
// children of the commit folders. Limits.MetadataBytes caps each entry's
// metadata. Exceeding either limit returns evidence.ErrTooLarge.
func (s *Store) List(ctx context.Context, filter storage.Filter) ([]evidence.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	evidenceDir, err := openDir(s.root, evidence.Root)
	if errors.Is(err, storage.ErrNotExist) {
		return []evidence.Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer evidenceDir.close()

	commits, err := listChildren(evidenceDir, s.limits.Entries)
	if err != nil {
		return nil, err
	}

	walk := listing{
		limits:    s.limits,
		filter:    filter,
		remaining: s.limits.Entries,
		entries:   []evidence.Entry{},
	}
	for _, child := range commits {
		if err := walk.commit(ctx, evidenceDir, child.Name()); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return walk.entries, nil
}

type listing struct {
	limits    evidence.Limits
	filter    storage.Filter
	remaining int
	entries   []evidence.Entry
}

func (l *listing) commit(ctx context.Context, parent dir, commit string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := evidence.ValidCommit(commit); err != nil {
		return fmt.Errorf("%q: %w", parent.join(commit), err)
	}

	commitDir, err := openDir(parent, commit)
	if err != nil {
		return err
	}
	defer commitDir.close()

	children, err := listChildren(commitDir, l.remaining)
	if err != nil {
		return err
	}
	l.remaining -= len(children)

	for _, child := range children {
		if err := l.entry(ctx, commitDir, commit, child); err != nil {
			return err
		}
	}
	return nil
}

func (l *listing) entry(ctx context.Context, parent dir, commit string, child fs.DirEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := child.Name()
	if child.IsDir() && isStagingName(name) {
		return nil
	}
	if err := evidence.ValidName(name); err != nil {
		return fmt.Errorf("%q: %w", parent.join(name), err)
	}

	entryDir, err := openDir(parent, name)
	if err != nil {
		return err
	}
	defer entryDir.close()

	entry, err := loadEntry(entryDir, evidence.ID{Commit: commit, Name: name}, l.limits)
	if err != nil {
		return err
	}
	if matchesFilter(l.filter, entry) {
		l.entries = append(l.entries, entry)
	}
	return nil
}

func matchesFilter(filter storage.Filter, entry evidence.Entry) bool {
	return (filter.Commit == "" || filter.Commit == entry.Subject.Commit) &&
		(filter.Path == "" || filter.Path == entry.Subject.Path) &&
		(filter.Kind == "" || filter.Kind == entry.Kind)
}

func listChildren(d dir, limit int) ([]fs.DirEntry, error) {
	handle, err := d.root.Open(".")
	if err != nil {
		return nil, pathError(d.path, err)
	}
	defer func() { _ = handle.Close() }()

	// Read one extra name to detect overflow, with a positive count even
	// when no budget remains.
	children, err := handle.ReadDir(min(limit, math.MaxInt-1) + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, pathError(d.path, err)
	}
	if len(children) > limit {
		return nil, fmt.Errorf("%q: %w", d.path, evidence.ErrTooLarge)
	}

	slices.SortFunc(children, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return children, nil
}

// isStagingName recognizes stagingPrefix followed by a crypto/rand.Text
// suffix, which has at least 128 bits of randomness in base 32.
func isStagingName(name string) bool {
	suffix, ok := strings.CutPrefix(name, stagingPrefix)
	return ok && len(suffix) >= 26 && !strings.ContainsFunc(suffix, func(char rune) bool {
		letter := char >= 'A' && char <= 'Z'
		digit := char >= '2' && char <= '7'
		return !letter && !digit
	})
}
