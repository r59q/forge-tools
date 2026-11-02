package forgeauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func newTestRemote(t *testing.T, base string) *GitRemote {
	t.Helper()
	g, err := NewGitRemote(base, StaticToken(secret), setTestToken, nil)
	if err != nil {
		t.Fatalf("NewGitRemote() error = %v", err)
	}
	return g
}

func TestGitRemoteURL(t *testing.T) {
	g := newTestRemote(t, "https://proxy.example/g/conn-1/")
	if g.BaseURL != "https://proxy.example/g/conn-1" {
		t.Errorf("BaseURL = %q", g.BaseURL)
	}
	good := map[string]string{
		"mrbean/teddy":         "https://proxy.example/g/conn-1/mrbean/teddy.git",
		" /group/sub/project/": "https://proxy.example/g/conn-1/group/sub/project.git",
	}
	for in, want := range good {
		if got, err := g.URL(in); err != nil || got != want {
			t.Errorf("URL(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	for _, p := range []string{"", "solo", "a//b", "a/../b", "a/./b", "a/b%2Fc", "a/b?x", "a/b#x", `a\b/c`, "a/b c", "a/b\x00"} {
		if got, err := g.URL(p); err == nil {
			t.Errorf("URL(%q) = %q, want an error", p, got)
		}
	}
	var none *GitRemote
	if _, err := none.URL("a/b"); err == nil {
		t.Error("nil remote URL err = nil")
	}
}

func TestNewGitRemoteErrors(t *testing.T) {
	tests := map[string]struct {
		base     string
		creds    Credentials
		setToken func(http.Header, string)
	}{
		"nil creds":    {"https://h", nil, setTestToken},
		"nil setToken": {"https://h", StaticToken(secret), nil},
		"empty":        {"", StaticToken(secret), setTestToken},
		"scheme":       {"ftp://h", StaticToken(secret), setTestToken},
		"user info":    {"https://u:p@h", StaticToken(secret), setTestToken},
		"query":        {"https://h?x=1", StaticToken(secret), setTestToken},
	}
	for name, tt := range tests {
		if _, err := NewGitRemote(tt.base, tt.creds, tt.setToken, nil); err == nil {
			t.Errorf("%s: err = nil", name)
		}
	}
}

func gitGet(t *testing.T, g *GitRemote, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := g.Client.Do(req)
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return resp, err
}

func TestGitRemoteTokenAndRedirects(t *testing.T) {
	other := newRecorder(t, nil)
	srv := newRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/g/1/o/r.git/away":
			http.Redirect(w, r, other.URL+"/g/1/o/r.git/info/refs", http.StatusFound)
		case "/g/1/o/r.git/sibling":
			http.Redirect(w, r, "/g/2/o/r.git/info/refs", http.StatusFound)
		case "/g/1/o/r.git/dots":
			http.Redirect(w, r, "/g/1/%2e%2e/2/o/r.git", http.StatusFound)
		case "/g/1/o/r.git/post":
			http.Redirect(w, r, "/g/1/n/r.git/info/refs", http.StatusFound)
		case "/g/1/o/r.git/info/refs":
			http.Redirect(w, r, "/g/1/n/r.git/info/refs", http.StatusMovedPermanently)
		}
	})
	g := newTestRemote(t, srv.URL+"/g/1")
	repo, err := g.URL("o/r")
	if err != nil {
		t.Fatal(err)
	}

	// A rename redirect within the remote is followed, with the token.
	if _, err := gitGet(t, g, repo+"/info/refs?service=git-receive-pack"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/g/1/o/r.git/info/refs", "/g/1/n/r.git/info/refs"} {
		if got := srv.seen(p); len(got) != 1 || got[0] != secret {
			t.Errorf("%s tokens = %q", p, got)
		}
	}

	for _, name := range []string{"away", "sibling", "dots"} {
		if _, err := gitGet(t, g, repo+"/"+name); err == nil || !strings.Contains(err.Error(), "leaves the Git remote") {
			t.Errorf("%s: err = %v, want a redirect error", name, err)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, repo+"/post", strings.NewReader("x"))
	if resp, err := g.Client.Do(req); err == nil || !strings.Contains(err.Error(), "redirected") {
		if resp != nil {
			resp.Body.Close()
		}
		t.Errorf("POST 302: err = %v, want a method change error", err)
	}
	if got := srv.seen("/g/2/o/r.git/info/refs"); len(got) != 0 {
		t.Errorf("followed a redirect to another connection: %q", got)
	}

	// An absolute URL to another origin is sent without the token.
	if _, err := gitGet(t, g, other.URL+"/direct"); err != nil {
		t.Fatal(err)
	}
	if got := other.seen("/direct"); len(got) != 1 || got[0] != "" {
		t.Errorf("other origin tokens = %q, want none", got)
	}
	if got := other.seen("/g/1/o/r.git/info/refs"); len(got) != 0 {
		t.Errorf("redirect reached the other origin: %q", got)
	}
}

