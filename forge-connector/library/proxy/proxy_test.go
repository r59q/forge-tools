package proxy_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-connector/library/proxy"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

const (
	clientToken = "caller-secret"
	forgeToken  = "forge-secret"
)

var providers = []forgeauth.Provider{github.Provider{}, gitlab.Provider{}, forgejo.Provider{}}

type fakeBroker struct {
	conn          forgeauth.Connection
	gitBase       string
	connectionErr error
	verifyErr     error
	issueErr      error
	issued        *broker.IssuedToken
	lookups       atomic.Int32
	verifications atomic.Int32
	issuances     atomic.Int32
}

func (b *fakeBroker) Connection(_ context.Context, id broker.ID) (forgeauth.Connection, error) {
	b.lookups.Add(1)
	if id != "conn" {
		return forgeauth.Connection{}, broker.ErrNotFound
	}
	return b.conn, b.connectionErr
}

func (b *fakeBroker) VerifyClient(_ context.Context, token forgeauth.StaticToken, id broker.ID) (broker.Client, error) {
	b.verifications.Add(1)
	if string(token) != clientToken || id != "conn" {
		return broker.Client{}, broker.ErrUnauthorized
	}
	return broker.Client{}, b.verifyErr
}

func (b *fakeBroker) IssueToken(_ context.Context, id broker.ID) (broker.IssuedToken, error) {
	b.issuances.Add(1)
	if id != "conn" {
		return broker.IssuedToken{}, broker.ErrNotFound
	}
	if b.issued != nil {
		return *b.issued, b.issueErr
	}
	return broker.IssuedToken{Kind: b.conn.Kind, BaseURL: b.conn.BaseURL, GitBaseURL: b.gitBase, Token: forgeToken}, b.issueErr
}

func config(t *testing.T, b *fakeBroker) proxy.Config {
	t.Helper()
	var reg forgeauth.Registry
	for _, provider := range providers {
		if err := reg.Register(provider); err != nil {
			t.Fatal(err)
		}
	}
	return proxy.Config{Verifier: b, TokenIssuer: b, Registry: &reg}
}

func handler(t *testing.T, c proxy.Config) *proxy.Handler {
	t.Helper()
	h, err := proxy.New(c)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func serve(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func readResponse(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestReplacesCredentials(t *testing.T) {
	for _, p := range providers {
		t.Run(string(p.Kind()), func(t *testing.T) {
			var requests atomic.Int32
			forge := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodPost || r.URL.EscapedPath() != "/api/v1/projects/group%2Frepo/issues" || r.URL.RawQuery != "page=2&name=a%2Bb" {
					t.Errorf("unexpected method or URL: %s %s", r.Method, r.URL)
				}
				if r.Host == "proxy.example" {
					t.Error("caller Host reached forge")
				}
				if token, ok := p.TokenFromHeader(r.Header); !ok || token != forgeToken {
					t.Error("forge did not receive its credential")
				}
				for name, values := range r.Header {
					for _, value := range values {
						if strings.Contains(value, clientToken) {
							t.Errorf("caller credential reached forge in %s", name)
						}
					}
				}
				for _, name := range []string{"Cookie", "Cookie2", "Proxy-Authorization", "Forwarded", "X-Forwarded-Host", "X-Hop"} {
					if r.Header.Get(name) != "" {
						t.Errorf("%s reached forge", name)
					}
				}
				if p.Kind() == forgeauth.GitLab && r.Header.Get("Authorization") != "" || p.Kind() != forgeauth.GitLab && r.Header.Get("Private-Token") != "" {
					t.Error("unused credential header reached forge")
				}
				if got, _ := io.ReadAll(r.Body); string(got) != "request body" || r.Header.Get("X-Request") != "kept" {
					t.Error("request data changed")
				}
				w.Header().Set("X-Forge", "kept")
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, "response body")
			}))
			b := &fakeBroker{conn: forgeauth.Connection{Kind: p.Kind(), BaseURL: forge.URL + "/api/v1/"}}
			server := serve(t, handler(t, config(t, b)))
			req, err := http.NewRequest(http.MethodPost, server.URL+"/c/conn/projects/group%2Frepo/issues?page=2&name=a%2Bb", strings.NewReader("request body"))
			if err != nil {
				t.Fatal(err)
			}
			req.Host = "proxy.example"
			req.Header.Set("Authorization", "Bearer "+clientToken)
			req.Header.Set("Private-Token", clientToken)
			p.SetToken(req.Header, clientToken)
			req.Header.Set("Cookie", "session="+clientToken)
			req.Header.Set("Cookie2", clientToken)
			req.Header.Set("Proxy-Authorization", "Basic "+clientToken)
			req.Header.Set("Forwarded", "host=elsewhere.example")
			req.Header.Set("X-Forwarded-Host", "elsewhere.example")
			req.Header.Set("Connection", "Authorization, Private-Token, X-Hop")
			req.Header.Set("X-Hop", "remove")
			req.Header.Set("X-Request", "kept")
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body := readResponse(t, resp)
			if resp.StatusCode != http.StatusCreated || body != "response body" || resp.Header.Get("X-Forge") != "kept" {
				t.Fatalf("unexpected response: %d %q", resp.StatusCode, body)
			}
			if requests.Load() != 1 || b.verifications.Load() != 1 || b.issuances.Load() != 1 {
				t.Error("request was not authorized and forwarded exactly once")
			}
		})
	}
}

