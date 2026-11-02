// Package fakeforge is an in-memory forge for forge-code tests.
//
// A Repo stores real Git objects keyed by their SHA-1 IDs (computed with
// internal/gitobj), refs, a default branch and a private flag. Builders
// (WriteBlob, WriteTree, BuildTree, WriteCommit, CommitTree, WriteTag,
// SetRef) panic on bad input; Seed fills a Repo with a standard Fixture.
// Readers serve forge fronts: ReadBlob, ReadTree, ReadCommit, ReadTag,
// Peel, Lookup (path walk), ListTree (flat or recursive), Log (git log
// order with path filter), MergeBases and IsAncestor. UpdateRefs is an
// atomic compare-and-swap for push fronts.
//
// A Forge maps repository paths, nested ones included, to Repos. Its
// Handler middleware wraps a front and, for every request, in order:
// counts the call, runs the hook (SetHook), serves an injected Fault,
// and checks the token in the forge's header format (401 if missing or
// wrong). Fronts then call Authorize for per-repo access.
//
// A Front builds the HTTP API of one forge kind on top of a Forge.
// NewServer starts an httptest server with the front under APIPrefix(kind)
// and returns a Server with a ready forgeauth Session made by the real
// provider, so headers match production. Mount adds more fronts, such as
// GitFront, a smart-HTTP receive-pack and single-commit fetch front, under
// GitPrefix; GitRemote opens a remote for it.
//
// Call counters, faults and the hook see paths relative to their mount
// prefix, escaped as sent, such as "/repos/o/r/git/trees/abc".
package fakeforge

import (
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
)

// Store errors.
var (
	// ErrNotFound is returned for missing objects and paths.
	ErrNotFound = errors.New("fakeforge: not found")
	// ErrWrongType is returned when an object has another type than
	// asked for.
	ErrWrongType = errors.New("fakeforge: wrong object type")
	// ErrStale is returned by UpdateRefs when a ref is not at its
	// expected value.
	ErrStale = errors.New("fakeforge: ref is not at the expected value")
)

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// Token is the token a new Forge accepts.
const Token = "fake-token"

// Access is what the token's user may do with a repository.
type Access int

// Access levels. The zero value is AccessWrite.
const (
	// AccessWrite allows reads and writes.
	AccessWrite Access = iota
	// AccessRead allows reads only.
	AccessRead
	// AccessNone allows nothing on private repositories and reads on
	// public ones.
	AccessNone
)

// Fault is an injected error response.
type Fault struct {
	// Method matches the request method; empty matches any.
	Method string
	// Path is a pattern for the escaped path, where * matches any run of
	// characters, slashes included.
	Path string
	// Status is the response status.
	Status int
	// Header is added to the response.
	Header http.Header
	// Body is the response body. Empty means {"message": status text}.
	Body string
	// Times is how often the fault fires; 0 means always.
	Times int
}

// Forge is a fake forge of one kind: repositories, a token, per-repo
// access, a hook, faults and call counters. It is safe for concurrent use.
type Forge struct {
	kind forgeauth.Kind

	mu       sync.Mutex
	token    string
	repos    map[string]*Repo
	access   map[string]Access
	nextID   int
	hook     func(*http.Request)
	faults   []*Fault
	calls    map[string]int
	requests []string
}

// New returns an empty forge of kind that accepts Token.
func New(kind forgeauth.Kind) *Forge {
	return &Forge{
		kind:   kind,
		token:  Token,
		repos:  map[string]*Repo{},
		access: map[string]Access{},
		calls:  map[string]int{},
	}
}

// Kind returns the forge kind.
func (f *Forge) Kind() forgeauth.Kind { return f.kind }

// SetToken changes the accepted token.
func (f *Forge) SetToken(tok string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token = tok
}

// AddRepo registers r at path, such as "owner/name" or "group/sub/proj",
// and gives it the next numeric ID. It panics if path is taken or has
// empty segments.
func (f *Forge) AddRepo(path string, r *Repo) *Repo {
	if path == "" || slices.Contains(strings.Split(path, "/"), "") {
		panic(fmt.Sprintf("fakeforge: bad repository path %q", path))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.repos[path]; ok {
		panic(fmt.Sprintf("fakeforge: repository %q exists", path))
	}
	f.nextID++
	r.mu.Lock()
	r.id = f.nextID
	r.mu.Unlock()
	f.repos[path] = r
	return r
}

// NewRepo registers and returns a new empty Repo at path.
func (f *Forge) NewRepo(path string) *Repo { return f.AddRepo(path, NewRepo()) }

// Repo returns the repository at path.
func (f *Forge) Repo(path string) (*Repo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.repos[path]
	return r, ok
}

// RepoByID returns the repository with a numeric ID and its path.
func (f *Forge) RepoByID(id int) (string, *Repo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for p, r := range f.repos {
		if r.ID() == id {
			return p, r, true
		}
	}
	return "", nil, false
}

// Repos returns the sorted repository paths.
func (f *Forge) Repos() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(maps.Keys(f.repos))
}

