package storage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-checkouts/library/checkouts"
	"github.com/r59q/forge-tools/forge-checkouts/service/internal/testrepo"
)

func TestRestartAndRollback(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	f, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}

	err = f.Update(ctx, func(s *checkouts.State) error {
		s.Checkouts["one"] = checkouts.Record{Checkout: checkouts.Checkout{ID: "one", State: checkouts.Retained}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	g, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}

	state, err := g.Read(ctx)
	if err != nil || state.Checkouts["one"].Checkout.State != checkouts.Retained {
		t.Fatal(state, err)
	}

	err = g.Update(ctx, func(s *checkouts.State) error {
		delete(s.Checkouts, "one")
		return checkouts.ErrInvalid
	})
	if !errors.Is(err, checkouts.ErrInvalid) {
		t.Fatal(err)
	}

	state, _ = g.Read(ctx)
	if len(state.Checkouts) != 1 {
		t.Fatal("rollback failed")
	}

	info, _ := os.Stat(filepath.Join(dir, "checkouts.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}

func TestConcurrentHandlesAndCancellation(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(t.Context(), dir)
	b, _ := Open(t.Context(), dir)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f := a
			if i%2 == 0 {
				f = b
			}

			err := f.Update(context.Background(), func(st *checkouts.State) error {
				st.Checkouts[time.Now().String()+string(rune(i))] = checkouts.Record{}
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}

	wg.Wait()
	s, _ := a.Read(context.Background())
	if len(s.Checkouts) != 20 {
		t.Fatal("lost updates", len(s.Checkouts))
	}

	lock, err := a.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	defer unlock(lock)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = b.Read(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestCorruptStoreIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "checkouts.json")
	bad := []byte(`{"version":1`)
	if err := os.WriteFile(file, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), dir); err == nil {
		t.Fatal("accepted corrupt data")
	}

	got, _ := os.ReadFile(file)
	if string(got) != string(bad) {
		t.Fatal("overwrote corrupt data")
	}
}

func TestLegacyMetadataKeepsCheckoutsAndChanges(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}

	manager, err := checkouts.New(ctx, dir, store, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { manager.Close() })
	w, err := manager.Create(ctx, checkouts.Input{Repository: checkouts.Repository{LocalPath: testrepo.New(t)}, Reference: "v1"})
	if err != nil {
		t.Fatal(err)
	}

	w, err = manager.Retain(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(w.Location, "file"), []byte("retained experiment\n"), 0600); err != nil {
		t.Fatal(err)
	}

	state, err := store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}

	manager.Close()
	record := state.Checkouts[w.ID]
	legacy, err := json.Marshal(map[string]any{"version": 1, "state": map[string]any{"workspaces": map[string]any{w.ID: map[string]any{"workspace": record.Checkout, "git_directory": record.GitDirectory}}}})
	if err != nil {
		t.Fatal(err)
	}

	oldFile := filepath.Join(dir, "workspaces.json")
	if err = os.WriteFile(oldFile, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(dir, "checkouts.json")); err != nil {
		t.Fatal(err)
	}

	store, err = Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}

	next, err := checkouts.New(ctx, dir, store, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer next.Close()
	got, err := next.Get(ctx, w.ID)
	if err != nil || got != w {
		t.Fatal(got, err)
	}

	status, err := next.Inspect(ctx, w.ID)
	if err != nil || len(status.Files) != 1 || status.Files[0].Worktree != "M" {
		t.Fatal(status, err)
	}
	if err = next.Remove(ctx, w.ID, false); !errors.Is(err, checkouts.ErrConflict) {
		t.Fatal(err)
	}
	if err = next.Remove(ctx, w.ID, true); err != nil {
		t.Fatal(err)
	}

	// The new empty state must take precedence over the preserved legacy file.
	reopened, err := Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}

	state, err = reopened.Read(ctx)
	if err != nil || len(state.Checkouts) != 0 {
		t.Fatal(state, err)
	}

	original, err := os.ReadFile(oldFile)
	if err != nil || string(original) != string(legacy) {
		t.Fatal("legacy metadata changed", err)
	}
}

func TestInvalidLegacyMetadataIsPreserved(t *testing.T) {
	for _, content := range []string{`{"version":1`, `{"version":2,"state":{"workspaces":{}}}`, `{"version":1,"state":{}}`} {
		dir := t.TempDir()
		file := filepath.Join(dir, "workspaces.json")
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(t.Context(), dir); err == nil {
			t.Fatal("accepted invalid legacy data")
		}

		got, err := os.ReadFile(file)
		if err != nil || string(got) != content {
			t.Fatal("changed invalid legacy data", err)
		}
		if _, err = os.Stat(filepath.Join(dir, "checkouts.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("created new state over invalid metadata", err)
		}
	}
}
