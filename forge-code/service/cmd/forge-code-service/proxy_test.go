package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/code"
	"github.com/r59q/forge-tools/forge-code/service/api"
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

// Commits the forges point main at.
const (
	ghMain = "1111111111111111111111111111111111111111"
	glMain = "2222222222222222222222222222222222222222"
	fjMain = "3333333333333333333333333333333333333333"
	ghTree = "4444444444444444444444444444444444444444"
	ghBlob = "5555555555555555555555555555555555555555"
)

func TestSourcesThroughProxy(t *testing.T) {
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gh := r.Header.Get("Authorization") == "Bearer forge"
		switch {
		case gh && r.URL.Path == "/repos/mrbean/teddy":
			fmt.Fprint(w, `{"default_branch":"main"}`)
		case gh && r.URL.Path == "/repos/mrbean/teddy/git/ref/heads/main":
			fmt.Fprintf(w, `{"ref":"refs/heads/main","object":{"type":"commit","sha":"%s"}}`, ghMain)
		case gh && r.URL.Path == "/repos/mrbean/teddy/git/commits/"+ghMain:
			fmt.Fprintf(w, `{"sha":"%s","tree":{"sha":"%s"},"parents":[],"message":"m"}`, ghMain, ghTree)
		case gh && r.URL.Path == "/repos/mrbean/teddy/git/trees/"+ghTree:
			fmt.Fprintf(w, `{"sha":"%s","tree":[{"path":"README.md","mode":"100644","type":"blob","sha":"%s","size":6}]}`, ghTree, ghBlob)
		case gh && r.URL.Path == "/repos/mrbean/teddy/git/blobs/"+ghBlob && r.Header.Get("Accept") == "application/vnd.github.raw+json":
			w.Header().Set("Content-Type", "application/octet-stream")
			fmt.Fprint(w, "hello\n")
		case r.URL.EscapedPath() == "/api/v4/projects/mrbean%2Ftools%2Fcli/repository/branches/main" && r.Header.Get("Private-Token") == "forge":
			fmt.Fprintf(w, `{"name":"main","commit":{"id":"%s"}}`, glMain)
		case r.URL.Path == "/api/v1/repos/mrbean/garden/git/refs/heads/main" && r.Header.Get("Authorization") == "token forge":
			fmt.Fprintf(w, `[{"ref":"refs/heads/main","object":{"type":"commit","sha":"%s"}}]`, fjMain)
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

	ctx := context.Background()
	client, err := sources(conn)(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tc := range []struct{ source, repo string }{{"fj", "mrbean/garden"}, {"gh", "mrbean/teddy"}, {"gl", "mrbean/tools/cli"}} {
		res, err := client.Resolve(ctx, code.RepositoryRef{Source: tc.source, Repository: tc.repo}, "refs/heads/main")
		if err != nil {
			t.Fatalf("%s: %v", tc.source, err)
		}
		got = append(got, tc.source+":"+string(res.Commit)[:1])
	}
	if want := "fj:3 gh:1 gl:2"; strings.Join(got, " ") != want {
		t.Fatalf("resolved %q, want %q", got, want)
	}
	for _, s := range client.Sources() {
		if s.Git == nil || !strings.HasPrefix(s.Git.BaseURL, p.URL+"/g/") {
			t.Errorf("source %s has no Git remote through the proxy: %+v", s.ID, s.Git)
		}
	}

	// A raw download through the service and the proxy.
	service, err := api.New(sources(conn), forgeauth.StaticToken("service"), api.Config{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	rest, err := service.REST(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/v1/raw?source=gh&repository=mrbean/teddy&path=README.md", nil)
	r.Header.Set("Authorization", "Bearer service")
	w := httptest.NewRecorder()
	rest.ServeHTTP(w, r)
	body, _ := io.ReadAll(w.Body)
	if w.Code != 200 || string(body) != "hello\n" || w.Header().Get("X-Forge-Code-Commit") != ghMain || w.Header().Get("X-Forge-Code-Blob") != ghBlob {
		t.Fatalf("raw through proxy: %d %q %v", w.Code, body, w.Header())
	}
}
