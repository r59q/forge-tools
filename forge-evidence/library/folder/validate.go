package folder

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"slices"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
	"github.com/r59q/forge-tools/forge-evidence/library/storage"
)

// Validate checks the .evidence folder without modifying it. Problems are
// ordered by path, then reason, both compared as bytes. A missing folder is
// valid. An incomplete check returns an empty report and an error.
func (s *Store) Validate(ctx context.Context) (storage.Report, error) {
	if err := ctx.Err(); err != nil {
		return storage.Report{}, err
	}

	w := validation{limits: s.limits, remaining: s.limits.Entries}
	if err := w.root(ctx, s.root); err != nil {
		return storage.Report{}, err
	}
	if err := ctx.Err(); err != nil {
		return storage.Report{}, err
	}

	slices.SortFunc(w.report.Problems, func(a, b storage.Problem) int {
		if order := cmp.Compare(a.Path, b.Path); order != 0 {
			return order
		}
		return cmp.Compare(a.Reason, b.Reason)
	})
	return w.report, nil
}

type validation struct {
	limits    evidence.Limits
	remaining int
	report    storage.Report
}

func (w *validation) root(ctx context.Context, parent dir) error {
	info, err := parent.root.Lstat(evidence.Root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return pathError(evidence.Root, err)
	}
	if !w.node(evidence.Root, info.Mode(), true) {
		return nil
	}

	root, err := openDir(parent, evidence.Root)
	if err != nil {
		return err
	}
	defer root.close()

	children, err := listChildren(root, w.limits.Entries)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := w.commit(ctx, root, child); err != nil {
			return err
		}
	}
	return nil
}

func (w *validation) commit(ctx context.Context, parent dir, child fs.DirEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	commit := child.Name()
	path := parent.join(commit)
	if !w.node(path, child.Type(), true) || !w.name(path, evidence.ValidCommit(commit)) {
		return nil
	}
	d, err := openDir(parent, commit)
	if err != nil {
		return err
	}
	defer d.close()

	children, err := listChildren(d, w.remaining)
	if err != nil {
		return err
	}
	w.remaining -= len(children)
	for _, child := range children {
		if err := w.entry(ctx, d, commit, child); err != nil {
			return err
		}
	}
	return nil
}

func (w *validation) entry(ctx context.Context, parent dir, commit string, child fs.DirEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := child.Name()
	path := parent.join(name)
	if !w.node(path, child.Type(), true) || !w.name(path, evidence.ValidName(name)) {
		return nil
	}
	d, err := openDir(parent, name)
	if err != nil {
		return err
	}
	defer d.close()

	return w.checkEntry(ctx, d, evidence.ID{Commit: commit, Name: name})
}

func (w *validation) node(path string, mode fs.FileMode, directory bool) bool {
	switch {
	case mode&fs.ModeSymlink != 0:
		w.add(path, storage.Symlink, "symbolic link")
	case !mode.IsDir() && !mode.IsRegular():
		w.add(path, storage.SpecialFile, "special file")
	case directory && !mode.IsDir():
		w.add(path, storage.StrayPath, "expected a directory")
	case !directory && !mode.IsRegular():
		w.add(path, storage.StrayPath, "expected a regular file")
	default:
		return true
	}
	return false
}

func (w *validation) name(path string, err error) bool {
	if err == nil {
		return true
	}
	w.add(path, storage.UnsafeName, err.Error())
	return false
}

func (w *validation) add(path string, reason storage.Reason, message string) {
	if len(w.report.Problems) == w.limits.Problems {
		w.report.Truncated = true
		return
	}
	w.report.Problems = append(w.report.Problems, storage.Problem{Path: path, Reason: reason, Message: message})
}
