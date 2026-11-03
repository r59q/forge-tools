package code

import (
	"fmt"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// editor applies file changes to a tree read from the forge. It loads
// only the directories along changed paths, checks each listing against
// its tree ID, and collects the new objects.
type editor struct {
	o    *op
	b    backend
	base OID // the commit the trees are read from
	root *editTree
	objs map[gitobj.ID]gitobj.Object
}

// editTree is one directory being edited.
type editTree struct {
	path    string
	orig    gitobj.ID // the tree ID it was read from; zero if new
	entries map[string]gitobj.TreeEntry
	kids    map[string]*editTree // subdirectories being edited, by name
}

// load lists the directory path of the base commit and checks that the
// listing hashes to want.
func (e *editor) load(path string, want gitobj.ID) (*editTree, error) {
	t, err := e.b.tree(e.o, e.base, path, false, defaultMaxEntries)
	if err != nil {
		return nil, err
	}
	if t.Truncated {
		return nil, fmt.Errorf("%w: directory %q has too many entries to edit", ErrTooLarge, path)
	}

	et := &editTree{
		path:    path,
		orig:    want,
		entries: map[string]gitobj.TreeEntry{},
		kids:    map[string]*editTree{},
	}
	list := make([]gitobj.TreeEntry, 0, len(t.Entries))
	for _, en := range t.Entries {
		id, err := sha1ID(en.ID)
		if err != nil {
			return nil, err
		}

		te := gitobj.TreeEntry{Name: en.Name, Mode: gitobj.Mode(en.Mode), ID: id}
		list = append(list, te)
		et.entries[te.Name] = te
	}

	data, err := gitobj.EncodeTree(list)
	if err != nil || gitobj.Hash(gitobj.TypeTree, data) != want {
		return nil, fmt.Errorf("%w: cannot rebuild directory %q exactly from the forge's listing", ErrUnsupported, path)
	}

	return et, nil
}

// apply applies changes: deletes first, then writes, each in path order.
func (e *editor) apply(changes []FileChange) error {
	sorted := slices.Clone(changes)
	slices.SortFunc(sorted, func(a, b FileChange) int {
		if a.Delete != b.Delete {
			if a.Delete {
				return -1
			}

			return 1
		}

		return strings.Compare(a.Path, b.Path)
	})
	for _, ch := range sorted {
		if err := e.change(ch); err != nil {
			return err
		}
	}

	return nil
}

// change applies one file change.
func (e *editor) change(ch FileChange) error {
	dir, name, err := e.parent(ch)
	if err != nil {
		return err
	}

	en, exists := dir.entries[name]
	if kid, ok := dir.kids[name]; ok && !ch.Delete && emptied(kid) {
		// Earlier deletes emptied the directory, so a file can take its place.
		delete(dir.kids, name)
		delete(dir.entries, name)
		exists = false
	}

	if _, isDir := dir.kids[name]; isDir || (exists && en.Mode == gitobj.ModeTree) {
		if ch.Delete {
			return fmt.Errorf("%w: cannot delete directory %q", ErrNotFile, ch.Path)
		}

		return fmt.Errorf("%w: change to %q: path is a directory", ErrInvalid, ch.Path)
	}

	if ch.Delete {
		if !exists {
			return pathMissing(ch.Path)
		}

		delete(dir.entries, name)

		return nil
	}

	mode := gitobj.Mode(ch.Mode)
	if mode == 0 {
		mode = gitobj.ModeFile
		if exists && en.Mode != gitobj.ModeSubmodule {
			mode = en.Mode
		}
	}

	id, err := e.object(ch, mode)
	if err != nil {
		return err
	}

	dir.entries[name] = gitobj.TreeEntry{Name: name, Mode: mode, ID: id}

	return nil
}

// parent returns the directory that holds ch.Path, loading or creating
// the directories on the way, and the path's last name.
func (e *editor) parent(ch FileChange) (*editTree, string, error) {
	segs := strings.Split(ch.Path, "/")
	dir := e.root
	for i, name := range segs[:len(segs)-1] {
		if kid, ok := dir.kids[name]; ok {
			dir = kid
			continue
		}

		kid, err := e.subdir(dir, ch, name, strings.Join(segs[:i+1], "/"))
		if err != nil {
			return nil, "", err
		}

		dir.kids[name] = kid
		dir = kid
	}

	return dir, segs[len(segs)-1], nil
}

// subdir loads or creates the subdirectory name of dir, at path sub, on
// the way to ch.Path.
func (e *editor) subdir(dir *editTree, ch FileChange, name, sub string) (*editTree, error) {
	en, ok := dir.entries[name]
	switch {
	case !ok && ch.Delete:
		return nil, pathMissing(ch.Path)
	case !ok:
		return &editTree{path: sub, entries: map[string]gitobj.TreeEntry{}, kids: map[string]*editTree{}}, nil
	case en.Mode == gitobj.ModeTree:
		return e.load(sub, en.ID)
	case ch.Delete:
		return nil, pathMissing(ch.Path)
	}

	return nil, fmt.Errorf("%w: change to %q: %q is not a directory", ErrInvalid, ch.Path, sub)
}

// object returns the ID of ch's new content with mode: the submodule
// commit, or a blob it adds to e.objs.
func (e *editor) object(ch FileChange, mode gitobj.Mode) (gitobj.ID, error) {
	if mode == gitobj.ModeSubmodule {
		return sha1ID(ch.Submodule)
	}

	blob := gitobj.Object{Type: gitobj.TypeBlob, Data: ch.Content}
	if blob.Data == nil {
		blob.Data = []byte{}
	}

	id := blob.ID()
	e.objs[id] = blob

	return id, nil
}

// emptied reports whether t holds no files once its edits apply: every
// entry is an edited subdirectory that is itself emptied.
func emptied(t *editTree) bool {
	for name := range t.entries {
		if k, ok := t.kids[name]; !ok || !emptied(k) {
			return false
		}
	}

	for _, k := range t.kids {
		if !emptied(k) {
			return false
		}
	}

	return true
}

// write encodes t and its edited subdirectories bottom up, adds the new
// trees to e.objs and returns t's ID. Emptied subdirectories are removed.
func (e *editor) write(t *editTree) (gitobj.ID, error) {
	for name, kid := range t.kids {
		id, err := e.write(kid)
		if err != nil {
			return gitobj.ID{}, err
		}
		if len(kid.entries) == 0 {
			delete(t.entries, name)
		} else {
			t.entries[name] = gitobj.TreeEntry{Name: name, Mode: gitobj.ModeTree, ID: id}
		}
	}

	list := make([]gitobj.TreeEntry, 0, len(t.entries))
	for _, en := range t.entries {
		list = append(list, en)
	}

	data, err := gitobj.EncodeTree(list)
	if err != nil {
		return gitobj.ID{}, fmt.Errorf("%w: directory %q: %w", ErrInvalid, t.path, err)
	}

	tree := gitobj.Object{Type: gitobj.TypeTree, Data: data}
	id := tree.ID()
	if id != t.orig {
		e.objs[id] = tree
	}

	return id, nil
}