func TestProxyErrors(t *testing.T) {
	secretErr := errors.New(clientToken + " " + forgeToken)
	tests := []struct {
		name      string
		path      string
		header    http.Header
		lookupErr error
		verifyErr error
		issueErr  error
		status    int
		verified  int32
		issued    int32
	}{
		{name: "missing token", status: 401},
		{name: "wrong scheme", header: http.Header{"Authorization": {"Basic " + clientToken}}, status: 401},
		{name: "repeated token", header: http.Header{"Authorization": {"Bearer " + clientToken, "Bearer " + clientToken}}, status: 401},
		{name: "unknown token", header: http.Header{"Authorization": {"Bearer unknown"}}, status: 401, verified: 1},
		{name: "unknown connection", path: "/c/unknown/user", status: 404},
		{name: "broker lookup down", lookupErr: secretErr, status: 503},
		{name: "forbidden", verifyErr: fmt.Errorf("wrapped: %w", broker.ErrForbidden), status: 403, verified: 1},
		{name: "unauthorized", verifyErr: broker.ErrUnauthorized, status: 401, verified: 1},
		{name: "deleted connection", verifyErr: broker.ErrNotFound, status: 404, verified: 1},
		{name: "unhealthy connection", verifyErr: broker.ErrUnavailable, status: 503, verified: 1},
		{name: "verify failed", verifyErr: secretErr, status: 503, verified: 1},
		{name: "verify timeout", verifyErr: context.DeadlineExceeded, status: 504, verified: 1},
		{name: "issue failed", issueErr: secretErr, status: 503, verified: 1, issued: 1},
		{name: "issue unauthorized", issueErr: broker.ErrUnauthorized, status: 401, verified: 1, issued: 1},
		{name: "issue forbidden", issueErr: broker.ErrForbidden, status: 403, verified: 1, issued: 1},
		{name: "issue missing", issueErr: broker.ErrNotFound, status: 404, verified: 1, issued: 1},
		{name: "issue timeout", issueErr: context.DeadlineExceeded, status: 504, verified: 1, issued: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: "https://forge.example/api"},
				connectionErr: tc.lookupErr, verifyErr: tc.verifyErr, issueErr: tc.issueErr}
			c := config(t, b)
			c.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("unauthorized request reached transport")
				return nil, secretErr
			})
			path := tc.path
			if path == "" {
				path = "/c/conn/user"
			}
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Header.Set("Authorization", "Bearer "+clientToken)
			if tc.header != nil {
				r.Header = tc.header
			}
			if tc.name == "missing token" {
				r.Header = http.Header{}
			}
			w := httptest.NewRecorder()
			handler(t, c).ServeHTTP(w, r)
			if w.Code != tc.status || !strings.HasPrefix(w.Body.String(), "proxy: ") || w.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("response = %d %q", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), clientToken) || strings.Contains(w.Body.String(), forgeToken) {
				t.Error("error leaked a credential")
			}
			if b.verifications.Load() != tc.verified || b.issuances.Load() != tc.issued {
				t.Error("unexpected verification or issuance")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInvalidConfig(t *testing.T) {
	b := &fakeBroker{}
	for _, change := range []func(*proxy.Config){
		func(c *proxy.Config) { c.Verifier = nil },
		func(c *proxy.Config) { c.TokenIssuer = nil },
		func(c *proxy.Config) { c.Registry = nil },
		func(c *proxy.Config) { c.PublicURL = "https://user:secret@proxy.example" },
		func(c *proxy.Config) { c.PublicURL = "https://proxy.example/path" },
		func(c *proxy.Config) { c.PublicURL = "https://proxy.example?query" },
		func(c *proxy.Config) { c.PublicURL = "file:///tmp/proxy" },
	} {
		c := config(t, b)
		change(&c)
		if _, err := proxy.New(c); !errors.Is(err, broker.ErrInvalid) {
			t.Fatalf("New() error = %v", err)
		}
	}
}

func TestTransportErrorIsSanitized(t *testing.T) {
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: "https://forge.example/api"}}
	c := config(t, b)
	c.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "GET", URL: "https://forge.example/" + clientToken, Err: errors.New(forgeToken)}
	})
	r := httptest.NewRequest(http.MethodGet, "/c/conn/user", nil)
	r.Header.Set("Authorization", "Bearer "+clientToken)
	w := httptest.NewRecorder()
	h := handler(t, c)
	h.ServeHTTP(w, r)
	if w.Code != 502 || w.Body.String() != "proxy: Bad Gateway\n" {
		t.Fatalf("response = %d %q", w.Code, w.Body.String())
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		if got := fmt.Sprintf(verb, h); got != "proxy.Handler" {
			t.Error("handler formatting exposed internals")
		}
	}
}

