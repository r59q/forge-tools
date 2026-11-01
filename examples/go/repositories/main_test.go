package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-connector/library/proxy"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
)

const (
	clientToken = "client-secret"
	forgeToken  = "forge-secret"
)

// fakeBroker stands in for the broker adapters a proxy service wires up.
type fakeBroker map[broker.ID]forgeauth.Connection

func (b fakeBroker) Connection(_ context.Context, id broker.ID) (forgeauth.Connection, error) {
	c, ok := b[id]
	if !ok {
		return forgeauth.Connection{}, broker.ErrNotFound
	}
	return c, nil
}

func (b fakeBroker) VerifyClient(_ context.Context, token forgeauth.StaticToken, id broker.ID) (broker.Client, error) {
	if _, ok := b[id]; !ok || string(token) != clientToken {
		return broker.Client{}, broker.ErrUnauthorized
	}
	return broker.Client{}, nil
}

func (b fakeBroker) IssueToken(_ context.Context, id broker.ID) (broker.IssuedToken, error) {
	c, ok := b[id]
	if !ok {
		return broker.IssuedToken{}, broker.ErrNotFound
	}
	return broker.IssuedToken{Kind: c.Kind, BaseURL: c.BaseURL, Token: forgeToken}, nil
}

// fakeForge serves fixed JSON for escaped API paths below prefix, after
// checking the forge token the proxy swapped in. Routes ending in "?page=2"
// are second pages linked from the first.
type fakeForge struct {
	srv    *httptest.Server
	prefix string
	routes map[string]string

	mu   sync.Mutex
	seen []string
}

