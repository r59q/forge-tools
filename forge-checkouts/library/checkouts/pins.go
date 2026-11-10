package checkouts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// pin runs inside the metadata update, before any borrower is created. A prior
// creating record journals the intent. Recovery reconciles refs after crashes or
// ambiguous store errors; metadata and Git cannot commit atomically together.
func pin(ctx context.Context, r Record) error {
	if r.Checkout.Mode != Isolated || r.Checkout.Revision == "" {
		return nil
	}

	_, err := git(ctx, r.GitDirectory, "-c", "core.fsync=reference", "update-ref", "--no-deref",
		"refs/checkouts/"+r.Checkout.ID, r.Checkout.Revision)
	return err
}

func unpin(ctx context.Context, r Record) error {
	if r.Checkout.Mode != Isolated {
		return nil
	}
	if _, err := os.Stat(filepath.Join(r.GitDirectory, "HEAD")); errors.Is(err, os.ErrNotExist) {
		return nil
	}

	return deletePin(ctx, r.GitDirectory, "refs/checkouts/"+r.Checkout.ID)
}

func deletePin(ctx context.Context, directory, ref string) error {
	_, err := git(ctx, directory, "-c", "core.fsync=reference", "update-ref", "--no-deref", "-d", ref)
	return err
}

func (m *Manager) recover(ctx context.Context) error {
	err := m.store.Update(ctx, func(s *State) error {
		if s.Checkouts == nil {
			s.Checkouts = map[string]Record{}
		}
		for id, r := range s.Checkouts {
			if id != r.Checkout.ID || !m.validRecord(r) {
				return ErrConflict
			}
			if r.Checkout.Mode == "" {
				r.Checkout.Mode = Worktree
			}
			if err := pin(ctx, r); err != nil {
				return err
			}
			if r.Checkout.State == Creating || r.Checkout.State == Removing {
				r.Checkout.State = Failed
			}
			s.Checkouts[id] = r
		}
		return nil
	})
	if err != nil {
		return err
	}

	return m.reconcilePins(ctx)
}

func (m *Manager) reconcilePins(ctx context.Context) error {
	s, err := m.store.Read(ctx)
	if err != nil {
		return err
	}
	caches, err := os.ReadDir(filepath.Join(m.root, "repositories"))
	if err != nil {
		return err
	}
	for _, cache := range caches {
		directory := filepath.Join(m.root, "repositories", cache.Name())
		if !cache.IsDir() {
			return ErrConflict
		}
		if err := reconcileCache(ctx, directory, s); err != nil {
			return err
		}
	}

	// Shadows are disposable and never part of a consumer's repository.
	root, err := os.OpenRoot(m.root)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.RemoveAll("shadows"); err != nil {
		return err
	}
	return root.Mkdir("shadows", 0700)
}

func reconcileCache(ctx context.Context, directory string, s State) error {
	if _, err := os.Stat(filepath.Join(directory, "HEAD")); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	out, err := git(ctx, directory, "for-each-ref", "--format=%(refname)", "refs/checkouts/")
	if err != nil {
		return err
	}
	for ref := range strings.FieldsSeq(out) {
		r, ok := s.Checkouts[strings.TrimPrefix(ref, "refs/checkouts/")]
		keep := ok && r.Checkout.Mode == Isolated && r.Checkout.Revision != "" && r.GitDirectory == directory
		if !keep {
			if err = deletePin(ctx, directory, ref); err != nil {
				return err
			}
		}
	}
	return nil
}
