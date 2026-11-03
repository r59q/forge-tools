package code

import (
	"errors"
	"slices"
	"strings"
)

// treeDiff is the state of one Compare: a walk from the trees of commit
// from to those of commit to.
type treeDiff struct {
	o        *op
	b        backend
	opts     CompareOptions
	from, to OID
	// raw holds additions, deletions, modifications and type changes;
	// result pairs renames.
	raw []Change
	// rawCap stops the walk: that many raw changes pair into more than
	// MaxChanges changes.
	rawCap int
	// listMax caps one recursive listing.
	listMax   int
	truncated bool
}

// run walks both trees from the root.
func (t *treeDiff) run() error {
	err := t.walkRoot()
	switch {
	case errors.Is(err, errStop):
		return nil
	case exhausted(err):
		t.truncated = true
		return nil
	}

	return err
}

// walkRoot compares the root trees, skipping them if their IDs match.
func (t *treeDiff) walkRoot() error {
	if t.b.capabilities().TreeIDs {
		a, err := t.b.commit(t.o, t.from)
		if err != nil {
			return err
		}

		b, err := t.b.commit(t.o, t.to)
		if err != nil {
			return err
		}
		if a.Tree != "" && a.Tree == b.Tree {
			return nil
		}
	}

	return t.walk("", true, true)
}

// list returns the entries of directory dir in commit, by name. A
// listing cut short stops the walk.
func (t *treeDiff) list(commit OID, dir string) (map[string]Entry, error) {
	tr, err := t.b.tree(t.o, commit, dir, false, defaultMaxEntries)
	if err != nil {
		// Git may store empty subtrees, which some forges call missing.
		if dir != "" && errors.Is(err, ErrPathNotFound) {
			return nil, nil
		}

		return nil, err
	}

	if tr.Truncated {
		t.truncated = true
		return nil, errStop
	}

	m := make(map[string]Entry, len(tr.Entries))
	for _, e := range tr.Entries {
		m[e.Name] = e
	}

	return m, nil
}

// walk compares directory dir, which exists in from if inFrom and in to
// if inTo, and descends into subdirectories whose IDs differ.
func (t *treeDiff) walk(dir string, inFrom, inTo bool) error {
	var old, cur map[string]Entry
	var err error
	if inFrom {
		if old, err = t.list(t.from, dir); err != nil {
			return err
		}
	}
	if inTo {
		if cur, err = t.list(t.to, dir); err != nil {
			return err
		}
	}

	for _, n := range unionNames(old, cur) {
		p := n
		if dir != "" {
			p = dir + "/" + n
		}

		if err := t.walkEntry(p, entryIn(old, n), entryIn(cur, n)); err != nil {
			return err
		}
	}

	return nil
}

// unionNames returns the names in a or b, sorted.
func unionNames(a, b map[string]Entry) []string {
	names := make([]string, 0, len(a)+len(b))
	for n := range a {
		names = append(names, n)
	}

	for n := range b {
		if _, ok := a[n]; !ok {
			names = append(names, n)
		}
	}

	slices.Sort(names)

	return names
}

// entryIn returns the entry name in m, or nil.
func entryIn(m map[string]Entry, name string) *Entry {
	e, ok := m[name]
	if !ok {
		return nil
	}

	return &e
}

// walkEntry compares path p, which is a in from and b in to, either nil
// if missing. It descends into directories and records file changes.
func (t *treeDiff) walkEntry(p string, a, b *Entry) error {
	sel := t.selected(p)
	if !sel && !t.ancestor(p) {
		return nil
	}
	if a != nil && b != nil && a.ID == b.ID && a.Mode == b.Mode {
		return nil
	}

	aDir, bDir := a != nil && a.Mode == ModeTree, b != nil && b.Mode == ModeTree
	if aDir && bDir {
		return t.walk(p, true, true)
	}

	if aDir {
		if err := t.oneSide(p, true); err != nil {
			return err
		}

		a = nil
	}

	if bDir {
		if err := t.oneSide(p, false); err != nil {
			return err
		}

		b = nil
	}

	if !sel || (a == nil && b == nil) {
		return nil
	}

	return t.add(entryChange(p, a, b))
}

// entryChange returns the change at path p from a to b, either nil if
// missing.
func entryChange(p string, a, b *Entry) Change {
	switch {
	case a == nil:
		return addedChange(p, *b)
	case b == nil:
		return deletedChange(p, *a)
	}

	c := Change{
		Status:  Modified,
		OldPath: p,
		Path:    p,
		OldMode: a.Mode,
		Mode:    b.Mode,
		OldID:   a.ID,
		ID:      b.ID,
	}
	if kindOf(a.Mode) != kindOf(b.Mode) {
		c.Status = TypeChanged
	}

	return c
}

// addedChange returns the change that adds e at p.
func addedChange(p string, e Entry) Change {
	return Change{Status: Added, Path: p, Mode: e.Mode, ID: e.ID}
}

// deletedChange returns the change that deletes e at p.
func deletedChange(p string, e Entry) Change {
	return Change{Status: Deleted, OldPath: p, OldMode: e.Mode, OldID: e.ID}
}

// oneSide handles directory p that exists only in from (deleted) or only
// in to. A selected directory is listed recursively; one that only leads
// to a selected path is walked.
func (t *treeDiff) oneSide(p string, deleted bool) error {
	switch {
	case t.selected(p):
		return t.subtree(p, deleted)
	case t.ancestor(p):
		return t.walk(p, deleted, !deleted)
	}

	return nil
}

// subtree adds every file below directory p as deleted or added.
func (t *treeDiff) subtree(p string, deleted bool) error {
	commit := t.to
	if deleted {
		commit = t.from
	}

	tr, err := t.b.tree(t.o, commit, p, true, t.listMax)
	if err != nil {
		if errors.Is(err, ErrPathNotFound) {
			return nil // an empty subtree
		}

		return err
	}

	entries := slices.Clone(tr.Entries)
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	for _, e := range entries {
		if e.Mode == ModeTree || !strings.HasPrefix(e.Path, p+"/") {
			continue
		}

		c := addedChange(e.Path, e)
		if deleted {
			c = deletedChange(e.Path, e)
		}
		if err := t.add(c); err != nil {
			return err
		}
	}

	if tr.Truncated {
		t.truncated = true
		return errStop
	}

	return nil
}

// add records a raw change and stops the walk once rawCap is reached.
func (t *treeDiff) add(c Change) error {
	t.raw = append(t.raw, c)
	if len(t.raw) >= t.rawCap {
		t.truncated = true
		return errStop
	}

	return nil
}

// selected reports whether p is at or below one of the Paths filters.
func (t *treeDiff) selected(p string) bool {
	if len(t.opts.Paths) == 0 {
		return true
	}

	for _, f := range t.opts.Paths {
		if p == f || strings.HasPrefix(p, f+"/") {
			return true
		}
	}

	return false
}

// ancestor reports whether p is a directory above one of the Paths
// filters.
func (t *treeDiff) ancestor(p string) bool {
	for _, f := range t.opts.Paths {
		if strings.HasPrefix(f, p+"/") {
			return true
		}
	}

	return false
}

// kindOf groups modes: regular and executable files are one kind, so a
// change between them is Modified.
func kindOf(m FileMode) FileMode {
	if m == ModeExec {
		return ModeFile
	}

	return m
}
