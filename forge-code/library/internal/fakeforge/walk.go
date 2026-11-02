package fakeforge

import (
	"container/heap"
	"fmt"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// rootTree returns the tree of a commit, tag or tree ID. The caller holds
// mu.
func (r *Repo) rootTree(id gitobj.ID) (gitobj.ID, error) {
	p, t, err := r.peel(id)
	if err != nil {
		return gitobj.ZeroID, err
	}
	switch t {
	case gitobj.TypeTree:
		return p, nil
	case gitobj.TypeCommit:
		c, err := r.commit(p)
		return c.Tree, err
	}
	return gitobj.ZeroID, fmt.Errorf("%w: %s is not a tree-ish", ErrWrongType, id)
}

// Lookup walks path from root, which may be a commit, tag or tree, and
// returns the entry at path. An empty path gives the root tree as an entry
// with mode gitobj.ModeTree and no name. A missing path, or one that goes
// through a non-tree, gives ErrNotFound.
func (r *Repo) Lookup(root gitobj.ID, path string) (gitobj.TreeEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tree, err := r.rootTree(root)
	if err != nil {
		return gitobj.TreeEntry{}, err
	}
	return r.lookup(tree, path)
}

func (r *Repo) lookup(tree gitobj.ID, path string) (gitobj.TreeEntry, error) {
	e := gitobj.TreeEntry{Mode: gitobj.ModeTree, ID: tree}
	if path == "" {
		return e, nil
	}
	for _, name := range strings.Split(path, "/") {
		if e.Mode != gitobj.ModeTree || name == "" {
			return gitobj.TreeEntry{}, fmt.Errorf("%w: path %q", ErrNotFound, path)
		}
		entries, err := r.tree(e.ID)
		if err != nil {
			return gitobj.TreeEntry{}, err
		}
		i := slices.IndexFunc(entries, func(x gitobj.TreeEntry) bool { return x.Name == name })
		if i < 0 {
			return gitobj.TreeEntry{}, fmt.Errorf("%w: path %q", ErrNotFound, path)
		}
		e = entries[i]
	}
	return e, nil
}

// PathEntry is a tree entry with its full path.
type PathEntry struct {
	gitobj.TreeEntry
	// Path is the slash-separated path from the listed tree.
	Path string
	// Size is the content length of a blob, and 0 for trees and
	// submodules.
	Size int64
}

// ListTree lists the tree of root, which may be a commit, tag or tree.
// Recursive listings are in pre-order with tree entries included, as
// `git ls-tree -r -t` prints them.
func (r *Repo) ListTree(root gitobj.ID, recursive bool) ([]PathEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tree, err := r.rootTree(root)
	if err != nil {
		return nil, err
	}
	var out []PathEntry
	return out, r.list(tree, "", recursive, &out)
}

func (r *Repo) list(tree gitobj.ID, dir string, recursive bool, out *[]PathEntry) error {
	entries, err := r.tree(tree)
	if err != nil {
		return err
	}
	for _, e := range entries {
		pe := PathEntry{TreeEntry: e, Path: dir + e.Name}
		if e.Mode != gitobj.ModeTree && e.Mode != gitobj.ModeSubmodule {
			if o, ok := r.objects[e.ID]; ok {
				pe.Size = int64(len(o.Data))
			}
		}
		*out = append(*out, pe)
		if recursive && e.Mode == gitobj.ModeTree {
			if err := r.list(e.ID, pe.Path+"/", true, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// LogOptions filters Log.
type LogOptions struct {
	// Path limits the log to commits that change it, with git's default
	// history simplification. Empty means no filter.
	Path string
	// Not hides commits reachable from these commit-ishes, as ^X does.
	Not []gitobj.ID
}

// Log returns the commits reachable from tip, a commit-ish, in the order
// `git log` prints them: newest committer date first, ties in the order
// they were reached.
//
// With a path, a merge that is TREESAME to a parent follows only the first
// such parent and is not shown; other commits are shown only if they
// change the path. A root commit counts as changing the path if it has it.
func (r *Repo) Log(tip gitobj.ID, o LogOptions) ([]gitobj.ID, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	start, err := r.peelCommit(tip)
	if err != nil {
		return nil, err
	}
	hidden := map[gitobj.ID]bool{}
	for _, n := range o.Not {
		c, err := r.peelCommit(n)
		if err != nil {
			return nil, err
		}
		if err := r.ancestors(c, hidden); err != nil {
			return nil, err
		}
	}
	var out []gitobj.ID
	q := &commitQueue{}
	seen := map[gitobj.ID]bool{}
	push := func(id gitobj.ID) error {
		if seen[id] || hidden[id] {
			return nil
		}
		seen[id] = true
		c, err := r.commit(id)
		if err != nil {
			return err
		}
		q.seq++
		heap.Push(q, queued{id: id, c: c, seq: q.seq})
		return nil
	}
	if err := push(start); err != nil {
		return nil, err
	}
	for q.Len() > 0 {
		cur := heap.Pop(q).(queued)
		parents, show, err := r.simplify(cur.c, o.Path, hidden)
		if err != nil {
			return nil, err
		}
		if show {
			out = append(out, cur.id)
		}
		for _, p := range parents {
			if err := push(p); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// simplify applies git's default history simplification for path to c.
// It returns the parents to follow and whether c is shown.
func (r *Repo) simplify(c gitobj.Commit, path string, hidden map[gitobj.ID]bool) ([]gitobj.ID, bool, error) {
	if path == "" {
		return c.Parents, true, nil
	}
	mine, err := r.pathEntry(c.Tree, path)
	if err != nil {
		return nil, false, err
	}
	if len(c.Parents) == 0 {
		return nil, mine != nil, nil
	}
	// Hidden parents never simplify, and count only when all are hidden.
	var changedVisible, changedHidden bool
	for _, p := range c.Parents {
		pc, err := r.commit(p)
		if err != nil {
			return nil, false, err
		}
		theirs, err := r.pathEntry(pc.Tree, path)
		if err != nil {
			return nil, false, err
		}
		same := (mine == nil && theirs == nil) || (mine != nil && theirs != nil && *mine == *theirs)
		switch {
		case hidden[p] && !same:
			changedHidden = true
		case hidden[p]:
		case same:
			return []gitobj.ID{p}, false, nil
		default:
			changedVisible = true
		}
	}
	// Any visible parent here differs from c.
	return c.Parents, changedVisible || changedHidden, nil
}

// pathEntry returns the entry at path in tree, or nil if there is none.
func (r *Repo) pathEntry(tree gitobj.ID, path string) (*gitobj.TreeEntry, error) {
	e, err := r.lookup(tree, path)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	e.Name = ""
	return &e, nil
}

// ancestors adds id and every commit reachable from it to set.
func (r *Repo) ancestors(id gitobj.ID, set map[gitobj.ID]bool) error {
	stack := []gitobj.ID{id}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if set[id] {
			continue
		}
		c, err := r.commit(id)
		if err != nil {
			return err
		}
		set[id] = true
		stack = append(stack, c.Parents...)
	}
	return nil
}

// IsAncestor reports whether commit-ish a is reachable from commit-ish b.
// A commit is its own ancestor.
func (r *Repo) IsAncestor(a, b gitobj.ID) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ca, err := r.peelCommit(a)
	if err != nil {
		return false, err
	}
	cb, err := r.peelCommit(b)
	if err != nil {
		return false, err
	}
	set := map[gitobj.ID]bool{}
	if err := r.ancestors(cb, set); err != nil {
		return false, err
	}
	return set[ca], nil
}

// MergeBases returns the best common ancestors of commit-ishes a and b,
// as `git merge-base --all` does: common ancestors that are not ancestors
// of another common ancestor. They are sorted newest committer date first,
// then by ID. Unrelated histories give none.
func (r *Repo) MergeBases(a, b gitobj.ID) ([]gitobj.ID, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ca, err := r.peelCommit(a)
	if err != nil {
		return nil, err
	}
	cb, err := r.peelCommit(b)
	if err != nil {
		return nil, err
	}
	sa, sb := map[gitobj.ID]bool{}, map[gitobj.ID]bool{}
	if err := r.ancestors(ca, sa); err != nil {
		return nil, err
	}
	if err := r.ancestors(cb, sb); err != nil {
		return nil, err
	}
	common := map[gitobj.ID]bool{}
	for id := range sa {
		if sb[id] {
			common[id] = true
		}
	}
	redundant := map[gitobj.ID]bool{}
	for id := range common {
		if redundant[id] {
			continue
		}
		c, _ := r.commit(id)
		below := map[gitobj.ID]bool{}
		for _, p := range c.Parents {
			if err := r.ancestors(p, below); err != nil {
				return nil, err
			}
		}
		for x := range below {
			redundant[x] = true
		}
	}
	var out []gitobj.ID
	when := map[gitobj.ID]int64{}
	for id := range common {
		if !redundant[id] {
			c, _ := r.commit(id)
			when[id] = c.Committer.When.Unix()
			out = append(out, id)
		}
	}
	slices.SortFunc(out, func(x, y gitobj.ID) int {
		if when[x] != when[y] {
			if when[x] > when[y] {
				return -1
			}
			return 1
		}
		return strings.Compare(x.String(), y.String())
	})
	return out, nil
}

// MergeBase returns the first of MergeBases, and false if a and b share no
// history.
func (r *Repo) MergeBase(a, b gitobj.ID) (gitobj.ID, bool, error) {
	all, err := r.MergeBases(a, b)
	if err != nil || len(all) == 0 {
		return gitobj.ZeroID, false, err
	}
	return all[0], true, nil
}

// queued is a commit waiting in Log's queue.
type queued struct {
	id  gitobj.ID
	c   gitobj.Commit
	seq int
}

// commitQueue orders commits by committer date, newest first, then by
// insertion, like git's prio_queue.
type commitQueue struct {
	items []queued
	seq   int
}

func (q *commitQueue) Len() int { return len(q.items) }
func (q *commitQueue) Less(i, j int) bool {
	a, b := q.items[i], q.items[j]
	ta, tb := a.c.Committer.When.Unix(), b.c.Committer.When.Unix()
	if ta != tb {
		return ta > tb
	}
	return a.seq < b.seq
}
func (q *commitQueue) Swap(i, j int) { q.items[i], q.items[j] = q.items[j], q.items[i] }
func (q *commitQueue) Push(x any)    { q.items = append(q.items, x.(queued)) }
func (q *commitQueue) Pop() any {
	x := q.items[len(q.items)-1]
	q.items = q.items[:len(q.items)-1]
	return x
}
