package workspaces

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryStoreIsolationAndRollback(t *testing.T) {
	s := &MemoryStore{}
	ctx := context.Background()
	var retained *State
	if err := s.Update(ctx, func(st *State) error {
		st.Workspaces["one"] = Record{Workspace: Workspace{ID: "one"}}
		retained = st
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	delete(retained.Workspaces, "one")
	st, _ := s.Read(ctx)
	if len(st.Workspaces) != 1 {
		t.Fatal("callback map leaked")
	}
	delete(st.Workspaces, "one")
	err := s.Update(ctx, func(st *State) error { delete(st.Workspaces, "one"); return ErrInvalid })
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	st, _ = s.Read(ctx)
	if len(st.Workspaces) != 1 {
		t.Fatal("read isolation or rollback failed")
	}
}
