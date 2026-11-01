package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// fakeForge is an httptest forge API. Routes are keyed by method and
// escaped path, so tests can check how paths were encoded. A route's value
// is a JSON value, or a [][]any served as pages linked with Link headers.
type fakeForge struct {
	srv    *httptest.Server
	prefix string

	mu     sync.Mutex
	routes map[string]any
	seen   []string
	bodies []map[string]string
}

// newFake starts a fake forge whose API lives below prefix, such as
// "/api/v4".
func newFake(t *testing.T, prefix string) *fakeForge {
	t.Helper()
	f := &fakeForge{prefix: prefix, routes: map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// on sets the response for method and an escaped API path.
func (f *fakeForge) on(method, path string, v any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+f.prefix+path] = v
}

// session returns a session of kind for the fake.
func (f *fakeForge) session(kind forgeauth.Kind) *forgeauth.Session {
	return &forgeauth.Session{Kind: kind, BaseURL: f.srv.URL + f.prefix, Client: f.srv.Client()}
}

// requests returns the requests seen so far as "METHOD escaped-path?query".
func (f *fakeForge) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *fakeForge) serve(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.EscapedPath()
	f.mu.Lock()
	f.seen = append(f.seen, key+"?"+r.URL.RawQuery)
	v, ok := f.routes[key]
	if r.Body != nil && r.ContentLength != 0 {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.bodies = append(f.bodies, body)
	}
	f.mu.Unlock()
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		return
	}
	if pages, ok := v.([][]any); ok {
		n, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if n < 1 {
			n = 1
		}
		if n < len(pages) {
			q := r.URL.Query()
			q.Set("page", strconv.Itoa(n+1))
			next := *r.URL
			next.RawQuery = q.Encode()
			w.Header().Set("Link", fmt.Sprintf(`<%s%s>; rel="next"`, f.srv.URL, next.RequestURI()))
		}
		v = pages[n-1]
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// obj is a JSON object literal.
type obj = map[string]any

func newClient(t *testing.T, sources ...Source) *Client {
	t.Helper()
	c, err := New(sources...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGitHub(t *testing.T) {
	f := newFake(t, "")
	repo := obj{
		"id": 1, "full_name": "mrbean/teddy", "description": "A bear",
		"default_branch": "main", "private": true, "visibility": "private",
		"html_url":   "https://github.com/mrbean/teddy",
		"clone_url":  "https://github.com/mrbean/teddy.git",
		"ssh_url":    "git@github.com:mrbean/teddy.git",
		"created_at": "2024-01-02T03:04:05Z", "updated_at": "2024-02-03T04:05:06Z",
	}
	f.on("GET", "/user/repos", [][]any{
		{repo},
		{obj{"id": 2, "full_name": "acme/mini", "default_branch": "trunk", "fork": true, "html_url": "https://github.com/acme/mini"}},
	})
	f.on("GET", "/repos/mrbean/teddy", repo)
	f.on("GET", "/repos/mrbean/teddy/branches", [][]any{
		{obj{"name": "dev", "commit": obj{"sha": "aaa"}}},
		{obj{"name": "main", "commit": obj{"sha": "bbb"}, "protected": true}},
	})
	f.on("GET", "/repos/mrbean/teddy/tags", []any{obj{"name": "v1", "commit": obj{"sha": "ccc"}}})
	c := newClient(t, Source{ID: "gh", Session: f.session(forgeauth.GitHub)})
	ctx := context.Background()

	repos, err := c.List(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0].Path != "mrbean/teddy" || repos[1].Path != "acme/mini" || !repos[1].Fork {
		t.Fatalf("List = %+v", repos)
	}
	if v := repos[1].Visibility; v != Public {
		t.Errorf("visibility from private=false = %q, want public", v)
	}

	got, err := c.Get(ctx, Ref{Path: "mrbean/teddy"})
	if err != nil {
		t.Fatal(err)
	}
	want := SourceInfo{ID: "gh", Kind: forgeauth.GitHub, Host: "github.com"}
	if got.Source != want || got.Owner != "mrbean" || got.Name != "teddy" || got.Visibility != Private ||
		got.WebURL != "https://github.com/mrbean/teddy" || got.SSHURL != "git@github.com:mrbean/teddy.git" ||
		got.CreatedAt.Year() != 2024 || got.ID != "1" {
		t.Fatalf("Get = %+v", got)
	}

	branches, err := c.Branches(ctx, got.Ref())
	if err != nil {
		t.Fatal(err)
	}
	wantBranches := []Branch{{Name: "dev", SHA: "aaa"}, {Name: "main", SHA: "bbb", Default: true, Protected: true}}
	if fmt.Sprint(branches) != fmt.Sprint(wantBranches) {
		t.Fatalf("Branches = %+v, want %+v", branches, wantBranches)
	}

	tags, err := c.Tags(ctx, got.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0] != (Tag{Name: "v1", SHA: "ccc"}) {
		t.Fatalf("Tags = %+v", tags)
	}

	for _, r := range f.requests() {
		if strings.HasPrefix(r, "GET /user/repos?") && !strings.Contains(r, "per_page=100") {
			t.Errorf("request %s lacks per_page", r)
		}
	}
}

func TestGitLabNestedPath(t *testing.T) {
	f := newFake(t, "/api/v4")
	project := obj{
		"id": 42, "path_with_namespace": "group/sub/project", "default_branch": "main",
		"visibility": "internal", "forked_from_project": obj{"id": 7},
		"web_url":          "https://gitlab.example/group/sub/project",
		"http_url_to_repo": "https://gitlab.example/group/sub/project.git",
		"ssh_url_to_repo":  "git@gitlab.example:group/sub/project.git",
		"last_activity_at": "2025-05-06T07:08:09Z",
	}
	f.on("GET", "/projects", [][]any{{project}, {obj{"id": 43, "path_with_namespace": "other/thing", "web_url": "https://gitlab.example/other/thing"}}})
	f.on("GET", "/projects/group%2Fsub%2Fproject", project)
	f.on("GET", "/projects/group%2Fsub%2Fproject/repository/branches", []any{
		obj{"name": "main", "commit": obj{"id": "111"}, "protected": true},
		obj{"name": "feature", "commit": obj{"id": "222"}},
	})
	f.on("GET", "/projects/group%2Fsub%2Fproject/repository/tags", [][]any{
		{obj{"name": "v1", "commit": obj{"id": "333"}}},
		{obj{"name": "v2", "commit": obj{"id": "444"}}},
	})
	c := newClient(t, Source{ID: "gl", Session: f.session(forgeauth.GitLab)})
	ctx := context.Background()

	repos, err := c.List(ctx, ListOptions{Owner: "Group"})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Path != "group/sub/project" {
		t.Fatalf("List(owner group) = %+v, want the subgroup project", repos)
	}

	got, err := c.Get(ctx, Ref{Source: "gl", Path: "/group/sub/project/"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "group/sub" || got.Name != "project" || got.Visibility != Internal || !got.Fork ||
		got.Source.Host != "gitlab.example" || got.UpdatedAt.Year() != 2025 || got.CloneURL != "https://gitlab.example/group/sub/project.git" {
		t.Fatalf("Get = %+v", got)
	}

	branches, err := c.Branches(ctx, got.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 2 || !branches[0].Default || branches[0].SHA != "111" || branches[1].Default {
		t.Fatalf("Branches = %+v", branches)
	}

	tags, err := c.Tags(ctx, got.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[1] != (Tag{Name: "v2", SHA: "444"}) {
		t.Fatalf("Tags = %+v", tags)
	}

	for _, r := range f.requests() {
		if strings.Contains(r, "%252F") {
			t.Errorf("request %s is double-encoded", r)
		}
	}
}

func TestForgejo(t *testing.T) {
	f := newFake(t, "/api/v1")
	repo := obj{
		"id": 9, "full_name": "mrbean/garden", "default_branch": "main", "private": false, "internal": true,
		"archived": true, "html_url": "https://codeberg.example/mrbean/garden",
	}
	f.on("GET", "/user/repos", [][]any{{repo}, {obj{"id": 10, "full_name": "mrbean/car", "private": true, "html_url": "https://codeberg.example/mrbean/car"}}})
	f.on("GET", "/repos/mrbean/garden", repo)
	f.on("GET", "/repos/mrbean/garden/branches", []any{obj{"name": "main", "commit": obj{"id": "f00"}}})
	f.on("GET", "/repos/mrbean/garden/tags", []any{obj{"name": "v0.1", "commit": obj{"sha": "ba5"}}})
	c := newClient(t, Source{ID: "fj", Session: f.session(forgeauth.Forgejo)})
	ctx := context.Background()

	repos, err := c.List(ctx, ListOptions{Owner: "mrbean"})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[1].Visibility != Private || repos[0].Visibility != Internal || !repos[0].Archived {
		t.Fatalf("List = %+v", repos)
	}

	branches, err := c.Branches(ctx, Ref{Path: "mrbean/garden"})
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 || branches[0] != (Branch{Name: "main", SHA: "f00", Default: true}) {
		t.Fatalf("Branches = %+v", branches)
	}

	tags, err := c.Tags(ctx, Ref{Source: "fj", Path: "mrbean/garden"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0] != (Tag{Name: "v0.1", SHA: "ba5"}) {
		t.Fatalf("Tags = %+v", tags)
	}

	for _, r := range f.requests() {
		if strings.Contains(r, "per_page") || (strings.Contains(r, "/user/repos") && !strings.Contains(r, "limit=50")) {
			t.Errorf("request %s should page with limit", r)
		}
	}
}

func TestListAcrossSources(t *testing.T) {
	gh := newFake(t, "")
	gh.on("GET", "/user/repos", []any{
		obj{"full_name": "mrbean/teddy", "html_url": "https://github.com/mrbean/teddy"},
		obj{"full_name": "someone/else", "html_url": "https://github.com/someone/else"},
	})
	gl := newFake(t, "/api/v4")
	gl.on("GET", "/projects", []any{
		obj{"path_with_namespace": "mrbean/teddy", "web_url": "https://gitlab.com/mrbean/teddy"},
		obj{"path_with_namespace": "MrBean/tools/cli", "web_url": "https://gitlab.com/MrBean/tools/cli"},
	})
	c := newClient(t,
		Source{ID: "gh", Session: gh.session(forgeauth.GitHub)},
		Source{ID: "gl", Session: gl.session(forgeauth.GitLab)},
	)
	ctx := context.Background()

	repos, err := c.List(ctx, ListOptions{Owner: "mrbean"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range repos {
		got = append(got, string(r.Source.Kind)+":"+r.Path)
	}
	want := "github:mrbean/teddy gitlab:mrbean/teddy gitlab:MrBean/tools/cli"
	if strings.Join(got, " ") != want {
		t.Fatalf("List = %v, want %s", got, want)
	}

	only, err := c.List(ctx, ListOptions{Source: "gl"})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 2 || only[0].Source.ID != "gl" {
		t.Fatalf("List(source gl) = %+v", only)
	}

	if _, err := c.List(ctx, ListOptions{Source: "nope"}); !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("List(unknown source) err = %v", err)
	}
}

func TestFindAndAmbiguity(t *testing.T) {
	gh := newFake(t, "")
	gh.on("GET", "/repos/mrbean/teddy", obj{"full_name": "mrbean/teddy", "html_url": "https://github.com/mrbean/teddy"})
	gl := newFake(t, "/api/v4")
	gl.on("GET", "/projects/mrbean%2Fteddy", obj{"path_with_namespace": "mrbean/teddy", "web_url": "https://gitlab.com/mrbean/teddy"})
	gl.on("GET", "/projects/group%2Fsub%2Fproject", obj{"path_with_namespace": "group/sub/project", "web_url": "https://gitlab.com/group/sub/project"})
	c := newClient(t,
		Source{ID: "gh", Session: gh.session(forgeauth.GitHub)},
		Source{ID: "gl", Session: gl.session(forgeauth.GitLab)},
	)
	ctx := context.Background()

	found, err := c.Find(ctx, "mrbean/teddy")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found[0].Source.ID != "gh" || found[1].Source.ID != "gl" {
		t.Fatalf("Find = %+v", found)
	}

	if _, err := c.Get(ctx, Ref{Path: "mrbean/teddy"}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Get(ambiguous) err = %v", err)
	}
	if _, err := c.Tags(ctx, Ref{Path: "mrbean/teddy"}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Tags(ambiguous) err = %v", err)
	}
	if r, err := c.Get(ctx, Ref{Source: "gl", Path: "mrbean/teddy"}); err != nil || r.Source.Kind != forgeauth.GitLab {
		t.Fatalf("Get(gl) = %+v, %v", r, err)
	}

	// A nested path cannot exist on GitHub, so only GitLab is asked.
	if r, err := c.Get(ctx, Ref{Path: "group/sub/project"}); err != nil || r.Source.ID != "gl" {
		t.Fatalf("Get(nested) = %+v, %v", r, err)
	}
	for _, r := range gh.requests() {
		if strings.Contains(r, "group") {
			t.Errorf("GitHub was asked for a nested path: %s", r)
		}
	}
}

func TestNotFound(t *testing.T) {
	gh := newFake(t, "")
	gl := newFake(t, "/api/v4")
	c := newClient(t,
		Source{ID: "gh", Session: gh.session(forgeauth.GitHub)},
		Source{ID: "gl", Session: gl.session(forgeauth.GitLab)},
	)
	ctx := context.Background()
	for _, ref := range []Ref{{Path: "mrbean/missing"}, {Source: "gh", Path: "mrbean/missing"}, {Source: "gl", Path: "a/b/c"}, {Source: "gh", Path: "a/b/c"}} {
		if _, err := c.Get(ctx, ref); !errors.Is(err, forgehttp.ErrNotFound) {
			t.Errorf("Get(%+v) err = %v, want ErrNotFound", ref, err)
		}
		if _, err := c.Branches(ctx, ref); !errors.Is(err, forgehttp.ErrNotFound) {
			t.Errorf("Branches(%+v) err = %v, want ErrNotFound", ref, err)
		}
	}
	if _, err := c.Tags(ctx, Ref{Source: "gh", Path: "mrbean/missing"}); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("Tags err = %v, want ErrNotFound", err)
	}
	var fe *forgehttp.Error
	if _, err := c.Get(ctx, Ref{Source: "gh", Path: "mrbean/missing"}); !errors.As(err, &fe) || fe.StatusCode != http.StatusNotFound {
		t.Errorf("Get err = %v, want a *forgehttp.Error", err)
	}
}

func TestSourceErrorFailsCall(t *testing.T) {
	gh := newFake(t, "")
	gh.on("GET", "/user/repos", []any{})
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"no"}`, http.StatusForbidden)
	}))
	defer bad.Close()
	c := newClient(t,
		Source{ID: "gh", Session: gh.session(forgeauth.GitHub)},
		Source{ID: "fj", Session: &forgeauth.Session{Kind: forgeauth.Forgejo, BaseURL: bad.URL, Client: bad.Client()}},
	)
	_, err := c.List(context.Background(), ListOptions{})
	if !errors.Is(err, forgehttp.ErrForbidden) || !strings.Contains(err.Error(), "source fj") {
		t.Fatalf("List err = %v, want forbidden from source fj", err)
	}
	// A forbidden source is not treated as "not there" when searching.
	if _, err := c.Get(context.Background(), Ref{Path: "a/b"}); !errors.Is(err, forgehttp.ErrForbidden) {
		t.Fatalf("Get err = %v, want forbidden", err)
	}
}

func TestUpdate(t *testing.T) {
	gh := newFake(t, "")
	gh.on("PATCH", "/repos/mrbean/teddy", obj{"full_name": "mrbean/teddy", "description": "new", "default_branch": "dev", "html_url": "https://github.com/mrbean/teddy"})
	gl := newFake(t, "/api/v4")
	gl.on("PUT", "/projects/group%2Fsub%2Fproject", obj{"path_with_namespace": "group/sub/project", "description": "", "web_url": "https://gitlab.com/group/sub/project"})
	c := newClient(t,
		Source{ID: "gh", Session: gh.session(forgeauth.GitHub)},
		Source{ID: "gl", Session: gl.session(forgeauth.GitLab)},
	)
	ctx := context.Background()
	desc, branch, empty := "new", "dev", ""

	r, err := c.Update(ctx, Ref{Source: "gh", Path: "mrbean/teddy"}, Update{Description: &desc, DefaultBranch: &branch})
	if err != nil {
		t.Fatal(err)
	}
	if r.Description != "new" || r.DefaultBranch != "dev" || r.Source.ID != "gh" {
		t.Fatalf("Update = %+v", r)
	}
	if _, err := c.Update(ctx, Ref{Source: "gl", Path: "group/sub/project"}, Update{Description: &empty}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(gh.bodies) != "[map[default_branch:dev description:new]]" {
		t.Errorf("GitHub body = %v", gh.bodies)
	}
	if fmt.Sprint(gl.bodies) != "[map[description:]]" {
		t.Errorf("GitLab body = %v", gl.bodies)
	}

	for _, u := range []Update{{}, {DefaultBranch: &empty}} {
		if _, err := c.Update(ctx, Ref{Source: "gh", Path: "mrbean/teddy"}, u); !errors.Is(err, ErrInvalid) {
			t.Errorf("Update(%+v) err = %v, want ErrInvalid", u, err)
		}
	}
}

func TestInvalidPaths(t *testing.T) {
	f := newFake(t, "")
	c := newClient(t, Source{ID: "gh", Session: f.session(forgeauth.GitHub)})
	for _, p := range []string{"", "solo", "a//b", "a/../b", "a/./b", "a/b%2Fc", "a/b?x", "a/b#x", `a\b/c`, "a/b c", "a/b\tc"} {
		if _, err := c.Get(context.Background(), Ref{Path: p}); !errors.Is(err, ErrInvalid) {
			t.Errorf("Get(%q) err = %v, want ErrInvalid", p, err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("invalid paths sent %d requests", n)
	}
}

func TestPaginationStaysOnSource(t *testing.T) {
	other := newFake(t, "")
	other.on("GET", "/user/repos", []any{obj{"full_name": "evil/repo"}})
	cases := map[string]string{
		"other host":       other.srv.URL + "/user/repos",
		"other connection": "/c/other/user/repos",
	}
	for name, link := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Link", `<`+link+`>; rel="next"`)
				_, _ = w.Write([]byte(`[]`))
			}))
			defer srv.Close()
			c := newClient(t, Source{ID: "gh", Session: &forgeauth.Session{Kind: forgeauth.GitHub, BaseURL: srv.URL + "/c/mine", Client: srv.Client()}})
			if _, err := c.List(context.Background(), ListOptions{}); err == nil || !strings.Contains(err.Error(), "leaves the source") {
				t.Fatalf("List err = %v, want a pagination error", err)
			}
		})
	}
	if n := len(other.requests()); n != 0 {
		t.Errorf("followed a link to another host %d times", n)
	}
}

func TestPaginationLoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `</user/repos?per_page=100>; rel="next"`)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := newClient(t, Source{ID: "gh", Session: &forgeauth.Session{Kind: forgeauth.GitHub, BaseURL: srv.URL, Client: srv.Client()}})
	if _, err := c.List(context.Background(), ListOptions{}); err == nil || !strings.Contains(err.Error(), "loops") {
		t.Fatalf("List err = %v, want a loop error", err)
	}
}

func TestNew(t *testing.T) {
	f := newFake(t, "")
	ok := f.session(forgeauth.GitHub)
	cases := map[string][]Source{
		"none":        nil,
		"empty ID":    {{ID: " ", Session: ok}},
		"duplicate":   {{ID: "a", Session: ok}, {ID: "a", Session: ok}},
		"no session":  {{ID: "a"}},
		"no client":   {{ID: "a", Session: &forgeauth.Session{Kind: forgeauth.GitHub, BaseURL: f.srv.URL}}},
		"bad kind":    {{ID: "a", Session: &forgeauth.Session{Kind: "svn", BaseURL: f.srv.URL, Client: f.srv.Client()}}},
		"no kind set": {{ID: "a", Session: &forgeauth.Session{BaseURL: f.srv.URL, Client: f.srv.Client()}}},
	}
	for name, sources := range cases {
		if _, err := New(sources...); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	c := newClient(t, Source{ID: "a", Session: ok}, Source{ID: "b", Session: ok})
	if s := c.Sources(); len(s) != 2 || s[0].ID != "a" || s[1].ID != "b" {
		t.Errorf("Sources = %+v", s)
	}
}
