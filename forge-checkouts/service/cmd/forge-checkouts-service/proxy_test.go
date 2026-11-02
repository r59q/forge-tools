package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/r59q/forge-tools/forge-checkouts/library/checkouts"
	"github.com/r59q/forge-tools/forge-checkouts/service/internal/testrepo"
	"github.com/r59q/forge-tools/forge-checkouts/service/storage"
	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-connector/library/connector"
	"github.com/r59q/forge-tools/forge-connector/library/proxy"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
)

type brokerStub struct {
	base    string
	revoked atomic.Bool
}

func (b *brokerStub) Connection(_ context.Context, id broker.ID) (forgeauth.Connection, error) {
	if id != "gh" {
		return forgeauth.Connection{}, broker.ErrNotFound
	}
	return forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: b.base}, nil
}

func (b *brokerStub) VerifyClient(_ context.Context, token forgeauth.StaticToken, _ broker.ID) (broker.Client, error) {
	if token != "client" || b.revoked.Load() {
		return broker.Client{}, broker.ErrUnauthorized
	}
	return broker.Client{}, nil
}
func (b *brokerStub) IssueToken(_ context.Context, id broker.ID) (broker.IssuedToken, error) {
	if id != "gh" {
		return broker.IssuedToken{}, broker.ErrNotFound
	}
	return broker.IssuedToken{Kind: forgeauth.GitHub, BaseURL: b.base, GitBaseURL: b.base + "/git", Token: "forge-fixture-secret"}, nil
}
func (b *brokerStub) ClientConnections(_ context.Context, token forgeauth.StaticToken) ([]broker.ClientConnection, error) {
	if token != "client" || b.revoked.Load() {
		return nil, broker.ErrUnauthorized
	}
	return []broker.ClientConnection{{ID: "gh", Kind: forgeauth.GitHub, Status: broker.StatusOK}}, nil
}

func TestConnectedFetchThroughProxy(t *testing.T) {
	ctx := context.Background()
	repo := testrepo.New(t)
	root := t.TempDir()
	bare := filepath.Join(root, "owner", "repo.git")
	testrepo.Git(t, root, "clone", "--bare", repo, bare)
	backend := filepath.Join(testrepo.Git(t, repo, "--exec-path"), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Fatal(err)
	}
	cgiHandler := &cgi.Handler{Path: backend, Root: "/git", Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}}
	header := http.Header{}
	github.SetGitToken(header, "forge-fixture-secret")
	var requests atomic.Int64
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != header.Get("Authorization") {
			http.Error(w, "denied", 401)
			return
		}
		requests.Add(1)
		cgiHandler.ServeHTTP(w, r)
	}))
	defer forge.Close()
	stub := &brokerStub{base: forge.URL}
	reg := &forgeauth.Registry{}
	if err := reg.Register(github.Provider{}); err != nil {
		t.Fatal(err)
	}
	h, err := proxy.New(proxy.Config{Verifier: stub, TokenIssuer: stub, Connections: stub, Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	p := httptest.NewServer(h)
	defer p.Close()
	conn, err := connector.New(connector.Config{URL: p.URL, Credentials: forgeauth.StaticToken("client")})
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	store, err := storage.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	m, err := checkouts.New(ctx, data, store, sources(conn))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	var created []checkouts.Checkout
	first := testrepo.Git(t, repo, "rev-parse", "v1^{commit}")
	for _, ref := range []string{"main", "v1", first, "HEAD"} {
		w, err := m.Create(ctx, checkouts.Input{Repository: checkouts.Repository{Source: "gh", Path: "owner/repo"}, Reference: ref})
		if err != nil {
			t.Fatal(ref, err)
		}
		want := testrepo.Git(t, repo, "rev-parse", ref+"^{commit}")
		if w.Revision != want || w.Repository.LocalPath != "" {
			t.Fatal(w)
		}
		created = append(created, w)
	}
	if requests.Load() < 8 {
		t.Fatal("fetch did not use authenticated proxy", requests.Load())
	}
	state, err := store.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	backing := state.Checkouts[created[0].ID].GitDirectory
	for _, r := range state.Checkouts {
		if r.GitDirectory != backing {
			t.Fatal("cache not shared")
		}
	}
	config, err := os.ReadFile(filepath.Join(backing, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "secret") || strings.Contains(string(config), "127.0.0.1") || strings.Contains(string(config), "remote ") {
		t.Fatal("persisted temporary endpoint or credentials")
	}
	// Re-fetch a moving branch without changing existing copies.
	if err = os.WriteFile(filepath.Join(repo, "file"), []byte("third\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testrepo.Git(t, repo, "commit", "-am", "third")
	testrepo.Git(t, repo, "push", bare, "main")
	newCheckout, err := m.Create(ctx, checkouts.Input{Repository: checkouts.Repository{Source: "gh", Path: "owner/repo"}, Reference: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if newCheckout.Revision == created[0].Revision {
		t.Fatal("stale branch")
	}
	st, err := m.Inspect(ctx, created[0].ID)
	if err != nil || st.Revision != created[0].Revision {
		t.Fatal("old checkout moved", st, err)
	}
	stub.revoked.Store(true)
	if _, err = m.Create(ctx, checkouts.Input{Repository: checkouts.Repository{Source: "gh", Path: "owner/repo"}}); err == nil {
		t.Fatal("cached credentials bypassed revocation")
	}
	if _, err = m.Inspect(ctx, newCheckout.ID); err != nil {
		t.Fatal("local inspection needs connector", err)
	}
	for _, w := range append(created, newCheckout) {
		if err = m.Remove(ctx, w.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = os.Stat(backing); err != nil {
		t.Fatal("cache removed", err)
	}
	all, _ := m.List(ctx)
	if len(all) != 1 || all[0].State != checkouts.Failed {
		t.Fatal(all)
	}
	if err = m.Remove(ctx, all[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Get(ctx, created[0].ID); !errors.Is(err, checkouts.ErrNotFound) {
		t.Fatal(err)
	}
}
