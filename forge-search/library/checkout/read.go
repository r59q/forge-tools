package checkout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// Tree is the content of a directory as code documents.
type Tree struct {
	// Documents hold one document per file read, in match order. ID is the
	// path relative to the root, with "/" separators; Version is the hex
	// SHA-256 of the content.
	Documents []search.Document `json:"documents"`

	// State counts the files left out.
	State State `json:"state"`
}

// Results are the matches of a directory search and the files it left out.
type Results struct {
	search.Results

	// Files counts the files that were not searched.
	Files State `json:"files"`
}

// Read reads the directory tree below root into code documents and keeps
// nothing after it returns. Zero limits use DefaultLimits.
//
// Files are read in match order until the next one would pass
// Limits.TotalBytes; the rest are left out and State.Truncated is set. Too
// many entries or too deep a directory return search.ErrTooLarge. A file
// that changes while it is read returns ErrChanged. Binary files are
// returned as they are; searches skip them.
func Read(ctx context.Context, root *os.Root, options Options) (Tree, error) {
	limits := options.Limits.withDefaults(DefaultLimits())
	if !validName(options.Source) {
		return Tree{}, fmt.Errorf("source: %w", search.ErrInvalid)
	}
	if !validName(options.Prefix) {
		return Tree{}, fmt.Errorf("prefix: %w", search.ErrInvalid)
	}

	listed, err := list(ctx, root, limits, options.Prefix)
	if err != nil {
		return Tree{}, err
	}

	tree := Tree{Documents: []search.Document{}, State: listed.state}
	var total int64

	for _, file := range listed.files {
		if total+file.size > limits.TotalBytes {
			tree.State.Truncated = true
			break
		}
		content, err := readFile(ctx, root, file)
		if err != nil {
			return Tree{}, err
		}
		total += file.size
		tree.Documents = append(tree.Documents, document(options.Source, file, content))
	}
	return tree, nil
}

// Search reads the directory tree below root, as Read does, and searches it
// with search.Unindexed. It stores nothing. Matches name files by their
// path relative to the root.
//
// Files left out by Limits.TotalBytes set State.Truncated. A context that
// ends returns the matches found so far with State.Canceled, as
// search.Unindexed does.
func Search(ctx context.Context, root *os.Root, query search.Query, options Options) (Results, error) {
	limits := options.Limits.withDefaults(DefaultLimits())

	// An empty search checks the query before the tree is read.
	if _, err := search.Unindexed(ctx, query, nil, search.Limits{}); err != nil {
		return Results{}, err
	}

	tree, err := Read(ctx, root, options)
	if ctx.Err() != nil {
		return canceled(), nil
	}
	if err != nil {
		return Results{}, err
	}

	searchLimits := search.Limits{
		DocumentBytes: limits.FileBytes,
		Documents:     max(len(tree.Documents), 1),
		TotalBytes:    math.MaxInt,
	}
	results, err := search.Unindexed(ctx, query, tree.Documents, searchLimits)
	if err != nil {
		return Results{}, err
	}

	results.State.Truncated = results.State.Truncated || tree.State.Truncated
	return Results{Results: results, Files: tree.State}, nil
}

func canceled() Results {
	return Results{Results: search.Results{Matches: []search.Match{}, State: search.State{Canceled: true}}}
}

// readFile reads a listed file. A file that is no longer the one listed, or
// that changes while it is read, returns ErrChanged.
func readFile(ctx context.Context, root *os.Root, listed file) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	handle, err := open(root, listed.path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = handle.Close() }()

	before, err := handle.Stat()
	if err != nil {
		return nil, fmt.Errorf("file %q: %w", listed.path, ErrUnreadable)
	}
	if !before.Mode().IsRegular() || !same(listed, before) {
		return nil, fmt.Errorf("file %q: %w", listed.path, ErrChanged)
	}

	content, err := io.ReadAll(io.LimitReader(handle, listed.size+1))
	if err != nil {
		return nil, fmt.Errorf("file %q: %w", listed.path, ErrUnreadable)
	}

	after, err := handle.Stat()
	if err != nil || int64(len(content)) != listed.size || !same(listed, after) {
		return nil, fmt.Errorf("file %q: %w", listed.path, ErrChanged)
	}
	return content, nil
}

// same reports whether a file still has the size and time it was listed with.
func same(listed file, info os.FileInfo) bool {
	return info.Size() == listed.size && info.ModTime().Equal(listed.modTime)
}

func contentVersion(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
