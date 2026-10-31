package broker_test

import (
	"context"
	"errors"
	"testing"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
)

func TestAdminMetadata(t *testing.T) {
	ctx := context.Background()
	var store broker.MemoryStore
	var reg forgeauth.Registry
	if err := reg.Register(github.Provider{}); err != nil {
		t.Fatal(err)
	}
	sealer, err := broker.NewAESGCMSealer(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	m, err := broker.NewManager(&store, sealer, &reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"", "https://user:secret@example.com", "file:///tmp/forge", "https://example.com?token=secret", "https://example.com#secret"} {
		if _, err := m.CreateForge(ctx, forgeauth.GitHub, base, "label"); !errors.Is(err, broker.ErrInvalid) {
			t.Fatalf("invalid endpoint error: %v", err)
		}
	}
	if _, err := m.CreateForge(ctx, "unknown", "https://example.com", ""); !errors.Is(err, broker.ErrInvalid) {
		t.Fatalf("unknown provider: %v", err)
	}
	f, err := m.CreateForge(ctx, forgeauth.GitHub, "https://example.com/api/", "label")
	if err != nil {
		t.Fatal(err)
	}
	if f.ID == "" || f.BaseURL != "https://example.com/api" {
		t.Fatalf("forge: %v", f)
	}
	if got, err := m.ListForges(ctx); err != nil || len(got) != 1 || got[0] != f {
		t.Fatalf("forges: %v %v", got, err)
	}
	c := broker.Connection{ID: "c", ForgeID: f.ID, Status: broker.StatusOK}
	if err := store.CreateConnection(ctx, c, []byte("sealed")); err != nil {
		t.Fatal(err)
	}
	if got, err := m.GetConnection(ctx, c.ID); err != nil || got != c {
		t.Fatalf("connection: %v %v", got, err)
	}
	if got, err := m.ListConnections(ctx); err != nil || len(got) != 1 || got[0] != c {
		t.Fatalf("connections: %v %v", got, err)
	}
	if err := m.DeleteForge(ctx, f.ID); !errors.Is(err, broker.ErrConflict) {
		t.Fatal(err)
	}
	if _, _, err := m.CreateClient(ctx, "client", []broker.ID{c.ID}, true); err != nil {
		t.Fatal(err)
	}
	clients, err := m.ListClients(ctx)
	if err != nil || len(clients) != 1 || clients[0].TokenHash != [32]byte{} {
		t.Fatal("client listing exposed hashes or failed")
	}
	if err := m.DeleteConnection(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteForge(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetConnection(ctx, c.ID); !errors.Is(err, broker.ErrNotFound) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.CreateForge(canceled, forgeauth.GitHub, "https://example.com", ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
