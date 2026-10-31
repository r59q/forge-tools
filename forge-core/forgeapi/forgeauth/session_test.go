package forgeauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// testHeader is a custom header, so the tests check this package's origin
// guard rather than net/http's own Authorization stripping.
const testHeader = "X-Test-Token"

func setTestToken(h http.Header, tok string) { h.Set(testHeader, tok) }

func openTest(t *testing.T, base string, creds Credentials, rt http.RoundTripper) *Session {
	t.Helper()
	s, err := OpenSession(context.Background(), GitHub, Connection{BaseURL: base, Transport: rt}, creds, setTestToken)
	if err != nil {
		t.Fatalf("OpenSession() error = %v", err)
	}
	return s
}

// recorder is an httptest server that records the token of each request.
type recorder struct {
	*httptest.Server
	mu     sync.Mutex
	tokens map[string][]string // path -> tokens seen
}

func newRecorder(t *testing.T, h http.HandlerFunc) *recorder {
	r := &recorder{tokens: map[string][]string{}}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.tokens[req.URL.Path] = append(r.tokens[req.URL.Path], req.Header.Get(testHeader))
		r.mu.Unlock()
		if h != nil {
			h(w, req)
		}
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *recorder) seen(path string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tokens[path]
}

func get(t *testing.T, s *Session, path string) *http.Response {
	t.Helper()
	req, err := s.NewRequest(context.Background(), http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func TestTokenSentToOwnHost(t *testing.T) {
	srv := newRecorder(t, nil)
	s := openTest(t, srv.URL+"/api/v1/", StaticToken(secret), nil)
	if s.BaseURL != srv.URL+"/api/v1" {
		t.Errorf("BaseURL = %q", s.BaseURL)
	}
	get(t, s, "/user")
	get(t, s, srv.URL+"/absolute")
	if got := srv.seen("/api/v1/user"); len(got) != 1 || got[0] != secret {
		t.Errorf("relative request tokens = %q", got)
	}
	if got := srv.seen("/absolute"); len(got) != 1 || got[0] != secret {
		t.Errorf("absolute same-host request tokens = %q", got)
	}
}

func TestTokenNotSentToOtherHost(t *testing.T) {
	other := newRecorder(t, nil)
	srv := newRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, other.URL+"/landed", http.StatusFound)
		case "/away307":
			http.Redirect(w, r, other.URL+"/landed307", http.StatusTemporaryRedirect)
		case "/here":
			http.Redirect(w, r, "/stayed", http.StatusFound)
		case "/bounce":
			http.Redirect(w, r, other.URL+"/back", http.StatusFound)
		}
	})
	// other redirects /back to srv again; srv's own origin gets the token
	// again, the other host never does.
	other.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other.mu.Lock()
		other.tokens[r.URL.Path] = append(other.tokens[r.URL.Path], r.Header.Get(testHeader))
		other.mu.Unlock()
		if r.URL.Path == "/back" {
			http.Redirect(w, r, srv.URL+"/returned", http.StatusFound)
		}
	})

	s := openTest(t, srv.URL, StaticToken(secret), nil)

	t.Run("absolute URL", func(t *testing.T) {
		get(t, s, other.URL+"/direct")
		if got := other.seen("/direct"); len(got) != 1 || got[0] != "" {
			t.Errorf("other host got tokens %q", got)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		get(t, s, "/away")
		if got := srv.seen("/away"); len(got) != 1 || got[0] != secret {
			t.Errorf("own host tokens = %q", got)
		}
		if got := other.seen("/landed"); len(got) != 1 || got[0] != "" {
			t.Errorf("other host got tokens %q", got)
		}
	})
	t.Run("307 redirect", func(t *testing.T) {
		req, _ := s.NewRequest(context.Background(), http.MethodPost, "/away307", strings.NewReader("body"))
		resp, err := s.Client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := other.seen("/landed307"); len(got) != 1 || got[0] != "" {
			t.Errorf("other host got tokens %q", got)
		}
	})
	t.Run("same host redirect", func(t *testing.T) {
		get(t, s, "/here")
		if got := srv.seen("/stayed"); len(got) != 1 || got[0] != secret {
			t.Errorf("same host redirect tokens = %q", got)
		}
	})
	t.Run("redirect back", func(t *testing.T) {
		get(t, s, "/bounce")
		if got := other.seen("/back"); len(got) != 1 || got[0] != "" {
			t.Errorf("other host got tokens %q", got)
		}
		if got := srv.seen("/returned"); len(got) != 1 || got[0] != secret {
			t.Errorf("returned tokens = %q", got)
		}
	})
	t.Run("caller request unchanged", func(t *testing.T) {
		req, _ := s.NewRequest(context.Background(), http.MethodGet, "/x", nil)
		resp, err := s.Client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if v := req.Header.Get(testHeader); v != "" {
			t.Errorf("caller's request has token header %q", v)
		}
	})
}

