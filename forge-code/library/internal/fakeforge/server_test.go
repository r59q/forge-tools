package fakeforge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// echoFront serves GET /repo/{path...}/refs as a paged list of ref names
// and POST /repo/{path...}/refs as a write.
func echoFront(f *Forge) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, ok := strings.CutPrefix(r.URL.Path, "/repo/")
		path, ok2 := strings.CutSuffix(path, "/refs")
		if !ok || !ok2 {
			WriteMessage(w, http.StatusNotFound, "Not Found")
			return
		}
		repo, status := f.Authorize(path, r.Method != http.MethodGet)
		if status != 0 {
			WriteMessage(w, status, http.StatusText(status))
			return
		}
		refs := repo.Refs("")
		start, end := Paginate(w, r, len(refs), Paging{SizeParam: "per_page", DefaultSize: 2, MaxSize: 3})
		var names []string
		for _, ref := range refs[start:end] {
			names = append(names, ref.Name)
		}
		WriteJSON(w, http.StatusOK, names)
	})
}

func get(t *testing.T, s *forgeauth.Session, path string, v any) error {
	t.Helper()
	return s.GetJSON(context.Background(), path, v)
}

func TestServerAuthPerKind(t *testing.T) {
	for _, kind := range []forgeauth.Kind{forgeauth.GitHub, forgeauth.GitLab, forgeauth.Forgejo} {
		t.Run(string(kind), func(t *testing.T) {
			s := NewServer(t, kind, echoFront)
			Seed(s.NewRepo("group/sub/proj"))
			var names []string
			if err := get(t, s.Session, "/repo/group/sub/proj/refs", &names); err != nil || len(names) != 2 {
				t.Fatalf("names = %v, %v", names, err)
			}
			bad, err := s.SessionWithToken("wrong")
			if err != nil {
				t.Fatal(err)
			}
			if err := get(t, bad, "/repo/group/sub/proj/refs", &names); !errors.Is(err, forgehttp.ErrUnauthorized) {
				t.Errorf("wrong token: %v", err)
			}
			resp, err := http.Get(s.APIBase() + "/repo/group/sub/proj/refs")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("no token: %d", resp.StatusCode)
			}
		})
	}
}

func TestServerGitRemoteAuth(t *testing.T) {
	s := NewServer(t, forgeauth.GitLab, nil)
	s.NewRepo("o/r")
	s.Mount(GitPrefix, func(f *Forge) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(r.URL.Path))
		})
	})
	g, err := s.GitRemote()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := g.URL("o/r")
	resp, err := g.Client.Get(u + "/info/refs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := s.Calls(http.MethodGet, "/o/r.git/info/refs"); got != 1 {
		t.Errorf("Calls = %d", got)
	}
	// The nil API front answers 404 after auth.
	if err := get(t, s.Session, "/x", new(any)); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("nil front: %v", err)
	}
	resp, _ = http.Get(s.HTTP.URL + "/elsewhere")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unmounted: %d", resp.StatusCode)
	}
}

func TestServerAccess(t *testing.T) {
	s := NewServer(t, forgeauth.GitHub, echoFront)
	pub, priv := s.NewRepo("o/pub"), s.NewRepo("o/priv")
	priv.SetPrivate(true)
	if pub.ID() != 1 || priv.ID() != 2 {
		t.Errorf("IDs = %d, %d", pub.ID(), priv.ID())
	}
	if p, r, ok := s.RepoByID(2); !ok || r != priv || p != "o/priv" {
		t.Error("RepoByID")
	}
	s.SetAccess("o/pub", AccessNone)
	s.SetAccess("o/priv", AccessNone)
	var names []string
	if err := get(t, s.Session, "/repo/o/pub/refs", &names); err != nil {
		t.Errorf("public read: %v", err)
	}
	if err := get(t, s.Session, "/repo/o/priv/refs", &names); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("private read: %v", err)
	}
	if err := get(t, s.Session, "/repo/o/none/refs", &names); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("missing repo: %v", err)
	}
	for _, tc := range []struct {
		access Access
		want   int
	}{{AccessRead, 403}, {AccessNone, 404}, {AccessWrite, 0}} {
		s.SetAccess("o/priv", tc.access)
		if _, got := s.Authorize("o/priv", true); got != tc.want {
			t.Errorf("write with %v: %d", tc.access, got)
		}
	}
	if got := s.Repos(); len(got) != 2 || got[0] != "o/priv" {
		t.Errorf("Repos = %v", got)
	}
}