func newForge(t *testing.T, prefix string, token func(http.Header) (string, bool), routes map[string]string) *fakeForge {
	t.Helper()
	f := &fakeForge{prefix: prefix, routes: routes}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, _ := token(r.Header); got != forgeToken {
			http.Error(w, `{"message":"bad credentials"}`, http.StatusUnauthorized)
			return
		}
		path := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
		if r.URL.Query().Get("page") == "2" {
			path += "?page=2"
		}
		f.mu.Lock()
		f.seen = append(f.seen, path)
		f.mu.Unlock()
		body, ok := routes[path]
		if !ok {
			http.Error(w, `{"message":"404 Not Found"}`, http.StatusNotFound)
			return
		}
		if _, ok := routes[path+"?page=2"]; ok {
			w.Header().Set("Link", fmt.Sprintf(`<%s%s?page=2>; rel="next"`, f.srv.URL, r.URL.EscapedPath()))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// setup runs the real forge-connector proxy handler in front of a fake
// GitHub and a fake GitLab, and returns the proxy URL.
func setup(t *testing.T) (proxyURL string, gh, gl *fakeForge) {
	t.Helper()
	gh = newForge(t, "", github.TokenFromHeader, map[string]string{
		"/user/repos":        `[{"id":1,"full_name":"mrbean/teddy","default_branch":"main","visibility":"public","html_url":"https://github.com/mrbean/teddy"}]`,
		"/user/repos?page=2": `[{"id":2,"full_name":"someone/else","visibility":"private","html_url":"https://github.com/someone/else"}]`,
	})
	gl = newForge(t, "/api/v4", gitlab.TokenFromHeader, map[string]string{
		"/projects":                      `[{"id":7,"path_with_namespace":"mrbean/tools/cli","default_branch":"main","visibility":"internal","web_url":"https://gitlab.com/mrbean/tools/cli"}]`,
		"/projects/mrbean%2Ftools%2Fcli": `{"id":7,"path_with_namespace":"mrbean/tools/cli","default_branch":"main","web_url":"https://gitlab.com/mrbean/tools/cli"}`,
		"/projects/mrbean%2Ftools%2Fcli/repository/branches":        `[{"name":"dev","commit":{"id":"d3v"}}]`,
		"/projects/mrbean%2Ftools%2Fcli/repository/branches?page=2": `[{"name":"main","commit":{"id":"m41n"}}]`,
		"/projects/mrbean%2Ftools%2Fcli/repository/tags":            `[{"name":"v1.0.0","commit":{"id":"t46"}}]`,
	})

	var reg forgeauth.Registry
	for _, p := range []forgeauth.Provider{github.Provider{}, gitlab.Provider{}} {
		if err := reg.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	b := fakeBroker{
		"gh-conn": {Kind: forgeauth.GitHub, BaseURL: gh.srv.URL},
		"gl-conn": {Kind: forgeauth.GitLab, BaseURL: gl.srv.URL + "/api/v4"},
	}
	h, err := proxy.New(proxy.Config{Verifier: b, TokenIssuer: b, Registry: &reg})
	if err != nil {
		t.Fatal(err)
	}
	p := httptest.NewServer(h)
	t.Cleanup(p.Close)
	return p.URL, gh, gl
}

// lines splits output into lines of whitespace-separated fields.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		out = append(out, strings.Join(strings.Fields(l), " "))
	}
	return out
}

func TestListThroughProxy(t *testing.T) {
	proxyURL, _, _ := setup(t)
	var out strings.Builder
	opts := options{Proxy: proxyURL, Sources: []string{"github:gh-conn", "gitlab:gl-conn"}, Owner: "mrbean"}
	if err := run(context.Background(), opts, forgeauth.StaticToken(clientToken), &out); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"github mrbean/teddy public https://github.com/mrbean/teddy",
		"gitlab mrbean/tools/cli internal https://gitlab.com/mrbean/tools/cli",
	}
	if got := lines(out.String()); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("output:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestListPagesThroughProxy(t *testing.T) {
	proxyURL, gh, _ := setup(t)
	var out strings.Builder
	opts := options{Proxy: proxyURL, Sources: []string{"github:gh-conn"}}
	if err := run(context.Background(), opts, forgeauth.StaticToken(clientToken), &out); err != nil {
		t.Fatal(err)
	}
	if got := lines(out.String()); len(got) != 2 || !strings.Contains(got[1], "someone/else") {
		t.Fatalf("output = %q, want both pages", got)
	}
	if strings.Join(gh.seen, " ") != "/user/repos /user/repos?page=2" {
		t.Fatalf("GitHub saw %v", gh.seen)
	}
}

func TestRefsNestedPathThroughProxy(t *testing.T) {
	proxyURL, gh, gl := setup(t)
	var out strings.Builder
	opts := options{Proxy: proxyURL, Sources: []string{"github:gh-conn", "gitlab:gl-conn"}, Refs: "mrbean/tools/cli"}
	if err := run(context.Background(), opts, forgeauth.StaticToken(clientToken), &out); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"gitlab mrbean/tools/cli https://gitlab.com/mrbean/tools/cli",
		"branch dev d3v",
		"branch main m41n default",
		"tag v1.0.0 t46",
	}
	if got := lines(out.String()); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("output:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(gh.seen) != 0 {
		t.Errorf("GitHub was asked for a nested GitLab path: %v", gh.seen)
	}
	if gl.seen[0] != "/projects/mrbean%2Ftools%2Fcli" {
		t.Errorf("GitLab saw %v, want one encoded path segment", gl.seen)
	}
}

func TestRejectedClientToken(t *testing.T) {
	proxyURL, _, _ := setup(t)
	opts := options{Proxy: proxyURL, Sources: []string{"github:gh-conn"}}
	if err := run(context.Background(), opts, forgeauth.StaticToken("wrong"), &strings.Builder{}); err == nil {
		t.Fatal("run succeeded with a rejected client token")
	}
}

func TestBadSource(t *testing.T) {
	for _, sources := range [][]string{nil, {"github"}, {"svn:conn"}} {
		opts := options{Proxy: "http://127.0.0.1:1", Sources: sources}
		if err := run(context.Background(), opts, forgeauth.StaticToken(clientToken), &strings.Builder{}); err == nil {
			t.Errorf("run(%q) succeeded", sources)
		}
	}
}
