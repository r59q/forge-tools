package checkouts

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/r59q/forge-tools/forge-checkouts/library/internal/gitshadow"
)

func shadowParent(r Record) string {
	return filepath.Join(filepath.Dir(filepath.Dir(r.Checkout.Location)), "shadows")
}

func isolatedView(directory, copyPath string) gitView {
	return gitView{
		directory: directory,
		env:       []string{"GIT_DIR=" + directory, "GIT_WORK_TREE=" + copyPath, "GIT_NO_REPLACE_OBJECTS=1"},
		isolated:  true,
	}
}

func createIsolated(ctx context.Context, r Record) error {
	directory, err := os.MkdirTemp(shadowParent(r), "create-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)

	if err = gitshadow.Init(directory, r.Checkout.Revision, filepath.Join(r.GitDirectory, "objects")); err != nil {
		return err
	}
	if err = os.Mkdir(r.Checkout.Location, 0700); err != nil {
		return err
	}

	view := isolatedView(directory, r.Checkout.Location)
	if _, err = view.run(ctx, "reset", "--hard", r.Checkout.Revision, "--"); err != nil {
		return err
	}

	return os.Rename(directory, filepath.Join(r.Checkout.Location, ".git"))
}

func readView(ctx context.Context, r Record) (gitView, func(), error) {
	if r.Checkout.Mode != Isolated {
		return gitView{directory: r.Checkout.Location}, func() {}, nil
	}

	directory, err := os.MkdirTemp(shadowParent(r), "read-*")
	if err != nil {
		return gitView{}, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	if err = gitshadow.Snapshot(ctx, r.Checkout.Location, directory, r.GitDirectory, r.Checkout.Revision); err != nil {
		cleanup()
		return gitView{}, nil, err
	}

	view := isolatedView(directory, r.Checkout.Location)
	if err = view.skipGitlinks(ctx); err != nil {
		cleanup()
		return gitView{}, nil, err
	}
	return view, cleanup, nil
}

// skipGitlinks prevents status's index refresh from reading nested Git config.
// --ignore-submodules alone does not protect the refresh step. Only the shadow
// index is modified; staged gitlink changes can still be reported.
func (v *gitView) skipGitlinks(ctx context.Context) error {
	out, err := v.run(ctx, "ls-files", "--stage", "-z")
	if err != nil {
		return err
	}
	for entry := range strings.SplitSeq(out, "\x00") {
		if !strings.HasPrefix(entry, "160000 ") {
			continue
		}
		metadata, path, ok := strings.Cut(entry, "\t")
		if !ok {
			return ErrConflict
		}
		v.gitlinks = append(v.gitlinks, path)
		// Unmerged entries are already skipped by Git's index refresh.
		if strings.HasSuffix(metadata, " 0") {
			if _, err = v.run(ctx, "update-index", "--skip-worktree", "--", path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v gitView) protectSubmodules(copyPath string) error {
	if len(v.gitlinks) == 0 {
		return nil
	}
	root, err := os.OpenRoot(copyPath)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, path := range v.gitlinks {
		// ReadDir does not enter nested .git or follow entries inside it.
		entries, err := fs.ReadDir(root.FS(), path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || len(entries) != 0 {
			return ErrConflict
		}
	}
	return nil
}
