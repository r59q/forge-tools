package checkouts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// Create checks out a detached commit without moving any existing checkout.
// Connected repositories use a managed bare cache; local repositories stay owned
// by their caller. A failed creation is recorded as failed, never ready.
func (m *Manager) Create(ctx context.Context, in Input) (Checkout, error) {
	if err := m.enter(ctx); err != nil {
		return Checkout{}, err
	}

	defer m.leave()
	repo := in.Repository
	ref := in.Reference
	if ref == "" {
		ref = "HEAD"
	}
	if strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\r\n:") || len(ref) > 1024 {
		return Checkout{}, ErrInvalid
	}

	source, err := m.checkoutSource(ctx, repo)
	if err != nil {
		return Checkout{}, err
	}

	repo = source.repository
	directory := source.directory

	w := Checkout{
		ID:         newID(),
		Repository: repo,
		Reference:  ref,
		State:      Creating,
		CreatedAt:  time.Now().UTC(),
	}
	w.Location = filepath.Join(m.root, "copies", w.ID)
	r := Record{Checkout: w, GitDirectory: directory}
	if err := m.save(ctx, r); err != nil {
		return Checkout{}, err
	}

	complete := false
	defer func() {
		if !complete {
			m.failed(ctx, r)
		}
	}()
	resolveRef, err := m.prepareSource(ctx, source, ref)
	if err != nil {
		return Checkout{}, err
	}

	commit, err := resolveCheckoutCommit(ctx, source.revisionDirectory, resolveRef)
	if err != nil {
		return Checkout{}, err
	}

	r.Checkout.Revision = strings.TrimSpace(commit)
	if err = m.save(ctx, r); err != nil {
		return Checkout{}, err
	}
	if _, err = git(ctx, directory, "worktree", "add", "--detach", "--", w.Location, r.Checkout.Revision); err != nil {
		return Checkout{}, err
	}

	r.Checkout.State = Ready
	if err = m.save(ctx, r); err != nil {
		return Checkout{}, err
	}

	complete = true

	return r.Checkout, nil
}

type checkoutSource struct {
	repository        Repository
	directory         string
	revisionDirectory string
}

func (m *Manager) checkoutSource(ctx context.Context, repo Repository) (checkoutSource, error) {
	if repo.LocalPath == "" {
		if repo.Source == "" || repo.Path == "" || m.resolve == nil {
			return checkoutSource{}, ErrInvalid
		}

		key := sha256.Sum256([]byte(repo.Source + "\x00" + repo.Path))
		directory := filepath.Join(m.root, "repositories", hex.EncodeToString(key[:]))

		return checkoutSource{repository: repo, directory: directory, revisionDirectory: directory}, nil
	}

	if repo.Source != "" || repo.Path != "" {
		return checkoutSource{}, ErrInvalid
	}

	path, err := filepath.Abs(repo.LocalPath)
	if err != nil {
		return checkoutSource{}, ErrInvalid
	}

	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return checkoutSource{}, ErrNotFound
	}

	common, err := git(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return checkoutSource{}, err
	}

	directory := strings.TrimSpace(common)
	repo.LocalPath = directory

	return checkoutSource{repository: repo, directory: directory, revisionDirectory: path}, nil
}

func (m *Manager) prepareSource(ctx context.Context, source checkoutSource, ref string) (string, error) {
	if source.repository.LocalPath != "" {
		return ref, nil
	}

	remote, err := m.resolve(ctx, source.repository.Source)
	if err != nil {
		return "", err
	}
	if err := fetch(ctx, remote, source.repository.Path, source.directory, ref); err != nil {
		return "", err
	}

	return "FETCH_HEAD", nil
}

func resolveCheckoutCommit(ctx context.Context, directory, ref string) (string, error) {
	commit, err := git(ctx, directory, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if errors.Is(err, errGit) {
		return "", ErrNotFound
	}

	return commit, err
}