func TestGitRemoteTokenError(t *testing.T) {
	srv := newRecorder(t, nil)
	g, err := NewGitRemote(srv.URL, EnvToken("FORGEAUTH_TEST_UNSET_GIT_TOKEN"), setTestToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitGet(t, g, srv.URL+"/o/r.git/info/refs"); !errors.Is(err, ErrNoToken) {
		t.Errorf("err = %v, want ErrNoToken", err)
	}
	if got := srv.seen("/o/r.git/info/refs"); len(got) != 0 {
		t.Errorf("sent a request without a token: %q", got)
	}
}

func TestDefaultGitURL(t *testing.T) {
	good := []struct {
		kind Kind
		api  string
		want string
	}{
		{GitHub, "", "https://github.com"},
		{GitHub, "https://api.github.com", "https://github.com"},
		{GitHub, "https://API.GitHub.com/", "https://github.com"},
		{GitHub, "https://api.acme.ghe.com", "https://acme.ghe.com"},
		{GitHub, "https://ghes.example/api/v3", "https://ghes.example"},
		{GitHub, "https://ghes.example:8443/sub/api/v3/", "https://ghes.example:8443/sub"},
		{GitLab, "https://gitlab.com/api/v4", "https://gitlab.com"},
		{GitLab, "http://gl.example/root/api/v4", "http://gl.example/root"},
		{Forgejo, "https://codeberg.org/api/v1", "https://codeberg.org"},
		{Forgejo, "https://f.example/git/api/v1/", "https://f.example/git"},
	}
	for _, tt := range good {
		if got, err := DefaultGitURL(tt.kind, tt.api); err != nil || got != tt.want {
			t.Errorf("DefaultGitURL(%s, %q) = %q, %v, want %q", tt.kind, tt.api, got, err, tt.want)
		}
	}
	bad := []struct {
		kind Kind
		api  string
	}{
		{GitHub, "https://ghes.example"},
		{GitHub, "https://api.a.b.ghe.com"},
		{GitHub, "https://api.ghe.com"},
		{GitHub, "https://ghes.example/xapi/v3"},
		{GitHub, "https://api.github.com/extra"},
		{GitLab, "https://gitlab.com"},
		{GitLab, "https://gitlab.com/api/v3"},
		{GitLab, ""},
		{Forgejo, "https://codeberg.org/api/v4"},
		{Forgejo, "https://u:p@codeberg.org/api/v1"},
		{"gitea", "https://g.example/api/v1"},
	}
	for _, tt := range bad {
		if got, err := DefaultGitURL(tt.kind, tt.api); err == nil {
			t.Errorf("DefaultGitURL(%s, %q) = %q, want an error", tt.kind, tt.api, got)
		}
	}
	if _, err := DefaultGitURL(GitLab, "https://gitlab.com"); !errors.Is(err, ErrNoGitURL) {
		t.Errorf("err = %v, want ErrNoGitURL", err)
	}
}
