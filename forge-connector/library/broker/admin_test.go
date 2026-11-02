package broker_test

import (
	"context"
	"errors"
	"testing"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
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

func TestForgeGitBaseURL(t *testing.T) {
	ctx := context.Background()
	var store broker.MemoryStore
	var reg forgeauth.Registry
	for _, p := range []forgeauth.Provider{github.Provider{}, gitlab.Provider{}, forgejo.Provider{}} {
		if err := reg.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	sealer, err := broker.NewAESGCMSealer(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	m, err := broker.NewManager(&store, sealer, &reg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind              forgeauth.Kind
		base, git, wanted string
	}{
		{forgeauth.GitHub, "https://api.github.com", "", "https://github.com"},
		{forgeauth.GitHub, "https://ghes.example/api/v3", "", "https://ghes.example"},
		{forgeauth.GitHub, "https://example.com/api", "", ""},
		{forgeauth.GitLab, "https://gitlab.example/sub/api/v4/", "", "https://gitlab.example/sub"},
		{forgeauth.Forgejo, "http://codeberg.example/api/v1", "", "http://codeberg.example"},
		{forgeauth.GitHub, "https://example.com/api", " https://git.example/root/ ", "https://git.example/root"},
	} {
		f, err := m.RegisterForge(ctx, broker.Forge{Kind: tc.kind, BaseURL: tc.base, GitBaseURL: tc.git})
		if err != nil || f.GitBaseURL != tc.wanted {
			t.Errorf("%s %s: GitBaseURL = %q, %v; want %q", tc.kind, tc.base, f.GitBaseURL, err, tc.wanted)
		}
	}
	for _, git := range []string{"ftp://git.example", "https://user:secret@git.example", "https://git.example?x=1", "https://git.example#f", "git.example", "https://"} {
		if _, err := m.RegisterForge(ctx, broker.Forge{Kind: forgeauth.GitHub, BaseURL: "https://api.github.com", GitBaseURL: git}); !errors.Is(err, broker.ErrInvalid) {
			t.Errorf("git base %q: %v", git, err)
		}
	}
	// Records stored before GitBaseURL existed get the default when read.
	legacy := broker.Forge{ID: "legacy", Kind: forgeauth.GitHub, BaseURL: "https://api.github.com"}
	if err := store.CreateForge(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	forges, err := m.ListForges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range forges {
		if f.ID == legacy.ID && f.GitBaseURL != "https://github.com" {
			t.Fatalf("legacy forge = %+v", f)
		}
	}
}
