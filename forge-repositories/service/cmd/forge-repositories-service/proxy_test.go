package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-connector/library/connector"
	"github.com/r59q/forge-tools/forge-connector/library/proxy"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-repositories/library/repositories"
)

// brokerStub stands in for the broker behind a real proxy handler. It knows
// one client token and serves GitHub and GitLab connections.
type brokerStub map[broker.ID]forgeauth.Connection

func (b brokerStub) Connection(_ context.Context, id broker.ID) (forgeauth.Connection, error) {
	if c, ok := b[id]; ok {
		return c, nil
	}
	return forgeauth.Connection{}, broker.ErrNotFound
}

func (b brokerStub) VerifyClient(_ context.Context, token forgeauth.StaticToken, id broker.ID) (broker.Client, error) {
	if token != "client" {
		return broker.Client{}, broker.ErrUnauthorized
	}
	return broker.Client{}, nil
}

func (b brokerStub) IssueToken(ctx context.Context, id broker.ID) (broker.IssuedToken, error) {
	c, err := b.Connection(ctx, id)
	return broker.IssuedToken{Kind: c.Kind, BaseURL: c.BaseURL, Token: "forge"}, err
}

func (b brokerStub) ClientConnections(_ context.Context, token forgeauth.StaticToken) ([]broker.ClientConnection, error) {
	if token != "client" {
		return nil, broker.ErrUnauthorized
	}
	return []broker.ClientConnection{
		{ID: "gh", Kind: forgeauth.GitHub, Status: broker.StatusOK},
		{ID: "gl", Kind: forgeauth.GitLab, Status: broker.StatusOK},
	}, nil
}

func TestSourcesThroughProxy(t *testing.T) {
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user/repos" && r.Header.Get("Authorization") == "Bearer forge":
			fmt.Fprint(w, `[{"id":1,"full_name":"mrbean/teddy","html_url":"https://github.com/mrbean/teddy"}]`)
		case r.URL.Path == "/api/v4/projects" && r.Header.Get("Private-Token") == "forge":
			fmt.Fprint(w, `[{"id":2,"path_with_namespace":"mrbean/tools/cli","web_url":"https://gitlab.com/mrbean/tools/cli"}]`)
		default:
			http.Error(w, `{"message":"no"}`, http.StatusUnauthorized)
		}
	}))
	defer forge.Close()
	stub := brokerStub{
		"gh": {Kind: forgeauth.GitHub, BaseURL: forge.URL},
		"gl": {Kind: forgeauth.GitLab, BaseURL: forge.URL + "/api/v4"},
	}
	reg := &forgeauth.Registry{}
	for _, p := range []forgeauth.Provider{github.Provider{}, gitlab.Provider{}} {
		if err := reg.Register(p); err != nil {
			t.Fatal(err)
		}
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

	client, err := sources(conn)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	repos, err := client.List(context.Background(), repositories.ListOptions{Owner: "mrbean"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range repos {
		got = append(got, r.Source.ID+":"+r.Path+"@"+r.Source.Host)
	}
	if want := "gh:mrbean/teddy@github.com gl:mrbean/tools/cli@gitlab.com"; strings.Join(got, " ") != want {
		t.Fatalf("repositories = %q, want %q", got, want)
	}
}
