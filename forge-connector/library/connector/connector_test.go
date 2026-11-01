package connector_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-connector/library/connector"
	"github.com/r59q/forge-tools/forge-connector/library/proxy"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

const (
	clientToken = "client-secret"
	forgeToken  = "forge-secret"
)

// fakeBroker backs a real proxy handler. It knows one client token.
type fakeBroker struct {
	forges      map[broker.ID]forgeauth.Connection
	connections []broker.ClientConnection
}

func (b fakeBroker) Connection(_ context.Context, id broker.ID) (forgeauth.Connection, error) {
	if c, ok := b.forges[id]; ok {
		return c, nil
	}
	return forgeauth.Connection{}, broker.ErrNotFound
}

func (b fakeBroker) VerifyClient(_ context.Context, token forgeauth.StaticToken, _ broker.ID) (broker.Client, error) {
	if token != clientToken {
		return broker.Client{}, broker.ErrUnauthorized
	}
	return broker.Client{}, nil
}

func (b fakeBroker) IssueToken(ctx context.Context, id broker.ID) (broker.IssuedToken, error) {
	c, err := b.Connection(ctx, id)
	return broker.IssuedToken{Kind: c.Kind, BaseURL: c.BaseURL, Token: forgeToken}, err
}

func (b fakeBroker) ClientConnections(_ context.Context, token forgeauth.StaticToken) ([]broker.ClientConnection, error) {
	if token != clientToken {
		return nil, broker.ErrUnauthorized
	}
	return b.connections, nil
}

// setup runs a fake forge behind a real proxy handler and returns the proxy URL.
func setup(t *testing.T) string {
	t.Helper()
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization") + r.Header.Get("Private-Token")
		if auth != "Bearer "+forgeToken && auth != forgeToken {
			http.Error(w, `{"message":"no"}`, http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"path":%q}`, r.URL.Path)
	}))
	t.Cleanup(forge.Close)
	b := fakeBroker{
		forges: map[broker.ID]forgeauth.Connection{
			"gh": {Kind: forgeauth.GitHub, BaseURL: forge.URL},
			"gl": {Kind: forgeauth.GitLab, BaseURL: forge.URL + "/api/v4"},
		},
		connections: []broker.ClientConnection{
			{ID: "gh", Name: "me", Kind: forgeauth.GitHub, Status: broker.StatusOK},
			{ID: "gl", Kind: forgeauth.GitLab, Status: broker.StatusOK},
			{ID: "old", Kind: forgeauth.GitHub, Status: broker.StatusRevoked},
			{ID: "svn", Kind: "svn", Status: broker.StatusOK},
		},
	}
	reg := &forgeauth.Registry{}
	for _, p := range []forgeauth.Provider{github.Provider{}, gitlab.Provider{}, forgejo.Provider{}} {
		if err := reg.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	h, err := proxy.New(proxy.Config{Verifier: b, TokenIssuer: b, Connections: b, Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	p := httptest.NewServer(h)
	t.Cleanup(p.Close)
	return p.URL
}

func TestConnections(t *testing.T) {
	proxyURL := setup(t)
	c, err := connector.New(connector.Config{URL: proxyURL + "/", Credentials: forgeauth.StaticToken(clientToken)})
	if err != nil {
		t.Fatal(err)
	}
	conns, err := c.Connections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, conn := range conns {
		// The session reaches the forge with the forge token, not the client token.
		var body struct{ Path string }
		if err := conn.Session.GetJSON(context.Background(), "/user", &body); err != nil {
			t.Fatalf("%s: %v", conn.ID, err)
		}
		got = append(got, fmt.Sprintf("%s %s %q %s", conn.ID, conn.Kind, conn.Name, body.Path))
	}
	if want := `gh github "me" /user, gl gitlab "" /api/v4/user`; strings.Join(got, ", ") != want {
		t.Fatalf("connections = %q, want %q", got, want)
	}
}

func TestConnectionsErrors(t *testing.T) {
	proxyURL := setup(t)
	c, _ := connector.New(connector.Config{URL: proxyURL, Credentials: forgeauth.StaticToken("wrong-secret")})
	if _, err := c.Connections(context.Background()); !errors.Is(err, forgehttp.ErrUnauthorized) || strings.Contains(err.Error(), "wrong-secret") {
		t.Fatalf("wrong token: %v", err)
	}
	c, _ = connector.New(connector.Config{URL: proxyURL, Credentials: forgeauth.EnvToken("CONNECTOR_TEST_UNSET")})
	if _, err := c.Connections(context.Background()); !errors.Is(err, forgeauth.ErrNoToken) {
		t.Fatalf("missing token: %v", err)
	}
	for _, u := range []string{"", "ftp://proxy", "http://user:pass@proxy", "http://proxy?x=1", "http://proxy#f"} {
		if _, err := connector.New(connector.Config{URL: u, Credentials: forgeauth.StaticToken(clientToken)}); !errors.Is(err, broker.ErrInvalid) {
			t.Errorf("URL %q: %v", u, err)
		}
	}
	if _, err := connector.New(connector.Config{URL: proxyURL}); !errors.Is(err, broker.ErrInvalid) {
		t.Error("missing credentials accepted")
	}
}