func TestAcceptedCallerHeaderFormats(t *testing.T) {
	for _, tc := range []struct {
		kind          forgeauth.Kind
		header, value string
	}{
		{forgeauth.GitHub, "Authorization", "token " + clientToken},
		{forgeauth.GitHub, "Authorization", "bEaReR " + clientToken},
		{forgeauth.GitLab, "Authorization", "Bearer " + clientToken},
		{forgeauth.GitLab, "Private-Token", clientToken},
		{forgeauth.Forgejo, "Authorization", "Bearer " + clientToken},
		{forgeauth.Forgejo, "Authorization", "token " + clientToken},
	} {
		t.Run(string(tc.kind)+tc.header+strings.Fields(tc.value)[0], func(t *testing.T) {
			forge := serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
			b := &fakeBroker{conn: forgeauth.Connection{Kind: tc.kind, BaseURL: forge.URL}}
			r := httptest.NewRequest(http.MethodGet, "/c/conn/user", nil)
			r.Header.Set(tc.header, tc.value)
			w := httptest.NewRecorder()
			handler(t, config(t, b)).ServeHTTP(w, r)
			if w.Code != 204 || b.verifications.Load() != 1 {
				t.Errorf("accepted header: status = %d", w.Code)
			}
		})
	}
}

func TestForgeErrorsPassThrough(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 429, 500, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			const body = `{"message":"forge failure"}`
			forge := serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(code)
				_, _ = io.WriteString(w, body)
			}))
			b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: forge.URL}}
			r := httptest.NewRequest(http.MethodGet, "/c/conn/user", nil)
			r.Header.Set("Authorization", "Bearer "+clientToken)
			w := httptest.NewRecorder()
			handler(t, config(t, b)).ServeHTTP(w, r)
			if w.Code != code || w.Body.String() != body || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Retry-After") != "60" {
				t.Error("forge error response changed")
			}
		})
	}
}

func TestPublicURLAndOriginMatching(t *testing.T) {
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: "https://Forge.EXAMPLE/api"}}
	c := config(t, b)
	c.PublicURL = "https://proxy.example/"
	c.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://Forge.EXAMPLE/api/user" || r.Host != "Forge.EXAMPLE" {
			t.Error("transport did not use the connection endpoint")
		}
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{
			"Location": {"https://forge.example:443/api/next?x=1#part"},
			"Link":     {`<//FORGE.EXAMPLE:443/api/projects/a%2Fb?page=2>; rel="next"`},
		}}, nil
	})
	r := httptest.NewRequest(http.MethodGet, "/c/conn/user", nil)
	r.Host = "internal.example"
	r.Header.Set("Authorization", "Bearer "+clientToken)
	r.Header.Set("X-Forwarded-Host", "attacker.example")
	r.Header.Set("X-Forwarded-Proto", "http")
	w := httptest.NewRecorder()
	handler(t, c).ServeHTTP(w, r)
	if got := w.Header().Get("Location"); got != "https://proxy.example/c/conn/next?x=1#part" {
		t.Errorf("Location = %q", got)
	}
	if next, _ := forgehttp.NextPage(w.Header()); next != "https://proxy.example/c/conn/projects/a%2Fb?page=2" {
		t.Errorf("next Link = %q", next)
	}
}

func TestRepeatedLocationIsDropped(t *testing.T) {
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitHub, BaseURL: "https://forge.example/api"}}
	c := config(t, b)
	c.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{
			"Location": {"/api/a", "https://other.example/steal"},
		}}, nil
	})
	r := httptest.NewRequest(http.MethodGet, "/c/conn/user", nil)
	r.Header.Set("Authorization", "Bearer "+clientToken)
	w := httptest.NewRecorder()
	handler(t, c).ServeHTTP(w, r)
	if w.Code != 302 || len(w.Header().Values("Location")) != 0 {
		t.Error("ambiguous Location headers were not dropped")
	}
}