// SetAccess sets the token user's access to the repository at path.
func (f *Forge) SetAccess(path string, a Access) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.access[path] = a
}

// Authorize returns the repository at path if the token user may read it,
// or write it when write is set. Otherwise it returns the status a forge
// sends: 404 for a missing repository or a private one without access,
// 403 for a write without write access.
func (f *Forge) Authorize(path string, write bool) (*Repo, int) {
	f.mu.Lock()
	r, ok := f.repos[path]
	a := f.access[path]
	f.mu.Unlock()
	switch {
	case !ok:
		return nil, http.StatusNotFound
	case a == AccessNone && r.Private():
		return nil, http.StatusNotFound
	case write && a != AccessWrite:
		return nil, http.StatusForbidden
	}
	return r, 0
}

// SetHook sets a func that runs before every request is served, even one
// that gets a fault or a 401. Tests use it to move refs mid-operation.
// nil removes it.
func (f *Forge) SetHook(fn func(r *http.Request)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hook = fn
}

// Inject adds a fault. Faults are checked in the order added.
func (f *Forge) Inject(ft Fault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = append(f.faults, &ft)
}

// Fail makes every request for method and path pattern get status.
func (f *Forge) Fail(method, path string, status int) {
	f.Inject(Fault{Method: method, Path: path, Status: status})
}

// ClearFaults removes all faults.
func (f *Forge) ClearFaults() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = nil
}

// Calls returns how many requests matched method (empty for any) and the
// path pattern, as in Fault.
func (f *Forge) Calls(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for k, c := range f.calls {
		m, p, _ := strings.Cut(k, " ")
		if (method == "" || method == m) && Match(path, p) {
			n += c
		}
	}
	return n
}

// Requests returns every request so far as "METHOD path?query".
func (f *Forge) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// ResetCalls clears the call counters and the request log.
func (f *Forge) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = map[string]int{}
	f.requests = nil
}

// Handler wraps a front with call counting, the hook, faults and token
// checks.
func (f *Forge) Handler(front http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		f.mu.Lock()
		f.calls[r.Method+" "+path]++
		f.requests = append(f.requests, r.Method+" "+path+"?"+r.URL.RawQuery)
		hook := f.hook
		f.mu.Unlock()
		if hook != nil {
			hook(r)
		}
		if ft := f.fault(r.Method, path); ft != nil {
			for k, vs := range ft.Header {
				w.Header()[k] = slices.Clone(vs)
			}
			if ft.Body == "" {
				WriteMessage(w, ft.Status, http.StatusText(ft.Status))
				return
			}
			w.WriteHeader(ft.Status)
			_, _ = w.Write([]byte(ft.Body))
			return
		}
		if !f.authenticated(r) {
			WriteMessage(w, http.StatusUnauthorized, f.unauthorizedMessage())
			return
		}
		if front == nil {
			WriteMessage(w, http.StatusNotFound, "Not Found")
			return
		}
		front.ServeHTTP(w, r)
	})
}

// fault returns the first live fault for method and path, and uses up
// one of its Times.
func (f *Forge) fault(method, path string) *Fault {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, ft := range f.faults {
		if (ft.Method != "" && ft.Method != method) || !Match(ft.Path, path) {
			continue
		}
		if ft.Times > 0 {
			ft.Times--
			if ft.Times == 0 {
				f.faults = slices.Delete(f.faults, i, i+1)
			}
		}
		return ft
	}
	return nil
}

// authenticated checks the API header of the forge's kind, or Git Basic
// auth with the token as password.
func (f *Forge) authenticated(r *http.Request) bool {
	f.mu.Lock()
	want := f.token
	f.mu.Unlock()
	var got string
	var ok bool
	switch f.kind {
	case forgeauth.GitHub:
		got, ok = github.TokenFromHeader(r.Header)
	case forgeauth.GitLab:
		got, ok = gitlab.TokenFromHeader(r.Header)
	case forgeauth.Forgejo:
		got, ok = forgejo.TokenFromHeader(r.Header)
	}
	if !ok {
		got, ok = basicPassword(r.Header)
	}
	return ok && got == want
}

// basicPassword returns the password of a single Basic Authorization
// header.
func basicPassword(h http.Header) (string, bool) {
	vals := h.Values("Authorization")
	if len(vals) != 1 {
		return "", false
	}
	scheme, enc, ok := strings.Cut(vals[0], " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", false
	}
	_, pw, ok := strings.Cut(string(raw), ":")
	return pw, ok && pw != ""
}

func (f *Forge) unauthorizedMessage() string {
	switch f.kind {
	case forgeauth.GitHub:
		return "Bad credentials"
	case forgeauth.GitLab:
		return "401 Unauthorized"
	}
	return "token is required"
}

// Match reports whether s matches pattern, where * matches any run of
// characters, slashes included.
func Match(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return len(s) >= len(last) && strings.HasSuffix(s, last)
}
