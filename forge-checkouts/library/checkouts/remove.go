package checkouts

import (
	"context"
	"errors"
	"os"
	"strings"
)

// Remove deletes only this working copy and its Git registration. It refuses
// staged, modified, untracked or ignored files unless discard is explicitly true.
// Existing local repositories and managed repository caches are never deleted.
func (m *Manager) Remove(ctx context.Context, id string, discard bool) error {
	if err := m.enter(ctx); err != nil {
		return err
	}

	defer m.leave()
	r, err := m.record(ctx, id)
	if err != nil {
		return err
	}

	exists, err := removable(ctx, r, discard)
	if err != nil {
		return err
	}

	r.Checkout.State = Removing
	if err = m.save(ctx, r); err != nil {
		return err
	}

	args := []string{"worktree", "remove"}
	if discard {
		args = append(args, "--force")
	}

	args = append(args, "--", r.Checkout.Location)
	if _, err = git(ctx, r.GitDirectory, args...); err != nil && !exists {
		err = unregistered(ctx, r, err)
	}
	if err != nil {
		m.failed(ctx, r)
		return err
	}

	return m.store.Update(ctx, func(s *State) error {
		delete(s.Checkouts, id)
		return nil
	})
}

// removable checks that r's working copy may be removed, and reports whether
// it exists. Without discard, it refuses a copy with any changed file.
func removable(ctx context.Context, r Record, discard bool) (bool, error) {
	_, err := os.Lstat(r.Checkout.Location)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if err = checkedPath(ctx, r); err != nil {
		return true, err
	}
	if discard {
		return true, nil
	}

	s, err := status(ctx, r.Checkout.Location)
	if err != nil {
		return true, err
	}
	if len(s.Files) > 0 {
		return true, ErrConflict
	}

	return true, nil
}

// unregistered checks that a missing working copy left no Git registration,
// as a failed creation may never have registered one. It returns removeErr if
// a registration remains, and nil if r is safe to forget.
func unregistered(ctx context.Context, r Record, removeErr error) error {
	out, err := git(ctx, r.GitDirectory, "worktree", "list", "--porcelain", "-z")
	if err == nil && strings.Contains(out, "worktree "+r.Checkout.Location+"\x00") {
		return removeErr
	}
	if err == nil {
		return nil
	}

	if _, statErr := os.Stat(r.GitDirectory); !errors.Is(statErr, os.ErrNotExist) {
		return err
	}

	return nil
}
