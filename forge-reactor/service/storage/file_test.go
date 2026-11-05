package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

func open(t *testing.T, dir string) (*File, *reactor.Reactor) {
	t.Helper()
	f, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := reactor.New(reactor.Config{Store: f.Store()})
	if err != nil {
		t.Fatal(err)
	}

	return f, r
}

func TestReopenKeepsState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	ctx := context.Background()
	f, r := open(t, dir)
	if _, err := r.CreateSubscription(ctx, reactor.Subscription{ID: "ci"}); err != nil {
		t.Fatal(err)
	}
	e, _, err := r.Publish(ctx, reactor.Event{Type: "app.built"}, "k")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode: %v %v", info, err)
	}

	f, r = open(t, dir)
	defer f.Close()
	got, deliveries, err := r.Event(ctx, e.ID)
	if err != nil || got.ID != e.ID || len(deliveries) != 1 || deliveries[0].Status != reactor.StatusPending {
		t.Fatalf("after reopen: %v %v %v", got, deliveries, err)
	}
	if _, duplicate, err := r.Publish(ctx, reactor.Event{Type: "app.built"}, "k"); err != nil || !duplicate {
		t.Fatalf("idempotency key lost: %v %v", duplicate, err)
	}

	temps, _ := filepath.Glob(filepath.Join(dir, ".reactor-*"))
	if len(temps) != 0 {
		t.Fatalf("temporary files left: %v", temps)
	}
}

func TestLocked(t *testing.T) {
	dir := t.TempDir()
	f, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second owner: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	g, err := Open(dir)
	if err != nil {
		t.Fatalf("after close: %v", err)
	}
	_ = g.Close()
}

func TestCorruptState(t *testing.T) {
	for name, content := range map[string]string{
		"not json": "{",
		"version":  `{"version":2,"state":{}}`,
		"no state": `{"version":1}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, stateFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); err == nil {
			t.Errorf("%s: opened", name)
		}
		// A failed open releases the directory.
		if err := os.Remove(filepath.Join(dir, stateFile)); err != nil {
			t.Fatal(err)
		}
		f, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}

	if _, err := Open(""); !errors.Is(err, reactor.ErrInvalid) {
		t.Fatalf("empty directory: %v", err)
	}
}
