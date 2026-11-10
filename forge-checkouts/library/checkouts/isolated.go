package checkouts

import (
	"context"
	"os"
	"path/filepath"

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

	return isolatedView(directory, r.Checkout.Location), cleanup, nil
}
