package code

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Tree lists a directory at rev. A missing directory, or a path that is
// not a directory, wraps ErrPathNotFound. Listings over MaxEntries or the
// call limit are cut short and report Truncated.
func (c *Client) Tree(ctx context.Context, r RepositoryRef, rev string, opts TreeOptions) (Tree, error) {
	limit := opts.MaxEntries
	switch {
	case limit < 0:
		return Tree{}, fmt.Errorf("%w: negative max entries", ErrInvalid)
	case limit == 0:
		limit = defaultMaxEntries
	}

	path, err := cleanDir(opts.Path)
	if err != nil {
		return Tree{}, err
	}

	pr, err := parseRevision(rev)
	if err != nil {
		return Tree{}, err
	}

	s, o, err := c.start(ctx, r)
	if err != nil {
		return Tree{}, err
	}

	res, _, err := resolve(o, s.forge, pr)
	if err != nil {
		return Tree{}, s.wrap(err)
	}

	t, err := s.forge.tree(o, res.Commit, path, opts.Recursive, limit)
	if err != nil {
		return Tree{}, s.wrap(err)
	}

	t.Commit, t.Path = res.Commit, path
	t.Entries = append([]Entry{}, t.Entries...)
	slices.SortFunc(t.Entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	if len(t.Entries) > limit {
		t.Entries, t.Truncated = t.Entries[:limit], true
	}

	return t, nil
}

// Stat describes the entry at path in rev. It is the one way to see
// symlinks and executable bits on every forge.
func (c *Client) Stat(ctx context.Context, r RepositoryRef, rev, path string) (PathInfo, error) {
	if _, err := cleanFilePath(path); err != nil {
		return PathInfo{}, err
	}

	pr, err := parseRevision(rev)
	if err != nil {
		return PathInfo{}, err
	}

	s, o, err := c.start(ctx, r)
	if err != nil {
		return PathInfo{}, err
	}

	res, _, err := resolve(o, s.forge, pr)
	if err != nil {
		return PathInfo{}, s.wrap(err)
	}

	e, err := s.forge.stat(o, res.Commit, path)
	if err != nil {
		return PathInfo{}, s.wrap(err)
	}

	return PathInfo{Commit: res.Commit, Entry: e}, nil
}

// ReadFile opens the file at path in rev. Symlinks are not followed: a
// symlink's content is its target. A directory or submodule is
// ErrNotFile. The caller must close the Reader.
func (c *Client) ReadFile(ctx context.Context, r RepositoryRef, rev, path string) (*Reader, error) {
	if _, err := cleanFilePath(path); err != nil {
		return nil, err
	}

	pr, err := parseRevision(rev)
	if err != nil {
		return nil, err
	}

	s, o, err := c.start(ctx, r)
	if err != nil {
		return nil, err
	}

	res, _, err := resolve(o, s.forge, pr)
	if err != nil {
		return nil, s.wrap(err)
	}

	rd, err := s.forge.readFile(o, res.Commit, path)
	if err != nil {
		return nil, s.wrap(err)
	}

	rd.Commit, rd.Path = res.Commit, path
	rd, err = finishReader(rd, c.limits.MaxBlobBytes)

	return rd, s.wrap(err)
}

// ReadBlob opens the blob id. A missing blob wraps ErrRevisionNotFound.
// The caller must close the Reader.
func (c *Client) ReadBlob(ctx context.Context, r RepositoryRef, id OID) (*Reader, error) {
	if err := checkOID("blob", id); err != nil {
		return nil, err
	}

	s, o, err := c.start(ctx, r)
	if err != nil {
		return nil, err
	}

	rd, err := s.forge.readBlob(o, id)
	if err != nil {
		return nil, s.wrap(err)
	}

	rd.ID, rd.Commit, rd.Path = id, "", ""
	rd, err = finishReader(rd, c.limits.MaxBlobBytes)

	return rd, s.wrap(err)
}
