package workspace_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/r59q/forge-tools/forge-agents/library/workspace"
)

type fakeCheckouts struct {
	created []workspace.Repository
	removed []string
}

func (f *fakeCheckouts) Create(_ context.Context, r workspace.Repository, ref string) (workspace.Checkout, error) {
	f.created = append(f.created, r)
	return workspace.Checkout{ID: "c1", Path: "/data/copies/c1", Reference: ref + "main"}, nil
}

func (f *fakeCheckouts) Remove(_ context.Context, id string) error {
	f.removed = append(f.removed, id)
	return nil
}

func TestScratch(t *testing.T) {
	l := workspace.Local{ScratchDir: t.TempDir()}
	w, err := l.Prepare(t.Context(), "a1", workspace.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != workspace.KindScratch || w.Path != filepath.Join(l.ScratchDir, "a1") {
		t.Fatalf("workspace = %+v", w)
	}
	if err := l.Release(t.Context(), w, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Errorf("scratch kept: %v", err)
	}
	if err := l.Release(t.Context(), workspace.Workspace{Kind: workspace.KindScratch, Path: "/etc"}, false); err == nil {
		t.Error("released a directory outside the scratch directory")
	}
}

func TestPath(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "repo")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	l := workspace.Local{Roots: []string{root}}
	w, err := l.Prepare(t.Context(), "a1", workspace.Spec{Kind: workspace.KindPath, Path: inside})
	if err != nil || w.Path != inside {
		t.Fatalf("workspace = %+v, %v", w, err)
	}
	for _, path := range []string{link, "repo", filepath.Join(root, "missing"), "/"} {
		if _, err := l.Prepare(t.Context(), "a1", workspace.Spec{Kind: workspace.KindPath, Path: path}); !errors.Is(err, workspace.ErrInvalid) {
			t.Errorf("%s: err = %v", path, err)
		}
	}
	if err := l.Release(t.Context(), w, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Errorf("path removed: %v", err)
	}
	if _, err := (workspace.Local{}).Prepare(t.Context(), "a1", workspace.Spec{Kind: workspace.KindPath, Path: inside}); !errors.Is(err, workspace.ErrUnavailable) {
		t.Errorf("no roots: err = %v", err)
	}
}

func TestCheckout(t *testing.T) {
	c := &fakeCheckouts{}
	l := workspace.Local{Checkouts: c}
	repo := workspace.Repository{Source: "github", Path: "r59q/forge-tools"}
	w, err := l.Prepare(t.Context(), "a1", workspace.Spec{Kind: workspace.KindCheckout, Repository: repo})
	if err != nil {
		t.Fatal(err)
	}
	if w.Checkout != "c1" || w.Path != "/data/copies/c1" || w.Repository != repo || w.Reference != "main" {
		t.Fatalf("workspace = %+v", w)
	}
	if err := l.Release(t.Context(), w, false); err != nil || len(c.removed) != 0 {
		t.Fatalf("kept checkout removed: %v %v", err, c.removed)
	}
	if err := l.Release(t.Context(), w, true); err != nil || len(c.removed) != 1 {
		t.Fatalf("checkout not removed: %v %v", err, c.removed)
	}
	if _, err := l.Prepare(t.Context(), "a1", workspace.Spec{Kind: workspace.KindCheckout}); !errors.Is(err, workspace.ErrInvalid) {
		t.Errorf("no repository: err = %v", err)
	}
	if _, err := (workspace.Local{}).Prepare(t.Context(), "a1", workspace.Spec{Kind: workspace.KindCheckout, Repository: repo}); !errors.Is(err, workspace.ErrUnavailable) {
		t.Errorf("no checkouts: err = %v", err)
	}
}
