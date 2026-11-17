package store_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/agent"
	"github.com/r59q/forge-tools/forge-agents/library/agent/storetest"
	"github.com/r59q/forge-tools/forge-agents/library/transcript"
	"github.com/r59q/forge-tools/forge-agents/service/store"
)

func open(t *testing.T, dir string) *store.SQLite {
	t.Helper()
	s, err := store.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSQLite(t *testing.T) {
	storetest.Run(t, func(t *testing.T) agent.Store { return open(t, t.TempDir()) })
}

func TestDurable(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	ctx := t.Context()
	if err := s.Put(ctx, agent.Agent{ID: "a", Client: "codex", State: agent.StateIdle, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "a", transcript.Event{Kind: transcript.KindMessage, Text: "kept"}, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	events, err := s.Events(ctx, "a", 0, 10)
	if err != nil || len(events) != 1 || events[0].Text != "kept" {
		t.Fatalf("reopened events = %+v, %v", events, err)
	}
	info, err := os.Stat(filepath.Join(dir, store.FileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("database file mode: %v %v", info, err)
	}
}
