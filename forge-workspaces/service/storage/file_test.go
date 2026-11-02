package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-workspaces/library/workspaces"
)

func TestRestartAndRollback(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	f, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Update(ctx, func(s *workspaces.State) error {
		s.Workspaces["one"] = workspaces.Record{Workspace: workspaces.Workspace{ID: "one", State: workspaces.Retained}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := g.Read(ctx)
	if err != nil || state.Workspaces["one"].Workspace.State != workspaces.Retained {
		t.Fatal(state, err)
	}
	err = g.Update(ctx, func(s *workspaces.State) error { delete(s.Workspaces, "one"); return workspaces.ErrInvalid })
	if !errors.Is(err, workspaces.ErrInvalid) {
		t.Fatal(err)
	}
	state, _ = g.Read(ctx)
	if len(state.Workspaces) != 1 {
		t.Fatal("rollback failed")
	}
	info, _ := os.Stat(filepath.Join(dir, "workspaces.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}
func TestConcurrentHandlesAndCancellation(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(dir)
	b, _ := Open(dir)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f := a
			if i%2 == 0 {
				f = b
			}
			err := f.Update(context.Background(), func(st *workspaces.State) error {
				st.Workspaces[time.Now().String()+string(rune(i))] = workspaces.Record{}
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	s, _ := a.Read(context.Background())
	if len(s.Workspaces) != 20 {
		t.Fatal("lost updates", len(s.Workspaces))
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
	file := filepath.Join(dir, "workspaces.json")
	bad := []byte(`{"version":1`)
	if err := os.WriteFile(file, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("accepted corrupt data")
	}
	got, _ := os.ReadFile(file)
	if string(got) != string(bad) {
		t.Fatal("overwrote corrupt data")
	}
}
