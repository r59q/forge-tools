package checkouts

import (
	"context"
	"encoding/hex"
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

	for _, sub := range []string{"copies", "repositories", "shadows"} {
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
		_ = lock.Close()
		return nil, ErrConflict
	}

	m := &Manager{
		root:    root,
		store:   store,
		resolve: resolve,
		gate:    make(chan struct{}, 1),
		lock:    lock,
	}
	err = m.recover(ctx)
	if err != nil {
		_ = m.Close()
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
	return m.store.Update(ctx, func(s *State) error {
		s.Checkouts[r.Checkout.ID] = r
		return pin(ctx, r)
	})
}

// failed records r as failed, even if ctx is already canceled.
func (m *Manager) failed(ctx context.Context, r Record) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
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
	validMode := w.Mode == "" || w.Mode == Worktree || w.Mode == Isolated && w.Repository.LocalPath == ""
	return validMode && validID(w.ID) && w.Location == filepath.Join(m.root, "copies", w.ID) && filepath.IsAbs(r.GitDirectory)
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
	if r.Checkout.Mode == Isolated {
		// .git belongs to the consumer. Reads validate it in the shadow;
		// forced removal must also work when it is missing or replaced.
		return nil
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

	view, cleanup, err := readView(ctx, r)
	if err != nil {
		return Status{}, err
	}
	defer cleanup()

	return status(ctx, view)
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

	view, cleanup, err := readView(ctx, r)
	if err != nil {
		return Diff{}, err
	}
	defer cleanup()
	var d Diff
	d.Staged, err = view.run(ctx, "diff", view.submodules(), "--no-ext-diff", "--no-textconv", "--binary", "--cached", "HEAD", "--")
	if err != nil {
		return Diff{}, err
	}

	d.Unstaged, err = view.run(ctx, "diff", view.submodules(), "--no-ext-diff", "--no-textconv", "--binary", "--")

	return d, err
}
