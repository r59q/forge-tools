package checkouts

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type memory struct {
	mu        sync.Mutex
	state     State
	failReady bool
}

func (s *memory) Read(ctx context.Context) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return State{}, err
	}

	b, _ := json.Marshal(s.state)
	var out State
	_ = json.Unmarshal(b, &out)

	return out, nil
}

func (s *memory) Update(ctx context.Context, fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	b, _ := json.Marshal(s.state)
	var out State
	_ = json.Unmarshal(b, &out)
	if err := fn(&out); err != nil {
		return err
	}
	if s.failReady {
		for _, r := range out.Checkouts {
			if r.Checkout.State == Ready {
				return errors.New("store failed")
			}
		}
	}

	s.state = out

	return nil
}

func command(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(
		os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Fixture",
		"GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture",
		"GIT_COMMITTER_EMAIL=fixture@example.com",
	)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s %v", args, b, err)
	}

	return strings.TrimSpace(string(b))
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func repository(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	command(t, dir, "init", "-b", "main")
	write(t, dir, "file", "original\n")
	write(t, dir, ".gitignore", "ignored\n")
	command(t, dir, "add", ".")
	command(t, dir, "commit", "-m", "first")
	first := command(t, dir, "rev-parse", "HEAD")
	command(t, dir, "tag", "-a", "v1", "-m", "tag")
	write(t, dir, "file", "second\n")
	command(t, dir, "commit", "-am", "second")

	return dir, first, command(t, dir, "rev-parse", "HEAD")
}

func manager(t *testing.T) (*Manager, *memory, string) {
	t.Helper()
	root := t.TempDir()
	s := &memory{}
	m, err := New(context.Background(), root, s, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { m.Close() })

	return m, s, root
}

func create(t *testing.T, m *Manager, repo, ref string) Checkout {
	t.Helper()
	w, err := m.Create(context.Background(), Input{Repository: Repository{LocalPath: repo}, Reference: ref})
	if err != nil {
		t.Fatal(err)
	}

	return w
}

func TestReferencesAndIndependentCheckouts(t *testing.T) {
	ctx := context.Background()
	repo, first, second := repository(t)
	m, _, _ := manager(t)
	for _, tc := range []struct{ ref, want string }{{"main", second}, {"v1", first}, {first, first}, {"", second}} {
		w := create(t, m, repo, tc.ref)
		if w.Revision != tc.want || w.State != Ready || w.CreatedAt.IsZero() {
			t.Fatal(w)
		}

		s, err := m.Inspect(ctx, w.ID)
		if err != nil || s.Revision != tc.want || s.Branch != "" {
			t.Fatal(s, err)
		}
	}

	a := create(t, m, repo, "main")
	write(t, a.Location, "file", "private edit\n")
	b := create(t, m, repo, "v1")
	data, _ := os.ReadFile(filepath.Join(a.Location, "file"))
	if string(data) != "private edit\n" {
		t.Fatal("other checkout changed")
	}

	data, _ = os.ReadFile(filepath.Join(b.Location, "file"))
	if string(data) != "original\n" {
		t.Fatal("wrong revision")
	}

	data, _ = os.ReadFile(filepath.Join(repo, "file"))
	if string(data) != "second\n" {
		t.Fatal("source changed")
	}

	command(t, a.Location, "switch", "-c", "checkout-branch")
	s, err := m.Inspect(ctx, a.ID)
	if err != nil || s.Branch != "checkout-branch" {
		t.Fatal(s, err)
	}

	command(t, a.Location, "add", "file")
	command(t, a.Location, "commit", "-m", "checkout commit")
	s, err = m.Inspect(ctx, a.ID)
	if err != nil || s.Revision == a.Revision {
		t.Fatal(s, err)
	}

	saved, _ := m.Get(ctx, a.ID)
	if saved.Revision != a.Revision {
		t.Fatal("creation revision changed")
	}
}

func TestCreateFromLinkedCheckoutHEAD(t *testing.T) {
	repo, first, _ := repository(t)
	m, _, _ := manager(t)
	original := create(t, m, repo, "v1")
	copy := create(t, m, original.Location, "HEAD")
	if copy.Revision != first {
		t.Fatalf("resolved main checkout HEAD: %s", copy.Revision)
	}

	r, err := m.record(context.Background(), copy.ID)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = checkedPath(ctx, r); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation", err)
	}
}

func TestConcurrentCreateAndCanceledWait(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := repository(t)
	m, _, _ := manager(t)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Create(ctx, Input{Repository: Repository{LocalPath: repo}, Reference: "v1"}); err != nil {
				t.Error(err)
			}
		}()
	}

	wg.Wait()
	all, err := m.List(ctx)
	if err != nil || len(all) != 5 {
		t.Fatal(all, err)
	}

	m.gate <- struct{}{}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = m.List(canceled)
	<-m.gate
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDiffOutputLimit(t *testing.T) {
	repo, _, _ := repository(t)
	m, _, _ := manager(t)
	w := create(t, m, repo, "main")
	write(t, w.Location, "file", strings.Repeat("large line\n", outputLimit/10+1))
	if _, err := m.ReadDiff(context.Background(), w.ID); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
}