// fakeTransport answers without a network. A request with a "to" query
// parameter is redirected there.
type fakeTransport struct {
	mu   sync.Mutex
	seen map[string]string // URL -> token
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.seen[req.URL.String()] = req.Header.Get(testHeader)
	f.mu.Unlock()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody, Request: req}
	if to := req.URL.Query().Get("to"); to != "" {
		resp.StatusCode = http.StatusFound
		resp.Header.Set("Location", to)
	}
	return resp, nil
}

func TestTokenOriginRules(t *testing.T) {
	tests := []struct {
		target string
		want   bool
	}{
		{"https://git.example.com/api/v1/user", true},
		{"https://GIT.Example.COM/other", true},
		{"https://git.example.com:443/x", true},
		{"http://git.example.com/x", false},
		{"https://git.example.com:8443/x", false},
		{"https://api.git.example.com/x", false},
		{"https://example.com/x", false},
		{"https://git.example.com.evil.test/x", false},
		{"https://evil.test/git.example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			for _, via := range []string{"direct", "redirect"} {
				ft := &fakeTransport{seen: map[string]string{}}
				s := openTest(t, "https://git.example.com/api/v1", StaticToken(secret), ft)
				path := tt.target
				if via == "redirect" {
					path = "/redirect?to=" + url.QueryEscape(tt.target)
				}
				resp := get(t, s, path)
				if resp.Request.URL.String() != tt.target {
					t.Fatalf("%s: final URL = %s", via, resp.Request.URL)
				}
				want := ""
				if tt.want {
					want = secret
				}
				if got := ft.seen[tt.target]; got != want {
					t.Errorf("%s: token sent = %q, want %q", via, got, want)
				}
			}
		})
	}
}

func TestOriginOf(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://example.com", "https://example.com:443"},
		{"HTTPS://Example.COM:443/x", "https://example.com:443"},
		{"http://example.com/x", "http://example.com:80"},
		{"http://example.com:80", "http://example.com:80"},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"https://[::1]/x", "https://[::1]:443"},
		{"ftp://example.com", ""},
		{"/relative", ""},
	}
	for _, tt := range tests {
		u, err := url.Parse(tt.url)
		if err != nil {
			t.Fatal(err)
		}
		if got := originOf(u); got != tt.want {
			t.Errorf("originOf(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

// rotating returns the next token on each call.
type rotating struct {
	mu   sync.Mutex
	toks []string
}

func (r *rotating) Token(context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.toks) == 0 {
		return "", ErrNoToken
	}
	tok := r.toks[0]
	r.toks = r.toks[1:]
	return tok, nil
}

func TestTokenReadPerRequest(t *testing.T) {
	srv := newRecorder(t, nil)
	creds := &rotating{toks: []string{"open", "first", "second"}}
	s := openTest(t, srv.URL, creds, nil)
	get(t, s, "/a")
	get(t, s, "/a")
	if got := srv.seen("/a"); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Errorf("tokens = %q", got)
	}
	// Credentials now fail: the request is not sent.
	req, _ := s.NewRequest(context.Background(), http.MethodPost, "/a", io.NopCloser(strings.NewReader("x")))
	if _, err := s.Client.Do(req); !errors.Is(err, ErrNoToken) {
		t.Errorf("Do() error = %v, want ErrNoToken", err)
	}
	if got := srv.seen("/a"); len(got) != 2 {
		t.Errorf("request sent without token: %q", got)
	}
}

func TestTokenInvalidAtRequest(t *testing.T) {
	srv := newRecorder(t, nil)
	creds := &rotating{toks: []string{"ok", "bad\r\nX-Evil: 1"}}
	s := openTest(t, srv.URL, creds, nil)
	req, _ := s.NewRequest(context.Background(), http.MethodGet, "/a", nil)
	_, err := s.Client.Do(req)
	if err == nil || strings.Contains(err.Error(), "bad") {
		t.Errorf("Do() error = %v", err)
	}
	if got := srv.seen("/a"); len(got) != 0 {
		t.Errorf("request sent: %q", got)
	}
}

func TestOpenSessionErrors(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		kind  Kind
		conn  Connection
		creds Credentials
		want  string
	}{
		{"kind mismatch", GitHub, Connection{Kind: GitLab, BaseURL: "https://x"}, StaticToken("t"), "does not match"},
		{"nil credentials", GitHub, Connection{}, nil, "nil credentials"},
		{"no default", GitLab, Connection{Kind: GitLab}, StaticToken("t"), "base URL required"},
		{"bad scheme", Forgejo, Connection{BaseURL: "ftp://x"}, StaticToken("t"), "http or https"},
		{"no scheme", Forgejo, Connection{BaseURL: "codeberg.org/api/v1"}, StaticToken("t"), "http or https"},
		{"no host", Forgejo, Connection{BaseURL: "https:///api"}, StaticToken("t"), "no host"},
		{"user info", Forgejo, Connection{BaseURL: "https://user:" + secret + "@x/api"}, StaticToken("t"), "user info"},
		{"query", Forgejo, Connection{BaseURL: "https://x/api?token=" + secret}, StaticToken("t"), "query"},
		{"bad URL", Forgejo, Connection{BaseURL: "https://x/%zz?" + secret}, StaticToken("t"), "invalid base URL"},
		{"empty token", GitHub, Connection{}, StaticToken(""), "no token"},
		{"bad token", GitHub, Connection{}, StaticToken("a b"), "whitespace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := OpenSession(ctx, tt.kind, tt.conn, tt.creds, setTestToken)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("OpenSession() = %v, %v, want error containing %q", s, err, tt.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error leaks secret: %v", err)
			}
		})
	}
}

