package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-changes/library/changes"
	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-connector/library/connector"
	"github.com/r59q/forge-tools/forge-connector/library/proxy"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
)

// brokerStub stands in for the broker behind a real proxy handler. It knows
// one client token and serves GitHub, GitLab and Forgejo connections.
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
		{ID: "fj", Kind: forgeauth.Forgejo, Status: broker.StatusOK},
		{ID: "gh", Kind: forgeauth.GitHub, Status: broker.StatusOK},
		{ID: "gl", Kind: forgeauth.GitLab, Status: broker.StatusOK},
	}, nil
}

func TestSourcesThroughProxy(t *testing.T) {
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/mrbean/teddy/pulls" && r.Header.Get("Authorization") == "Bearer forge":
			fmt.Fprint(w, `[{"id":1,"number":3,"title":"Lost","state":"open","html_url":"https://github.com/mrbean/teddy/pull/3","repository_url":"https://api.github.com/repos/mrbean/teddy"}]`)
		case r.URL.Path == "/api/v4/projects/mrbean/teddy/merge_requests" && r.Header.Get("Private-Token") == "forge":
			fmt.Fprint(w, `[{"id":2,"iid":7,"title":"Crash","state":"opened","web_url":"https://gitlab.com/mrbean/teddy/-/merge_requests/7","references":{"full":"mrbean/teddy!7"}}]`)
		case r.URL.Path == "/api/v1/repos/mrbean/teddy/pulls" && r.Header.Get("Authorization") == "token forge":
			fmt.Fprint(w, `[{"id":3,"number":5,"title":"Weeds","state":"open","html_url":"https://codeberg.org/mrbean/teddy/pulls/5","repository":{"full_name":"mrbean/teddy"}}]`)
		default:
			http.Error(w, `{"message":"no"}`, http.StatusUnauthorized)
		}
	}))
	defer forge.Close()
	stub := brokerStub{
		"fj": {Kind: forgeauth.Forgejo, BaseURL: forge.URL + "/api/v1"},
		"gh": {Kind: forgeauth.GitHub, BaseURL: forge.URL},
		"gl": {Kind: forgeauth.GitLab, BaseURL: forge.URL + "/api/v4"},
	}
	reg := &forgeauth.Registry{}
	for _, p := range []forgeauth.Provider{github.Provider{}, gitlab.Provider{}, forgejo.Provider{}} {
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
	list, err := client.List(context.Background(), changes.ListOptions{Repository: "mrbean/teddy"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, i := range list {
		got = append(got, fmt.Sprintf("%s:%s#%d", i.Ref.Source, i.Ref.Repository, i.Ref.Number))
	}
	if want := "fj:mrbean/teddy#5 gh:mrbean/teddy#3 gl:mrbean/teddy#7"; strings.Join(got, " ") != want {
		t.Fatalf("changes = %q, want %q", got, want)
	}
}
