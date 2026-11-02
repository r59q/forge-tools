package fakeforge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
)

// Front builds one forge kind's HTTP API on a Forge. Requests reach it
// with the mount prefix stripped, already counted and authenticated.
type Front func(f *Forge) http.Handler

// GitPrefix is where Git smart-HTTP fronts are mounted.
const GitPrefix = "/git"

// APIPrefix returns the API base path for a forge kind: "/api/v3"
// (GitHub Enterprise style), "/api/v4" or "/api/v1".
func APIPrefix(kind forgeauth.Kind) string {
	switch kind {
	case forgeauth.GitHub:
		return "/api/v3"
	case forgeauth.GitLab:
		return "/api/v4"
	}
	return "/api/v1"
}

// Server is a Forge served over httptest.
type Server struct {
	*Forge
	// HTTP is the running server.
	HTTP *httptest.Server
	// Session is an API session for Token, opened by the real provider.
	Session *forgeauth.Session

	mu     sync.RWMutex
	mounts map[string]http.Handler
}

// NewServer starts a server for a new Forge of kind with api mounted under
// APIPrefix(kind). A nil api answers 404. The server closes when the test
// ends.
func NewServer(t testing.TB, kind forgeauth.Kind, api Front) *Server {
	t.Helper()
	s := &Server{Forge: New(kind), mounts: map[string]http.Handler{}}
	s.HTTP = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.HTTP.Close)
	s.Mount(APIPrefix(kind), api)
	sess, err := s.SessionWithToken(Token)
	if err != nil {
		t.Fatal(err)
	}
	s.Session = sess
	return s
}

// Mount serves front under prefix, such as GitPrefix, wrapped by
// Forge.Handler.
func (s *Server) Mount(prefix string, front Front) {
	var h http.Handler
	if front != nil {
		h = front(s.Forge)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mounts[prefix] = http.StripPrefix(prefix, s.Forge.Handler(h))
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	var best string
	var h http.Handler
	for p, mh := range s.mounts {
		if (r.URL.Path == p || strings.HasPrefix(r.URL.Path, p+"/")) && len(p) > len(best) {
			best, h = p, mh
		}
	}
	s.mu.RUnlock()
	if h == nil {
		http.NotFound(w, r)
		return
	}
	h.ServeHTTP(w, r)
}

// APIBase returns the API base URL.
func (s *Server) APIBase() string { return s.HTTP.URL + APIPrefix(s.Kind()) }

// SessionWithToken opens an API session that sends tok, such as a wrong
// token for 401 tests.
func (s *Server) SessionWithToken(tok string) (*forgeauth.Session, error) {
	p, err := provider(s.Kind())
	if err != nil {
		return nil, err
	}
	return p.Open(context.Background(), forgeauth.Connection{
		Kind:      s.Kind(),
		BaseURL:   s.APIBase(),
		Transport: s.HTTP.Client().Transport,
	}, forgeauth.StaticToken(tok))
}

// GitRemote opens a Git remote for the front mounted at GitPrefix, using
// the provider's Git auth header and Token.
func (s *Server) GitRemote() (*forgeauth.GitRemote, error) {
	p, err := provider(s.Kind())
	if err != nil {
		return nil, err
	}
	ga, ok := p.(forgeauth.GitAuthenticator)
	if !ok {
		return nil, fmt.Errorf("fakeforge: %s has no Git auth", s.Kind())
	}
	return forgeauth.NewGitRemote(s.HTTP.URL+GitPrefix, forgeauth.StaticToken(Token), ga.SetGitToken, s.HTTP.Client().Transport)
}

func provider(kind forgeauth.Kind) (forgeauth.Provider, error) {
	switch kind {
	case forgeauth.GitHub:
		return github.Provider{}, nil
	case forgeauth.GitLab:
		return gitlab.Provider{}, nil
	case forgeauth.Forgejo:
		return forgejo.Provider{}, nil
	}
	return nil, fmt.Errorf("fakeforge: unknown kind %q", kind)
}

// WriteJSON writes v as a JSON response with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteMessage writes {"message": msg} with status, the error shape all
// three forges use.
func WriteMessage(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"message": msg})
}

// Paging names a front's page parameters.
type Paging struct {
	// PageParam is the page number parameter; empty means "page".
	PageParam string
	// SizeParam is the page size parameter, such as "per_page" or "limit".
	SizeParam string
	// DefaultSize and MaxSize bound the page size.
	DefaultSize, MaxSize int
}

// Paginate picks the page of total items that r asks for and returns its
// bounds, items[start:end]. It sets a Link header with first, prev, next
// and last URLs that keep r's other query parameters.
func Paginate(w http.ResponseWriter, r *http.Request, total int, p Paging) (start, end int) {
	pageParam := p.PageParam
	if pageParam == "" {
		pageParam = "page"
	}
	size := p.DefaultSize
	if n, err := strconv.Atoi(r.URL.Query().Get(p.SizeParam)); err == nil && n > 0 {
		size = n
	}
	if p.MaxSize > 0 && size > p.MaxSize {
		size = p.MaxSize
	}
	if size < 1 {
		size = 1
	}
	page := 1
	if n, err := strconv.Atoi(r.URL.Query().Get(pageParam)); err == nil && n > 0 {
		page = n
	}
	last := max(1, (total+size-1)/size)
	start = min(total, (page-1)*size)
	end = min(total, start+size)

	link := func(n int, rel string) string {
		u, err := url.Parse(r.RequestURI)
		if err != nil {
			u = &url.URL{Path: r.URL.Path}
		}
		q := u.Query()
		q.Set(pageParam, strconv.Itoa(n))
		if p.SizeParam != "" {
			q.Set(p.SizeParam, strconv.Itoa(size))
		}
		u.RawQuery = q.Encode()
		return fmt.Sprintf(`<http://%s%s>; rel="%s"`, r.Host, u.RequestURI(), rel)
	}
	var links []string
	if page > 1 {
		links = append(links, link(1, "first"), link(min(page-1, last), "prev"))
	}
	if page < last {
		links = append(links, link(page+1, "next"), link(last, "last"))
	}
	if len(links) > 0 {
		w.Header().Set("Link", strings.Join(links, ", "))
	}
	return start, end
}
