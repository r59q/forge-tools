package checkouts

import (
	"context"
	"errors"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

func connectedManager(t *testing.T) (*Manager, *memory, Input) {
	t.Helper()
	repo, _, _ := repository(t)
	remoteRoot := t.TempDir()
	command(t, remoteRoot, "clone", "--bare", repo, filepath.Join(remoteRoot, "owner", "repo.git"))
	server := httptest.NewServer(&cgi.Handler{
		Path: filepath.Join(command(t, repo, "--exec-path"), "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + remoteRoot, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"},
	})
	t.Cleanup(server.Close)
	m, store, _ := manager(t)
	m.resolve = func(context.Context, string) (*forgeauth.GitRemote, error) {
		return &forgeauth.GitRemote{BaseURL: server.URL, Client: server.Client()}, nil
	}

	return m, store, Input{Repository: Repository{Source: "fixture", Path: "owner/repo"}, Mode: Isolated}
}

func isolated(t *testing.T) (*Manager, *memory, Checkout, string) {
	t.Helper()
	m, store, in := connectedManager(t)
	w, err := m.Create(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.record(t.Context(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	return m, store, w, r.GitDirectory
}

func TestIsolatedGitAndPruning(t *testing.T) {
	m, _, w, cache := isolated(t)
	ctx := t.Context()
	if w.Mode != Isolated || command(t, cache, "rev-parse", "refs/checkouts/"+w.ID) != w.Revision {
		t.Fatal(w)
	}
	config, err := os.ReadFile(filepath.Join(cache, "config"))
	if err != nil {
		t.Fatal(err)
	}
	refs := command(t, cache, "show-ref")
	command(t, w.Location, "log", "--oneline")
	command(t, w.Location, "switch", "-c", "agent")
	write(t, w.Location, "own", "new object\n")
	command(t, w.Location, "add", "own")
	command(t, w.Location, "commit", "-m", "agent commit")
	command(t, w.Location, "rebase", "--force-rebase", "HEAD~1")
	write(t, w.Location, "file", "stashed\n")
	command(t, w.Location, "stash", "push")
	command(t, w.Location, "stash", "pop")
	command(t, w.Location, "reset", "--hard", "HEAD")
	command(t, w.Location, "pack-refs", "--all")
	command(t, w.Location, "update-index", "--split-index")
	st, err := m.Inspect(ctx, w.ID)
	if err != nil || st.Revision == w.Revision || st.Revision != command(t, w.Location, "rev-parse", "HEAD") || st.Branch != "agent" {
		t.Fatal(st, err)
	}
	if command(t, cache, "show-ref") != refs {
		t.Fatal("borrower changed cache refs")
	}
	gotConfig, _ := os.ReadFile(filepath.Join(cache, "config"))
	if string(config) != string(gotConfig) {
		t.Fatal("borrower changed cache config")
	}
	// FETCH_HEAD is deliberately removed: only the pin keeps the starting commit.
	if err = os.Remove(filepath.Join(cache, "FETCH_HEAD")); err != nil {
		t.Fatal(err)
	}
	command(t, cache, "gc", "--prune=now")
	command(t, w.Location, "fsck", "--full")
	if _, err = m.Retain(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if err = m.Remove(ctx, w.ID, false); err != nil {
		t.Fatal(err)
	}
	if command(t, cache, "for-each-ref", "refs/checkouts/") != "" {
		t.Fatal("pin survived removal")
	}
}

func TestIsolatedHostIgnoresConfig(t *testing.T) {
	for _, attack := range []string{"filter", "worktree", "include", "config-symlink", "alternates"} {
		t.Run(attack, func(t *testing.T) {
			m, _, w, _ := isolated(t)
			outside := t.TempDir()
			marker := filepath.Join(outside, "executed")
			write(t, w.Location, "file", "staged\n")
			command(t, w.Location, "add", "file")
			write(t, w.Location, "file", "unstaged\n")
			write(t, w.Location, ".gitattributes", "file filter=x\n")
			config := filepath.Join(w.Location, ".git", "config")
			switch attack {
			case "filter":
				command(t, w.Location, "config", "filter.x.clean", "touch '"+marker+"'; cat")
				command(t, w.Location, "config", "filter.x.required", "true")
			case "worktree":
				write(t, outside, "file", "outside secret\n")
				command(t, w.Location, "config", "core.worktree", outside)
			case "include":
				// Invalid syntax makes any attempt to read the include fail.
				write(t, outside, "config", "[invalid syntax\n")
				command(t, w.Location, "config", "include.path", filepath.Join(outside, "config"))
			case "config-symlink":
				write(t, outside, "config", "[invalid syntax\n")
				if err := os.Remove(config); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "config"), config); err != nil {
					t.Fatal(err)
				}
			case "alternates":
				write(t, w.Location, ".git/objects/info/alternates", outside+"\n")
			}
			st, err := m.Inspect(t.Context(), w.ID)
			if err != nil || len(st.Files) != 2 {
				t.Fatal(st, err)
			}
			diff, err := m.ReadDiff(t.Context(), w.ID)
			if err != nil || !strings.Contains(diff.Staged, "+staged") || !strings.Contains(diff.Unstaged, "+unstaged") ||
				strings.Contains(diff.Unstaged, "outside secret") {
				t.Fatal(diff, err)
			}
			if _, err = os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("host ran the clean filter", err)
			}
			if err = m.Remove(t.Context(), w.ID, false); !errors.Is(err, ErrConflict) {
				t.Fatal("dirty checkout removed", err)
			}
			if err = os.Symlink(outside, filepath.Join(w.Location, "outside")); err != nil {
				t.Fatal(err)
			}
			if err = m.Remove(t.Context(), w.ID, true); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(outside); err != nil {
				t.Fatal("removal followed symlink", err)
			}
		})
	}
}

func TestIsolatedModesAndLocalRejection(t *testing.T) {
	repo, _, _ := repository(t)
	m, _, _ := manager(t)
	for _, mode := range []string{Isolated, "invalid"} {
		_, err := m.Create(t.Context(), Input{Repository: Repository{LocalPath: repo}, Mode: mode})
		if !errors.Is(err, ErrInvalid) {
			t.Fatal(mode, err)
		}
	}
	for _, mode := range []string{"", Worktree} {
		w, err := m.Create(t.Context(), Input{Repository: Repository{LocalPath: repo}, Mode: mode})
		if err != nil || w.Mode != Worktree {
			t.Fatal(w, err)
		}
	}
	if command(t, repo, "for-each-ref", "refs/checkouts/") != "" {
		t.Fatal("local source received a pin")
	}
}

func TestIsolatedRecoveryAndFailedRemoval(t *testing.T) {
	m, store, w, cache := isolated(t)
	ctx := t.Context()
	if _, err := m.Retain(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	store.failReady = true
	_, err := m.Create(ctx, Input{Repository: w.Repository, Mode: Isolated})
	if err == nil {
		t.Fatal("ignored failed store")
	}
	store.failReady = false
	// Simulate crashes after pin creation and before/after metadata commits.
	orphan := newID()
	command(t, cache, "update-ref", "refs/checkouts/"+orphan, w.Revision)
	command(t, cache, "update-ref", "-d", "refs/checkouts/"+w.ID)
	if err = store.Update(ctx, func(s *State) error {
		r := s.Checkouts[w.ID]
		r.Checkout.State = Creating
		s.Checkouts[w.ID] = r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	m2, err := New(ctx, m.root, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if command(t, cache, "rev-parse", "refs/checkouts/"+w.ID) != w.Revision ||
		command(t, cache, "for-each-ref", "refs/checkouts/"+orphan) != "" {
		t.Fatal("pins not reconciled")
	}
	all, err := m2.List(ctx)
	if err != nil || len(all) != 2 {
		t.Fatal(all, err)
	}
	for _, copy := range all {
		if copy.State != Failed || copy.Mode != Isolated {
			t.Fatal(copy)
		}
		if err = m2.Remove(ctx, copy.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	if command(t, cache, "for-each-ref", "refs/checkouts/") != "" {
		t.Fatal("failed copies left pins")
	}
}
