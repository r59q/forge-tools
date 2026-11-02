package checkouts

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
		st.Checkouts["one"] = Record{Checkout: Checkout{ID: "one"}}
		retained = st
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	delete(retained.Checkouts, "one")
	st, _ := s.Read(ctx)
	if len(st.Checkouts) != 1 {
		t.Fatal("callback map leaked")
	}
	delete(st.Checkouts, "one")
	err := s.Update(ctx, func(st *State) error { delete(st.Checkouts, "one"); return ErrInvalid })
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	st, _ = s.Read(ctx)
	if len(st.Checkouts) != 1 {
		t.Fatal("read isolation or rollback failed")
	}
}
