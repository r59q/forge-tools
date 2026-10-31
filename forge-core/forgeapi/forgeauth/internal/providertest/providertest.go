// Package providertest has shared tests for forgeauth providers.
package providertest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// Config describes the provider under test.
type Config struct {
	// Provider is the provider under test.
	Provider forgeauth.Provider
	// Kind is the kind the provider must report.
	Kind forgeauth.Kind
	// APIPath is the API base path, such as "/api/v4".
	APIPath string
	// Header is the auth header the provider sends, such as "Authorization".
	Header string
	// Prefix comes before the token in Header, such as "Bearer ".
	Prefix string
	// User is the forge's JSON response to GET /user.
	User string
	// Want is the identity User decodes to.
	Want forgeauth.Identity
}

const token = "tok-123_abc.DEF"

// authHeaders are the headers any provider might send a token in.
var authHeaders = []string{"Authorization", "Private-Token"}

// server is an httptest server that records requests.
type server struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
}

func newServer(t *testing.T, h http.HandlerFunc) *server {
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r)
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) requests() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs
}

func (c Config) open(t *testing.T, base string) *forgeauth.Session {
	t.Helper()
	s, err := c.Provider.Open(context.Background(), forgeauth.Connection{Kind: c.Kind, BaseURL: base}, forgeauth.StaticToken(token))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return s
}

// checkAuth checks that r carries the token in c's format and no other
// auth header.
func (c Config) checkAuth(t *testing.T, r *http.Request) {
	t.Helper()
	for _, name := range authHeaders {
		got := r.Header.Values(name)
		if strings.EqualFold(name, c.Header) {
			if len(got) != 1 || got[0] != c.Prefix+token {
				t.Errorf("%s = %q, want %q", name, got, c.Prefix+token)
			}
		} else if len(got) != 0 {
			t.Errorf("unexpected %s header %q", name, got)
		}
	}
}

func checkNoAuth(t *testing.T, r *http.Request) {
	t.Helper()
	for _, name := range authHeaders {
		if v := r.Header.Values(name); len(v) != 0 {
			t.Errorf("%s %s got %s header %q", r.Method, r.URL, name, v)
		}
	}
	if strings.Contains(fmt.Sprint(r.Header), token) {
		t.Errorf("%s %s got the token in %v", r.Method, r.URL, r.Header)
	}
}

// Run runs the shared provider tests.
func Run(t *testing.T, c Config) {
	ctx := context.Background()

	t.Run("Kind", func(t *testing.T) {
		if got := c.Provider.Kind(); got != c.Kind {
			t.Errorf("Kind() = %q, want %q", got, c.Kind)
		}
	})

	t.Run("Whoami", func(t *testing.T) {
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != c.APIPath+"/user" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, c.User)
		})
		s := c.open(t, srv.URL+c.APIPath)
		if s.Kind != c.Kind {
			t.Errorf("session kind = %q", s.Kind)
		}
		id, err := c.Provider.Whoami(ctx, s)
		if err != nil {
			t.Fatalf("Whoami() error = %v", err)
		}
		if id != c.Want {
			t.Errorf("Whoami() = %+v, want %+v", id, c.Want)
		}
		reqs := srv.requests()
		if len(reqs) != 1 {
			t.Fatalf("got %d requests, want 1", len(reqs))
		}
		c.checkAuth(t, reqs[0])
	})

	t.Run("Whoami unauthorized", func(t *testing.T) {
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"message":"401 Unauthorized"}`)
		})
		_, err := c.Provider.Whoami(ctx, c.open(t, srv.URL+c.APIPath))
		var herr *forgehttp.Error
		if !errors.Is(err, forgehttp.ErrUnauthorized) || !errors.As(err, &herr) || herr.StatusCode != 401 {
			t.Fatalf("Whoami() error = %v, want ErrUnauthorized", err)
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("error leaks token: %v", err)
		}
	})

	t.Run("Whoami incomplete user", func(t *testing.T) {
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"name":"No ID"}`)
		})
		if id, err := c.Provider.Whoami(ctx, c.open(t, srv.URL+c.APIPath)); err == nil {
			t.Errorf("Whoami() = %+v, want error", id)
		}
	})

	t.Run("Whoami wrong session", func(t *testing.T) {
		other := forgeauth.Kind("other")
		if _, err := c.Provider.Whoami(ctx, &forgeauth.Session{Kind: other, BaseURL: "http://x", Client: http.DefaultClient}); err == nil {
			t.Error("Whoami() accepted a session of another kind")
		}
		if _, err := c.Provider.Whoami(ctx, nil); err == nil {
			t.Error("Whoami() accepted a nil session")
		}
	})

	t.Run("Open kind mismatch", func(t *testing.T) {
		_, err := c.Provider.Open(ctx, forgeauth.Connection{Kind: "other", BaseURL: "https://x"}, forgeauth.StaticToken(token))
		if err == nil {
			t.Error("Open() accepted another kind")
		}
	})

	t.Run("no token to other host", func(t *testing.T) {
		other := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, c.User)
		})
		srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/landed", http.StatusFound)
		})
		s := c.open(t, srv.URL+c.APIPath)

		// Redirect: Whoami's GET /user is redirected to the other host.
		c.Provider.Whoami(ctx, s)
		// Absolute URL to the other host.
		resp, err := s.Client.Get(other.URL + "/direct")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()

		if reqs := srv.requests(); len(reqs) != 1 {
			t.Fatalf("own host got %d requests, want 1", len(reqs))
		} else {
			c.checkAuth(t, reqs[0])
		}
		reqs := other.requests()
		if len(reqs) != 2 {
			t.Fatalf("other host got %d requests, want 2", len(reqs))
		}
		for _, r := range reqs {
			checkNoAuth(t, r)
		}
	})

	t.Run("SetToken", func(t *testing.T) {
		h := http.Header{}
		c.Provider.SetToken(h, token)
		if got := h.Values(c.Header); len(got) != 1 || got[0] != c.Prefix+token {
			t.Errorf("SetToken() header %s = %q, want %q", c.Header, got, c.Prefix+token)
		}
		if got, ok := c.Provider.TokenFromHeader(h); !ok || got != token {
			t.Errorf("TokenFromHeader(SetToken()) = %q, %v", got, ok)
		}
	})

	t.Run("TokenFromHeader rejects", func(t *testing.T) {
		for _, h := range []http.Header{
			{},
			{"Authorization": {""}},
			{"Authorization": {"Basic dTpw"}},
			{"Authorization": {"Bearer"}},
			{"Authorization": {"Bearer a b"}},
			{"Authorization": {"token a", "token b"}},
			{"X-Token": {token}},
		} {
			if got, ok := c.Provider.TokenFromHeader(h); ok {
				t.Errorf("TokenFromHeader(%v) = %q, want false", h, got)
			}
		}
	})
}