func TestOpenSessionDefaults(t *testing.T) {
	s, err := OpenSession(context.Background(), GitHub, Connection{}, StaticToken("t"), setTestToken)
	if err != nil {
		t.Fatal(err)
	}
	if s.Kind != GitHub || s.BaseURL != "https://api.github.com" || s.Client == nil {
		t.Errorf("session = %+v", s)
	}
}

func TestSessionFormatHidesToken(t *testing.T) {
	s := openTest(t, "https://x.test", StaticToken(secret), nil)
	out := fmt.Sprintf("%v %+v %#v %+v %#v %s", s, *s, s.Client.Transport, s.Client.Transport, *s.Client, s.Client.Transport)
	if strings.Contains(out, secret) || !strings.Contains(out, redacted) {
		t.Errorf("formatted session leaks token: %s", out)
	}
}

func TestSessionURL(t *testing.T) {
	s := &Session{BaseURL: "https://x.test/api/v4"}
	tests := map[string]string{
		"/user":                   "https://x.test/api/v4/user",
		"user":                    "https://x.test/api/v4/user",
		"projects?page=2":         "https://x.test/api/v4/projects?page=2",
		"https://y.test/a?page=2": "https://y.test/a?page=2",
		"HTTP://y.test/a":         "HTTP://y.test/a",
	}
	for in, want := range tests {
		if got := s.URL(in); got != want {
			t.Errorf("URL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGetJSON(t *testing.T) {
	srv := newRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			if r.Header.Get("Accept") != "application/json" {
				w.WriteHeader(http.StatusNotAcceptable)
				return
			}
			fmt.Fprint(w, `{"name":"x"}`)
		case "/bad":
			fmt.Fprint(w, `not json`)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
		}
	})
	s := openTest(t, srv.URL, StaticToken(secret), nil)
	ctx := context.Background()

	var v struct{ Name string }
	if err := s.GetJSON(ctx, "/ok", &v); err != nil || v.Name != "x" {
		t.Errorf("GetJSON(/ok) = %v, %+v", err, v)
	}
	if err := s.GetJSON(ctx, "/bad?x=1", &v); err == nil || !strings.Contains(err.Error(), "decode") || strings.Contains(err.Error(), "x=1") {
		t.Errorf("GetJSON(/bad) error = %v", err)
	}
	err := s.GetJSON(ctx, "/denied", &v)
	var herr *forgehttp.Error
	if !errors.Is(err, forgehttp.ErrUnauthorized) || !errors.As(err, &herr) || herr.Message != "Bad credentials" {
		t.Errorf("GetJSON(/denied) error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaks token: %v", err)
	}
}

func TestDefaultBaseURL(t *testing.T) {
	tests := []struct {
		kind    Kind
		host    string
		want    string
		wantErr bool
	}{
		{GitHub, "", "https://api.github.com", false},
		{GitHub, "github.com", "https://api.github.com", false},
		{GitHub, "https://GitHub.com/", "https://api.github.com", false},
		{GitHub, "https://ghe.example.com", "https://ghe.example.com/api/v3", false},
		{GitLab, "", "", true},
		{GitLab, "gitlab.com", "https://gitlab.com/api/v4", false},
		{GitLab, "http://localhost:8080/gitlab/", "http://localhost:8080/gitlab/api/v4", false},
		{Forgejo, "", "", true},
		{Forgejo, "codeberg.org", "https://codeberg.org/api/v1", false},
		{Forgejo, "https://git.example.com/", "https://git.example.com/api/v1", false},
		{Forgejo, "ftp://git.example.com", "", true},
		{Kind("svn"), "example.com", "", true},
	}
	for _, tt := range tests {
		got, err := DefaultBaseURL(tt.kind, tt.host)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("DefaultBaseURL(%s, %q) = %q, %v, want %q", tt.kind, tt.host, got, err, tt.want)
		}
	}
	if _, err := DefaultBaseURL(GitLab, ""); !errors.Is(err, ErrNoBaseURL) {
		t.Errorf("error = %v, want ErrNoBaseURL", err)
	}
}
