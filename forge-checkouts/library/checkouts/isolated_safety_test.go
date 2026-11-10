package checkouts

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIsolatedRejectsGitDataSymlinks(t *testing.T) {
	for _, path := range []string{".git", ".git/HEAD", ".git/index", ".git/refs", ".git/objects"} {
		t.Run(path, func(t *testing.T) {
			m, _, w, cache := isolated(t)
			outside := t.TempDir()
			write(t, outside, "keep", "outside data")
			target := filepath.Join(w.Location, path)
			if err := os.RemoveAll(target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Inspect(t.Context(), w.ID); err == nil {
				t.Fatal("accepted replaced Git data")
			}
			if _, err := m.ReadDiff(t.Context(), w.ID); err == nil {
				t.Fatal("accepted replaced Git data")
			}
			if err := m.Remove(t.Context(), w.ID, true); err != nil {
				t.Fatal("cannot discard tampered checkout", err)
			}
			if data, err := os.ReadFile(filepath.Join(outside, "keep")); err != nil || string(data) != "outside data" {
				t.Fatal("removal followed a symlink", err)
			}
			if command(t, cache, "for-each-ref", "refs/checkouts/") != "" {
				t.Fatal("pin leaked")
			}
		})
	}
}

func TestIsolatedMissingGitAndReplacedLocation(t *testing.T) {
	m, _, w, _ := isolated(t)
	if err := os.RemoveAll(filepath.Join(w.Location, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Inspect(t.Context(), w.ID); err == nil {
		t.Fatal("accepted missing repository")
	}
	if err := m.Remove(t.Context(), w.ID, true); err != nil {
		t.Fatal(err)
	}

	w, err := m.Create(t.Context(), Input{Repository: w.Repository, Mode: Isolated})
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	write(t, outside, "keep", "outside data")
	if err = os.RemoveAll(w.Location); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, w.Location); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Inspect(t.Context(), w.ID); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = m.Remove(t.Context(), w.ID, true); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted replaced checkout directory", err)
	}
	if _, err = os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal(err)
	}
}

func TestIsolatedDoesNotReadNestedConfig(t *testing.T) {
	for _, state := range []string{"untracked", "staged", "committed"} {
		t.Run(state, func(t *testing.T) {
			m, _, w, _ := isolated(t)
			nested := filepath.Join(w.Location, "nested")
			command(t, w.Location, "init", nested)
			write(t, nested, "file", "nested")
			command(t, nested, "add", "file")
			command(t, nested, "commit", "-m", "nested")
			if state != "untracked" {
				command(t, w.Location, "add", "nested")
			}
			if state == "committed" {
				command(t, w.Location, "commit", "-m", "add gitlink")
			}
			write(t, nested, ".git/config", "[invalid syntax\n")
			if _, err := m.Inspect(t.Context(), w.ID); err != nil {
				t.Fatal("read nested repository config", err)
			}
			if _, err := m.ReadDiff(t.Context(), w.ID); err != nil {
				t.Fatal("read nested repository config", err)
			}
			if err := m.Remove(t.Context(), w.ID, false); !errors.Is(err, ErrConflict) {
				t.Fatal("discarded nested repository contents", err)
			}
			if err := m.Remove(t.Context(), w.ID, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}
