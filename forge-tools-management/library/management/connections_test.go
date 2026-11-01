package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
)

const goodToken, newToken = "forge-secret-good", "forge-secret-new"

// connector runs a real broker against a fake GitLab that accepts the
// tokens in valid.
func connector(t *testing.T) (*broker.Manager, string, *atomic.Value) {
	t.Helper()
	var valid atomic.Value
	valid.Store([]string{goodToken, newToken})
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := gitlab.Provider{}.TokenFromHeader(r.Header)
		ok := false
		for _, v := range valid.Load().([]string) {
			ok = ok || token == v
		}
		if r.URL.Path != "/api/v4/user" || !ok {
			http.Error(w, "rejected "+token, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "username": "owner"})
	}))
	t.Cleanup(forge.Close)
	registry := &forgeauth.Registry{}
	if err := registry.Register(gitlab.Provider{}); err != nil {
		t.Fatal(err)
	}
	sealer, err := broker.NewAESGCMSealer(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	m, err := broker.NewManager(&broker.MemoryStore{}, sealer, registry)
	if err != nil {
		t.Fatal(err)
	}
	return m, forge.URL, &valid
}

func TestConnectionLifecycle(t *testing.T) {
	ctx := context.Background()
	m, host, valid := connector(t)
	c := New(Config{Connector: m})
	if got, err := c.ListConnections(ctx); err != nil || !got.Configured || len(got.Connections) != 0 {
		t.Fatalf("empty ListConnections = %+v, %v", got, err)
	}
	add := NewConnection{Kind: forgeauth.GitLab, Host: host, Name: " me ", Credential: goodToken}
	first, err := c.AddConnection(ctx, add)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.Name != "me" || first.Kind != forgeauth.GitLab || first.BaseURL != host+"/api/v4" || first.Login != "owner" || first.Status != broker.StatusOK {
		t.Fatalf("AddConnection = %+v", first)
	}
	second, err := c.AddConnection(ctx, add)
	if err != nil {
		t.Fatal(err)
	}
	if forges, _ := m.ListForges(ctx); len(forges) != 1 {
		t.Fatalf("forge not reused: %+v", forges)
	}

	for _, bad := range []NewConnection{
		{Kind: forgeauth.GitLab, Host: host, Credential: "wrong"},
		{Kind: forgeauth.GitLab, Host: host},
		{Kind: "svn", Host: host, Credential: goodToken},
		{Kind: forgeauth.GitLab, Host: "ftp://example.com", Credential: goodToken},
	} {
		if _, err := c.AddConnection(ctx, bad); !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "wrong") {
			t.Fatalf("AddConnection(%+v) = %v", bad, err)
		}
	}
	if _, err := c.AddConnection(ctx, NewConnection{Kind: forgeauth.GitLab, Host: host + "/other", Credential: "wrong"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if forges, _ := m.ListForges(ctx); len(forges) != 1 {
		t.Fatalf("failed add left a forge: %+v", forges)
	}

	if _, err := c.RotateCredential(ctx, first.ID, "wrong"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rejected rotation = %v", err)
	}
	if _, err := c.RotateCredential(ctx, first.ID, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty rotation = %v", err)
	}
	if got, err := c.RotateCredential(ctx, first.ID, newToken); err != nil || got.Status != broker.StatusOK || got.BaseURL == "" {
		t.Fatalf("RotateCredential = %+v, %v", got, err)
	}
	if got, err := c.TestConnection(ctx, first.ID); err != nil || got.Status != broker.StatusOK {
		t.Fatalf("TestConnection = %+v, %v", got, err)
	}
	valid.Store([]string{newToken})
	if _, err := c.TestConnection(ctx, second.ID); !errors.Is(err, ErrCheckFailed) {
		t.Fatalf("failed TestConnection = %v", err)
	}
	list, err := c.ListConnections(ctx)
	if err != nil || len(list.Connections) != 2 {
		t.Fatalf("ListConnections = %+v, %v", list, err)
	}
	for _, conn := range list.Connections {
		if want := map[string]broker.Status{first.ID: broker.StatusOK, second.ID: broker.StatusRevoked}[conn.ID]; conn.Status != want {
			t.Fatalf("%s status = %s, want %s", conn.ID, conn.Status, want)
		}
	}

	for _, id := range []string{"", "missing"} {
		if _, err := c.TestConnection(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("TestConnection(%q) = %v", id, err)
		}
		if err := c.RemoveConnection(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("RemoveConnection(%q) = %v", id, err)
		}
	}
	if err := c.RemoveConnection(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if forges, _ := m.ListForges(ctx); len(forges) != 1 {
		t.Fatal("forge in use was deleted")
	}
	if err := c.RemoveConnection(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if forges, _ := m.ListForges(ctx); len(forges) != 0 {
		t.Fatal("unused forge was kept")
	}
}

func TestDefaultHosts(t *testing.T) {
	for kind, want := range map[forgeauth.Kind]string{
		forgeauth.GitHub: "https://api.github.com", forgeauth.GitLab: "https://gitlab.com/api/v4", forgeauth.Forgejo: "https://codeberg.org/api/v1",
	} {
		var got string
		admin := &fakeAdmin{ConnectorAdmin: nil, createForge: func(_ forgeauth.Kind, baseURL string) { got = baseURL }}
		_, _ = New(Config{Connector: admin}).AddConnection(context.Background(), NewConnection{Kind: kind, Credential: "token"})
		if got != want {
			t.Errorf("%s base URL = %q, want %q", kind, got, want)
		}
	}
}

// fakeAdmin records forge creation and fails every other call.
type fakeAdmin struct {
	ConnectorAdmin
	createForge func(forgeauth.Kind, string)
}

func (f *fakeAdmin) ListForges(context.Context) ([]broker.Forge, error) { return nil, nil }
func (f *fakeAdmin) CreateForge(_ context.Context, kind forgeauth.Kind, baseURL, _ string) (broker.Forge, error) {
	f.createForge(kind, baseURL)
	return broker.Forge{}, errors.New("upstream-secret")
}

func TestConnectorErrors(t *testing.T) {
	ctx := context.Background()
	unconfigured := New(Config{})
	if got, err := unconfigured.ListConnections(ctx); err != nil || got.Configured || got.Connections == nil {
		t.Fatalf("unconfigured ListConnections = %+v, %v", got, err)
	}
	for name, err := range map[string]error{
		"add":    func() error { _, err := unconfigured.AddConnection(ctx, NewConnection{}); return err }(),
		"test":   func() error { _, err := unconfigured.TestConnection(ctx, "id"); return err }(),
		"rotate": func() error { _, err := unconfigured.RotateCredential(ctx, "id", "token"); return err }(),
		"remove": unconfigured.RemoveConnection(ctx, "id"),
		"ensure": unconfigured.EnsureClient(ctx, "proxy", true, "token"),
	} {
		if !errors.Is(err, ErrNotConfigured) {
			t.Errorf("%s = %v", name, err)
		}
	}
	failing := New(Config{Connector: &fakeAdmin{createForge: func(forgeauth.Kind, string) {}}})
	_, err := failing.AddConnection(ctx, NewConnection{Kind: forgeauth.GitHub, Credential: "token"})
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("upstream failure = %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	m, _, _ := connector(t)
	if _, err := New(Config{Connector: m}).ListConnections(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled = %v", err)
	}
}

func TestEnsureClient(t *testing.T) {
	ctx := context.Background()
	m, host, _ := connector(t)
	c := New(Config{Connector: m})
	token := forgeauth.StaticToken(strings.Repeat("p", broker.MinClientTokenLength))
	for _, bad := range []struct {
		name  string
		token forgeauth.StaticToken
	}{{"", token}, {"proxy", token[1:]}} {
		if err := c.EnsureClient(ctx, bad.name, true, bad.token); !errors.Is(err, ErrInvalid) {
			t.Fatalf("EnsureClient(%q) = %v", bad.name, err)
		}
	}
	if err := c.EnsureClient(ctx, "proxy", true, token); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureClient(ctx, "proxy", true, token); err != nil {
		t.Fatal(err)
	}
	if clients, _ := m.ListClients(ctx); len(clients) != 1 || !clients[0].AllConnections || !clients[0].CanIssueTokens {
		t.Fatalf("clients = %+v", clients)
	}
	conn, err := c.AddConnection(ctx, NewConnection{Kind: forgeauth.GitLab, Host: host, Credential: goodToken})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.IssueToken(ctx, token, broker.ID(conn.ID)); err != nil {
		t.Fatalf("client cannot use a later connection: %v", err)
	}
	rotated := token + "-rotated"
	if err := c.EnsureClient(ctx, "proxy", true, rotated); err != nil {
		t.Fatal(err)
	}
	if _, err := m.IssueToken(ctx, token, broker.ID(conn.ID)); !errors.Is(err, broker.ErrUnauthorized) {
		t.Fatalf("old token still valid: %v", err)
	}
	if _, err := m.IssueToken(ctx, rotated, broker.ID(conn.ID)); err != nil {
		t.Fatalf("rotated token rejected: %v", err)
	}
	if clients, _ := m.ListClients(ctx); len(clients) != 1 {
		t.Fatalf("old client kept: %+v", clients)
	}
}
