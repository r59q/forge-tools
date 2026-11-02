package checkouts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Manager serializes operations and exclusively owns its root until Close.
// Use a private local filesystem with flock support. Consumers must stop writing
// to a checkout before Remove; Git worktrees do not isolate running processes.
type Manager struct {
	root    string
	store   Store
	resolve Resolve
	gate    chan struct{}
	lock    *os.File
}

// New opens root and recovers interrupted operations as failed, preserving their
// files for inspection or explicit removal. Ready and retained checkouts remain.
// The root and store must always be reused together, at their original location.
func New(ctx context.Context, root string, store Store, resolve Resolve) (*Manager, error) {
	if root == "" || store == nil {
		return nil, ErrInvalid
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	for _, sub := range []string{"copies", "repositories"} {
		path := filepath.Join(root, sub)
		if err = os.MkdirAll(path, 0700); err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			return nil, ErrConflict
		}
	}
	lock, err := os.OpenFile(filepath.Join(root, "manager.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, ErrConflict
	}
	m := &Manager{root: root, store: store, resolve: resolve, gate: make(chan struct{}, 1), lock: lock}
	err = store.Update(ctx, func(s *State) error {
		if s.Checkouts == nil {
			s.Checkouts = map[string]Record{}
		}
		for id, r := range s.Checkouts {
			if id != r.Checkout.ID || !m.validRecord(r) {
				return ErrConflict
			}
			if r.Checkout.State == Creating || r.Checkout.State == Removing {
				r.Checkout.State = Failed
				s.Checkouts[id] = r
			}
		}
		return nil
	})
	if err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

// Close releases root ownership after outstanding operations finish. It does not
// remove working files. Calls after Close return ErrConflict.
func (m *Manager) Close() error {
	m.gate <- struct{}{}
	defer func() { <-m.gate }()
	if m.lock == nil {
		return nil
	}
	_ = syscall.Flock(int(m.lock.Fd()), syscall.LOCK_UN)
	err := m.lock.Close()
	m.lock = nil
	return err
}
func (m *Manager) enter(ctx context.Context) error {
	select {
	case m.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if m.lock == nil {
		<-m.gate
		return ErrConflict
	}
	return nil
}
func (m *Manager) leave() { <-m.gate }
func (m *Manager) save(ctx context.Context, r Record) error {
	return m.store.Update(ctx, func(s *State) error { s.Checkouts[r.Checkout.ID] = r; return nil })
}
func (m *Manager) failed(r Record) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r.Checkout.State = Failed
	_ = m.save(ctx, r) // An interrupted state is also recovered as failed on restart.
}
func validID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && id == strings.ToLower(id)
}
func (m *Manager) validRecord(r Record) bool {
	w := r.Checkout
	return validID(w.ID) && w.Location == filepath.Join(m.root, "copies", w.ID) && filepath.IsAbs(r.GitDirectory)
}
func (m *Manager) record(ctx context.Context, id string) (Record, error) {
	if !validID(id) {
		return Record{}, ErrInvalid
	}
	s, err := m.store.Read(ctx)
	if err != nil {
		return Record{}, err
	}
	r, ok := s.Checkouts[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	if !m.validRecord(r) {
		return Record{}, ErrConflict
	}
	return r, nil
}

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
	var directory, revisionDirectory string
	if repo.LocalPath != "" {
		if repo.Source != "" || repo.Path != "" {
			return Checkout{}, ErrInvalid
		}
		path, err := filepath.Abs(repo.LocalPath)
		if err != nil {
			return Checkout{}, ErrInvalid
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return Checkout{}, ErrNotFound
		}
		common, err := git(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return Checkout{}, err
		}
		directory = strings.TrimSpace(common)
		revisionDirectory = path
		repo.LocalPath = directory
	} else {
		if repo.Source == "" || repo.Path == "" || m.resolve == nil {
			return Checkout{}, ErrInvalid
		}
		key := sha256.Sum256([]byte(repo.Source + "\x00" + repo.Path))
		directory = filepath.Join(m.root, "repositories", hex.EncodeToString(key[:]))
		revisionDirectory = directory
	}
	w := Checkout{ID: newID(), Repository: repo, Reference: ref, State: Creating, CreatedAt: time.Now().UTC()}
	w.Location = filepath.Join(m.root, "copies", w.ID)
	r := Record{Checkout: w, GitDirectory: directory}
	if err := m.save(ctx, r); err != nil {
		return Checkout{}, err
	}
	complete := false
	defer func() {
		if !complete {
			m.failed(r)
		}
	}()
	resolveRef := ref
	if repo.LocalPath == "" {
		remote, err := m.resolve(ctx, repo.Source)
		if err != nil {
			return Checkout{}, err
		}
		if err = os.MkdirAll(directory, 0700); err != nil {
			return Checkout{}, err
		}
		if _, err = git(ctx, "", "init", "--bare", "--", directory); err != nil {
			return Checkout{}, err
		}
		if err = fetch(ctx, remote, repo.Path, directory, ref); err != nil {
			return Checkout{}, err
		}
		resolveRef = "FETCH_HEAD"
	}
	commit, err := git(ctx, revisionDirectory, "rev-parse", "--verify", "--end-of-options", resolveRef+"^{commit}")
	if err != nil {
		if errors.Is(err, errGit) {
			return Checkout{}, ErrNotFound
		}
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

// List returns metadata in creation order, including failed operations.
func (m *Manager) List(ctx context.Context) ([]Checkout, error) {
	if err := m.enter(ctx); err != nil {
		return nil, err
	}
	defer m.leave()
	s, err := m.store.Read(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Checkout, 0, len(s.Checkouts))
	for _, r := range s.Checkouts {
		result = append(result, r.Checkout)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

// Get returns saved metadata even if the working copy is missing or failed.
func (m *Manager) Get(ctx context.Context, id string) (Checkout, error) {
	if err := m.enter(ctx); err != nil {
		return Checkout{}, err
	}
	defer m.leave()
	r, err := m.record(ctx, id)
	return r.Checkout, err
}

// Retain marks a usable checkout as retained. It remains until explicitly removed;
// there is no automatic expiry, including for ordinary ready checkouts.
func (m *Manager) Retain(ctx context.Context, id string) (Checkout, error) {
	if err := m.enter(ctx); err != nil {
		return Checkout{}, err
	}
	defer m.leave()
	r, err := m.record(ctx, id)
	if err != nil {
		return Checkout{}, err
	}
	if r.Checkout.State != Ready && r.Checkout.State != Retained {
		return Checkout{}, ErrConflict
	}
	r.Checkout.State = Retained
	return r.Checkout, m.save(ctx, r)
}

// checkedPath refuses replaced directories and worktrees attached elsewhere.
func checkedPath(ctx context.Context, r Record) error {
	info, err := os.Lstat(r.Checkout.Location)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return ErrConflict
	}
	common, err := git(ctx, r.Checkout.Location, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	if strings.TrimSpace(common) != r.GitDirectory {
		return ErrConflict
	}
	return nil
}

// Inspect reads the current commit, branch and staged, modified, untracked and
// ignored paths. It can also inspect failed creations that have a checkout.
func (m *Manager) Inspect(ctx context.Context, id string) (Status, error) {
	if err := m.enter(ctx); err != nil {
		return Status{}, err
	}
	defer m.leave()
	r, err := m.record(ctx, id)
	if err != nil {
		return Status{}, err
	}
	if err = checkedPath(ctx, r); err != nil {
		return Status{}, err
	}
	return status(ctx, r.Checkout.Location)
}

// ReadDiff returns separate staged and unstaged patches, each at most 8 MiB.
func (m *Manager) ReadDiff(ctx context.Context, id string) (Diff, error) {
	if err := m.enter(ctx); err != nil {
		return Diff{}, err
	}
	defer m.leave()
	r, err := m.record(ctx, id)
	if err != nil {
		return Diff{}, err
	}
	if err = checkedPath(ctx, r); err != nil {
		return Diff{}, err
	}
	var d Diff
	d.Staged, err = git(ctx, r.Checkout.Location, "diff", "--no-ext-diff", "--no-textconv", "--binary", "--cached", "HEAD", "--")
	if err != nil {
		return Diff{}, err
	}
	d.Unstaged, err = git(ctx, r.Checkout.Location, "diff", "--no-ext-diff", "--no-textconv", "--binary", "--")
	return d, err
}

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
	_, statErr := os.Lstat(r.Checkout.Location)
	if statErr == nil {
		if err = checkedPath(ctx, r); err != nil {
			return err
		}
		if !discard {
			s, err := status(ctx, r.Checkout.Location)
			if err != nil {
				return err
			}
			if len(s.Files) > 0 {
				return ErrConflict
			}
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
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
	_, err = git(ctx, r.GitDirectory, args...)
	if err != nil && statErr == nil {
		m.failed(r)
		return err
	}
	// A failed creation may never have registered a worktree. A missing path
	// is safe to forget only when no registration remains.
	if err != nil {
		out, listErr := git(ctx, r.GitDirectory, "worktree", "list", "--porcelain", "-z")
		if listErr == nil && strings.Contains(out, "worktree "+r.Checkout.Location+"\x00") {
			m.failed(r)
			return err
		}
		if listErr != nil {
			if _, e := os.Stat(r.GitDirectory); !errors.Is(e, os.ErrNotExist) {
				m.failed(r)
				return listErr
			}
		}
	}
	return m.store.Update(ctx, func(s *State) error { delete(s.Checkouts, id); return nil })
}