func TestServerFaultsHookCalls(t *testing.T) {
	s := NewServer(t, forgeauth.GitHub, echoFront)
	repo := s.NewRepo("o/r")
	fx := Seed(repo)
	var hooked atomic.Int32
	s.SetHook(func(r *http.Request) {
		hooked.Add(1)
		repo.SetRef("refs/heads/moved", fx.Head)
	})
	s.Inject(Fault{
		Path:   "/repo/*/refs",
		Status: http.StatusForbidden,
		Header: http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"1"}},
		Times:  1,
	})
	var names []string
	if err := get(t, s.Session, "/repo/o/r/refs", &names); !errors.Is(err, forgehttp.ErrRateLimited) {
		t.Errorf("fault: %v", err)
	}
	if err := get(t, s.Session, "/repo/o/r/refs", &names); err != nil {
		t.Errorf("after fault: %v", err)
	}
	if _, ok := repo.Ref("refs/heads/moved"); !ok || hooked.Load() != 2 {
		t.Errorf("hook ran %d times", hooked.Load())
	}
	s.Fail(http.MethodGet, "/repo/o/r/*", http.StatusTeapot)
	if err := get(t, s.Session, "/repo/o/r/refs", &names); err == nil {
		t.Error("Fail did not fire")
	}
	s.ClearFaults()
	s.Inject(Fault{Path: "*", Status: http.StatusBadGateway, Body: "upstream down"})
	if err := get(t, s.Session, "/repo/o/r/refs", &names); err == nil || !strings.Contains(err.Error(), "upstream down") {
		t.Errorf("custom body: %v", err)
	}
	s.ClearFaults()
	s.SetHook(nil)
	if got := s.Calls(http.MethodGet, "/repo/o/r/refs"); got != 4 {
		t.Errorf("Calls = %d", got)
	}
	if got := s.Calls("POST", "*"); got != 0 {
		t.Errorf("POST calls = %d", got)
	}
	if got := s.Requests(); len(got) != 4 || got[0] != "GET /repo/o/r/refs?" {
		t.Errorf("Requests = %q", got)
	}
	s.ResetCalls()
	if s.Calls("", "*") != 0 {
		t.Error("ResetCalls")
	}
}

func TestPaginate(t *testing.T) {
	for _, tc := range []struct {
		query             string
		total             int
		start, end        int
		next, prev, other bool
	}{
		{"", 5, 0, 2, true, false, true},
		{"page=2&per_page=2", 5, 2, 4, true, true, true},
		{"page=3", 5, 4, 5, false, true, true},
		{"page=9", 5, 5, 5, false, true, true},
		{"per_page=50", 5, 0, 3, true, false, true},
		{"page=x&per_page=-1", 0, 0, 0, false, false, false},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v3/list?state=all&"+tc.query, nil)
		start, end := Paginate(w, r, tc.total, Paging{SizeParam: "per_page", DefaultSize: 2, MaxSize: 3})
		if start != tc.start || end != tc.end {
			t.Errorf("%q: [%d:%d], want [%d:%d]", tc.query, start, end, tc.start, tc.end)
		}
		links := forgehttp.ParseLinks(w.Header())
		next, hasNext := links.Get("next")
		_, hasPrev := links.Get("prev")
		if hasNext != tc.next || hasPrev != tc.prev || (len(links) > 0) != tc.other {
			t.Errorf("%q: Link = %q", tc.query, w.Header().Get("Link"))
		}
		if hasNext && !strings.HasPrefix(next, "http://example.com/api/v3/list?") || hasNext && !strings.Contains(next, "state=all") {
			t.Errorf("%q: next = %q", tc.query, next)
		}
	}
}

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern, s string
		want       bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/bc", false},
		{"*", "", true},
		{"/a/*", "/a/b/c", true},
		{"/a/*/c", "/a/x/y/c", true},
		{"/a/*/c", "/a/x/y/d", false},
		{"*/git/trees/*", "/repos/o/r/git/trees/abc", true},
		{"/a*a", "/a", false},
		{"*%2F*", "/projects/g%2Fp", true},
	} {
		if got := Match(tc.pattern, tc.s); got != tc.want {
			t.Errorf("Match(%q, %q) = %v", tc.pattern, tc.s, got)
		}
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	WriteMessage(w, http.StatusNotFound, "Not Found")
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["message"] != "Not Found" {
		t.Errorf("body = %s", w.Body)
	}
	if w.Code != 404 || w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("%d %s", w.Code, w.Header())
	}
}

func TestForgeTokenAndRepo(t *testing.T) {
	s := NewServer(t, forgeauth.Forgejo, echoFront)
	repo := s.NewRepo("o/r")
	if got, ok := s.Repo("o/r"); !ok || got != repo || s.Kind() != forgeauth.Forgejo {
		t.Fatal("Repo lookup")
	}
	if _, ok := s.Repo("o/x"); ok {
		t.Error("found a missing repo")
	}
	s.SetToken("other")
	var names []string
	if err := get(t, s.Session, "/repo/o/r/refs", &names); !errors.Is(err, forgehttp.ErrUnauthorized) {
		t.Errorf("old token: %v", err)
	}
	other, _ := s.SessionWithToken("other")
	if err := get(t, other, "/repo/o/r/refs", &names); err != nil {
		t.Errorf("new token: %v", err)
	}
	for _, bad := range []string{"", "a//b", "/a"} {
		func() {
			defer func() { _ = recover() }()
			s.NewRepo(bad)
			t.Errorf("NewRepo(%q) did not panic", bad)
		}()
	}
}
