package checkout

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// gitDirectory is skipped wherever it appears: a directory in a worktree, a
// file in a submodule or linked worktree.
const gitDirectory = ".git"

// file is a regular file found by a listing, before it is read.
type file struct {
	path    string
	size    int64
	modTime time.Time
}

// listing holds the files of a tree in path order, compared as bytes, which
// is the match order of their documents.
type listing struct {
	files []file
	state State
}

// bytes is the content size of all listed files.
func (l listing) bytes() int64 {
	var total int64
	for _, file := range l.files {
		total += file.size
	}
	return total
}

// walker lists a tree within its limits. It reads no file content.
type walker struct {
	root    *os.Root
	limits  Limits
	visited int
	result  listing
}

// list finds the files of the tree below root. Too many entries or too deep
// a directory return search.ErrTooLarge.
func list(ctx context.Context, root *os.Root, limits Limits) (listing, error) {
	w := walker{root: root, limits: limits, result: listing{files: []file{}}}

	if err := w.directory(ctx, ".", 0); err != nil {
		return listing{}, err
	}
	slices.SortFunc(w.result.files, func(a, b file) int { return strings.Compare(a.path, b.path) })
	return w.result, nil
}

func (w *walker) directory(ctx context.Context, name string, depth int) error {
	if depth > w.limits.Depth {
		return fmt.Errorf("directory %q: %w", name, search.ErrTooLarge)
	}
	entries, err := w.entries(ctx, name)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if err := w.entry(ctx, name, entry, depth); err != nil {
			return err
		}
	}
	return nil
}

// entries reads the entries of a directory, counting them against
// Limits.Entries.
func (w *walker) entries(ctx context.Context, name string) ([]fs.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	directory, err := open(w.root, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()

	remaining := w.limits.Entries - w.visited
	entries, err := directory.ReadDir(remaining + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("directory %q: %w", name, ErrUnreadable)
	}
	if len(entries) > remaining {
		return nil, fmt.Errorf("directory %q: entries: %w", name, search.ErrTooLarge)
	}

	w.visited += len(entries)
	return entries, nil
}

func (w *walker) entry(ctx context.Context, directory string, entry fs.DirEntry, depth int) error {
	if entry.Name() == gitDirectory {
		return nil
	}

	name := path.Join(directory, entry.Name())
	switch {
	case !validName(name):
		w.result.state.Unsupported++
	case entry.IsDir():
		return w.directory(ctx, name, depth+1)
	case entry.Type().IsRegular():
		return w.regular(name, entry)
	default:
		w.result.state.Unsupported++
	}
	return nil
}

func (w *walker) regular(name string, entry fs.DirEntry) error {
	info, err := entry.Info()
	if err != nil {
		return pathError(name, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("file %q: %w", name, ErrChanged)
	}

	if info.Size() > int64(w.limits.FileBytes) {
		w.result.state.TooLarge++
		return nil
	}
	w.result.files = append(w.result.files, file{path: name, size: info.Size(), modTime: info.ModTime()})
	return nil
}

// validName reports whether a path or source can be part of a document
// identity.
func validName(name string) bool {
	return len(name) <= search.MaxNameBytes && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}

// open opens a root-relative name without blocking, so that a file replaced
// by a FIFO cannot hang the read. The root confines symlink resolution.
func open(root *os.Root, name string) (*os.File, error) {
	handle, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, pathError(name, err)
	}
	return handle, nil
}

// pathError maps a file system error to a category. The original error is
// dropped: it may hold a host path.
func pathError(name string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%q: %w", name, ErrChanged)
	}
	return fmt.Errorf("%q: %w", name, ErrUnreadable)
}
