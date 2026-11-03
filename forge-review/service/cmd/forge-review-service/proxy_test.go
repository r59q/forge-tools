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
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-review/library/review"
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
		{
			ID:     "fj",
			Kind:   forgeauth.Forgejo,
			Status: broker.StatusOK,
		},
		{
			ID:     "gh",
			Kind:   forgeauth.GitHub,
			Status: broker.StatusOK,
		},
		{
			ID:     "gl",
			Kind:   forgeauth.GitLab,
			Status: broker.StatusOK,
		},
	}, nil
}

func TestSourcesThroughProxy(t *testing.T) {
	const head = "2222222222222222222222222222222222222222"
	const base = "1111111111111111111111111111111111111111"
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		valid := r.Header.Get("Authorization") == "Bearer forge"
		if strings.HasPrefix(r.URL.Path, "/api/v4/") {
			valid = r.Header.Get("Private-Token") == "forge"
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			valid = r.Header.Get("Authorization") == "token forge"
		}
		if !valid {
			http.Error(w, "fixture authorization failed", http.StatusUnauthorized)
			return
		}

		if strings.HasSuffix(r.URL.Path, ".diff") ||
			strings.HasSuffix(r.URL.Path, "/raw_diffs") || r.Header.Get("Accept") == "application/vnd.github.diff" {
			fmt.Fprint(w, "--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n")
			return
		}

		if strings.HasPrefix(r.URL.Path, "/api/v4/") {
			fmt.Fprintf(w, `{"diff_refs":{"base_sha":%q,"head_sha":%q,"start_sha":%q}}`, base, head, base)
			return
		}

		fmt.Fprintf(w, `{"head":{"sha":%q},"base":{"sha":%q}}`, head, base)
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

	handler, err := proxy.New(proxy.Config{
		Verifier:    stub,
		TokenIssuer: stub,
		Connections: stub,
		Registry:    reg,
	})
	if err != nil {
		t.Fatal(err)
	}

	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()
	conn, err := connector.New(connector.Config{URL: proxyServer.URL, Credentials: forgeauth.StaticToken("client")})
	if err != nil {
		t.Fatal(err)
	}

	discovered, err := sources(conn)(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	client, err := review.New(&review.MemoryStore{}, discovered...)
	if err != nil {
		t.Fatal(err)
	}

	for _, source := range discovered {
		snapshot, err := client.CreateSubject(context.Background(), review.Input{Kind: "change_request", Ref: review.Ref{
			Source:     source.ID,
			Repository: "owner/repo",
			Number:     1,
		}})
		if err != nil || snapshot.Revision.Head != head || len(snapshot.Files) != 1 {
			t.Fatalf("%s: %+v %v", source.ID, snapshot, err)
		}
	}
}