func TestChangesDiffAndProtectedRemoval(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := repository(t)
	m, _, _ := manager(t)
	w := create(t, m, repo, "main")
	write(t, w.Location, "file", "staged\n")
	command(t, w.Location, "add", "file")
	write(t, w.Location, "file", "unstaged\n")
	write(t, w.Location, "new\nfile", "untracked\n")
	write(t, w.Location, "ignored", "valuable\n")
	s, err := m.Inspect(ctx, w.ID)
	if err != nil || len(s.Files) != 3 {
		t.Fatal(s, err)
	}

	found := map[string]File{}
	for _, f := range s.Files {
		found[f.Path] = f
	}

	if found["file"].Index != "M" || found["file"].Worktree != "M" || found["new\nfile"].Index != "?" || found["ignored"].Index != "!" {
		t.Fatal(s)
	}

	d, err := m.ReadDiff(ctx, w.ID)
	if err != nil ||
		!strings.Contains(d.Staged, "+staged") || !strings.Contains(d.Unstaged, "+unstaged") || strings.Contains(d.Staged, "+unstaged") {
		t.Fatal(d, err)
	}
	if err = m.Remove(ctx, w.ID, false); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}

	got, _ := m.Get(ctx, w.ID)
	if got.State != Ready {
		t.Fatal(got)
	}
	if _, err = m.Retain(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if err = m.Remove(ctx, w.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(w.Location); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err = m.Get(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if command(t, repo, "rev-parse", "--abbrev-ref", "HEAD") != "main" {
		t.Fatal("source checkout changed")
	}
	if strings.Contains(command(t, repo, "worktree", "list", "--porcelain"), w.Location) {
		t.Fatal("registration leaked")
	}
}

func TestEachDirtyKindIsProtected(t *testing.T) {
	for _, kind := range []string{"staged", "modified", "untracked", "ignored"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			repo, _, _ := repository(t)
			m, _, _ := manager(t)
			w := create(t, m, repo, "main")
			name := "file"
			if kind == "untracked" || kind == "ignored" {
				name = kind
			}

			write(t, w.Location, name, "work\n")
			if kind == "staged" {
				command(t, w.Location, "add", name)
			}
			if err := m.Remove(ctx, w.ID, false); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
		})
	}
}

func TestRenameAndCleanRemoval(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := repository(t)
	m, _, _ := manager(t)
	w := create(t, m, repo, "main")
	command(t, w.Location, "mv", "file", "renamed")
	s, err := m.Inspect(ctx, w.ID)
	if err != nil || len(s.Files) != 1 || s.Files[0].OldPath != "file" || s.Files[0].Path != "renamed" {
		t.Fatal(s, err)
	}

	command(t, w.Location, "reset", "--hard", "HEAD")
	if err = m.Remove(ctx, w.ID, false); err != nil {
		t.Fatal(err)
	}
}

func TestRestartRecoveryAndOwnership(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := repository(t)
	m, s, root := manager(t)
	w := create(t, m, repo, "main")
	if _, err := m.Retain(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := New(ctx, root, s, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate manager", err)
	}

	failed := create(t, m, repo, "v1")
	err := s.Update(ctx, func(s *State) error {
		r := s.Checkouts[failed.ID]
		r.Checkout.State = Creating
		s.Checkouts[failed.ID] = r

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	m2, err := New(ctx, root, s, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer m2.Close()
	got, err := m2.Get(ctx, w.ID)
	if err != nil || got.State != Retained {
		t.Fatal(got, err)
	}

	got, err = m2.Get(ctx, failed.ID)
	if err != nil || got.State != Failed {
		t.Fatal(got, err)
	}
	if err = m2.Remove(ctx, failed.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(w.Location); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(repo, w.Location); err != nil {
		t.Fatal(err)
	}
	if err = m2.Remove(ctx, w.ID, true); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(repo, "file")); err != nil {
		t.Fatal("source deleted", err)
	}
}

func TestCreationFailures(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := repository(t)
	m, s, _ := manager(t)
	if _, err := m.Create(ctx, Input{Repository: Repository{LocalPath: repo}, Reference: "missing"}); err == nil {
		t.Fatal("accepted missing ref")
	}

	all, _ := m.List(ctx)
	if len(all) != 1 || all[0].State != Failed {
		t.Fatal(all)
	}
	if err := m.Remove(ctx, all[0].ID, false); err != nil {
		t.Fatal(err)
	}

	s.failReady = true
	if _, err := m.Create(ctx, Input{Repository: Repository{LocalPath: repo}}); err == nil {
		t.Fatal("ignored persistence error")
	}

	all, _ = m.List(ctx)
	if len(all) != 1 || all[0].State != Failed {
		t.Fatal(all)
	}

	s.failReady = false
	if err := m.Remove(ctx, all[0].ID, false); err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.Create(canceled, Input{Repository: Repository{LocalPath: repo}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}

	for _, ref := range []string{"--help", "main\nHEAD", "main:evil"} {
		if _, err := m.Create(ctx, Input{Repository: Repository{LocalPath: repo}, Reference: ref}); !errors.Is(err, ErrInvalid) {
			t.Fatal(ref, err)
		}
	}
}
