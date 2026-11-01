package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// fakeForge is an httptest forge API. Routes are keyed by method and
// escaped path, so tests can check how paths were encoded. A route's value
// is a JSON value, a [][]any served as pages linked with Link headers, a
// status code, a reply, or a func that picks the value per request.
type fakeForge struct {
	srv    *httptest.Server
	prefix string

	mu     sync.Mutex
	routes map[string]any
	seen   []string
	bodies []string
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

// reply is a response with a status code, and a Location if set.
type reply struct {
	code     int
	location string
	body     any
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

// sent returns the request bodies seen so far as compact JSON with sorted
// keys.
func (f *fakeForge) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

func (f *fakeForge) serve(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.EscapedPath()
	data, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.seen = append(f.seen, key+"?"+r.URL.RawQuery)
	v, ok := f.routes[key]
	if len(data) > 0 {
		var body any
		_ = json.Unmarshal(data, &body)
		canon, _ := json.Marshal(body)
		f.bodies = append(f.bodies, string(canon))
	}
	f.mu.Unlock()
	if fn, isFn := v.(func(*http.Request) any); isFn {
		v = fn(r)
	}
	if code, isCode := v.(int); isCode {
		w.WriteHeader(code)
		return
	}
	if rp, isReply := v.(reply); isReply {
		if rp.location != "" {
			w.Header().Set("Location", f.prefix+rp.location)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rp.code)
		if rp.body != nil {
			_ = json.NewEncoder(w).Encode(rp.body)
		}
		return
	}
	if !ok || v == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		return
	}
	if pages, isPages := v.([][]any); isPages {
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
	if r.Method == http.MethodPost {
		w.WriteHeader(http.StatusCreated)
	}
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

func ptr[T any](v T) *T { return &v }

func date(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// sameIssue fails t if got differs from want.
func sameIssue(t *testing.T, what string, got, want Issue) {
	t.Helper()
	if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
		t.Errorf("%s =\n%+v\nwant\n%+v", what, got, want)
	}
}

// hasRequest fails t unless a request starts with prefix and contains
// every part.
func hasRequest(t *testing.T, f *fakeForge, prefix string, parts ...string) {
	t.Helper()
	for _, r := range f.requests() {
		if !strings.HasPrefix(r, prefix) {
			continue
		}
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(r, p)
		}
		if ok {
			return
		}
	}
	t.Errorf("no request %s with %v in %v", prefix, parts, f.requests())
}

func githubIssueJSON() obj {
	return obj{
		"id": 1001, "number": 7, "title": "Bear is lost", "body": "Find him", "state": "closed",
		"user":      obj{"login": "mrbean", "html_url": "https://github.com/mrbean"},
		"assignees": []any{obj{"login": "irma", "html_url": "https://github.com/irma"}},
		"labels":    []any{obj{"name": "bug"}, "urgent"},
		"milestone": obj{"title": "v1"}, "comments": 3, "locked": true,
		"html_url":       "https://github.com/mrbean/teddy/issues/7",
		"repository_url": "https://api.github.com/repos/mrbean/teddy",
		"created_at":     "2024-01-02T03:04:05Z", "updated_at": "2024-02-03T04:05:06Z",
		"closed_at": "2024-03-04T05:06:07Z",
	}
}

func TestGitHub(t *testing.T) {
	f := newFake(t, "")
	pr := obj{"id": 2, "number": 8, "title": "PR", "state": "open", "pull_request": obj{"url": "x"}, "html_url": "https://github.com/mrbean/teddy/pull/8"}
	open := obj{"id": 3, "number": 9, "title": "Open", "state": "open", "labels": []any{obj{"name": "bug"}, obj{"name": "urgent"}},
		"closed_at": nil, "milestone": nil, "body": nil, "html_url": "https://github.com/mrbean/teddy/issues/9"}
	f.on("GET", "/repos/mrbean/teddy/issues", [][]any{{githubIssueJSON(), pr}, {open}})
	f.on("GET", "/repos/mrbean/teddy/issues/7", githubIssueJSON())
	f.on("GET", "/repos/mrbean/teddy/issues/8", pr)
	f.on("GET", "/issues", []any{obj{"id": 4, "number": 1, "title": "Elsewhere", "state": "open",
		"repository": obj{"full_name": "acme/rocket"}, "html_url": "https://github.com/acme/rocket/issues/1"}, pr})
	c := newClient(t, Source{ID: "gh", Session: f.session(forgeauth.GitHub)})
	ctx := context.Background()

	want := Issue{
		Source:     SourceInfo{ID: "gh", Kind: forgeauth.GitHub, Host: "github.com"},
		Repository: "mrbean/teddy", ID: "1001", Number: 7, Title: "Bear is lost", Body: "Find him",
		State: Closed, Author: User{Login: "mrbean", WebURL: "https://github.com/mrbean"},
		Assignees: []User{{Login: "irma", WebURL: "https://github.com/irma"}},
		Labels:    []string{"bug", "urgent"}, Milestone: "v1", Comments: 3, Locked: true,
		WebURL:    "https://github.com/mrbean/teddy/issues/7",
		CreatedAt: date("2024-01-02T03:04:05Z"), UpdatedAt: date("2024-02-03T04:05:06Z"),
		ClosedAt: date("2024-03-04T05:06:07Z"),
	}

	list, err := c.List(ctx, ListOptions{Repository: "mrbean/teddy", State: StateAll, Labels: []string{"bug", "urgent"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[1].Number != 9 || list[1].State != Open || !list[1].ClosedAt.IsZero() || list[1].Body != "" {
		t.Fatalf("List = %+v, want issues 7 and 9 without the pull request", list)
	}
	sameIssue(t, "List[0]", list[0], want)
	hasRequest(t, f, "GET /repos/mrbean/teddy/issues?", "state=all", "labels=bug%2Curgent", "per_page=100")

	got, err := c.Get(ctx, Ref{Repository: "mrbean/teddy", Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	sameIssue(t, "Get", got, want)
	if got.Ref() != (Ref{Source: "gh", Repository: "mrbean/teddy", Number: 7}) {
		t.Errorf("Ref = %+v", got.Ref())
	}
	if _, err := c.Get(ctx, Ref{Source: "gh", Repository: "mrbean/teddy", Number: 8}); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("Get(pull request) err = %v, want ErrNotFound", err)
	}

	mine, err := c.List(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].Repository != "acme/rocket" {
		t.Fatalf("List(account) = %+v", mine)
	}
	hasRequest(t, f, "GET /issues?", "filter=assigned", "state=open", "per_page=100")
	if _, err := c.List(ctx, ListOptions{Scope: Created, State: StateClosed}); err != nil {
		t.Fatal(err)
	}
	hasRequest(t, f, "GET /issues?", "filter=created", "state=closed")
}

func TestGitHubWrites(t *testing.T) {
	f := newFake(t, "")
	f.on("GET", "/repos/mrbean/teddy/issues/7", githubIssueJSON())
	f.on("GET", "/repos/mrbean/teddy/issues/8", obj{"number": 8, "pull_request": obj{}})
	f.on("POST", "/repos/mrbean/teddy/issues", githubIssueJSON())
	f.on("PATCH", "/repos/mrbean/teddy/issues/7", githubIssueJSON())
	f.on("GET", "/repos/mrbean/teddy/issues/7/comments", [][]any{
		{obj{"id": 11, "body": "first", "user": obj{"login": "irma", "html_url": "https://github.com/irma"},
			"html_url": "https://github.com/mrbean/teddy/issues/7#issuecomment-11", "created_at": "2024-05-01T00:00:00Z", "updated_at": "2024-05-02T00:00:00Z"}},
		{obj{"id": 12, "body": "second"}},
	})
	f.on("POST", "/repos/mrbean/teddy/issues/7/comments", obj{"id": 13, "body": "hi", "html_url": "https://github.com/mrbean/teddy/issues/7#issuecomment-13"})
	c := newClient(t, Source{ID: "gh", Session: f.session(forgeauth.GitHub)})
	ctx := context.Background()
	ref := Ref{Source: "gh", Repository: "mrbean/teddy", Number: 7}

	iss, err := c.Create(ctx, RepositoryRef{Source: "gh", Repository: "mrbean/teddy"}, NewIssue{Title: "Bear", Body: "Lost", Labels: []string{"bug"}, Assignees: []string{"irma"}})
	if err != nil {
		t.Fatal(err)
	}
	if iss.Number != 7 || iss.Source.ID != "gh" || iss.Repository != "mrbean/teddy" {
		t.Errorf("Create = %+v", iss)
	}
	if _, err := c.Create(ctx, RepositoryRef{Source: "gh", Repository: "mrbean/teddy"}, NewIssue{Title: "Plain"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Update(ctx, ref, Update{Title: ptr("New"), Body: ptr(""), State: ptr(Closed), Labels: &[]string{}, Assignees: ptr([]string(nil))}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Update(ctx, ref, Update{State: ptr(Open), Labels: &[]string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	wantBodies := []string{
		`{"assignees":["irma"],"body":"Lost","labels":["bug"],"title":"Bear"}`,
		`{"body":"","title":"Plain"}`,
		`{"assignees":[],"body":"","labels":[],"state":"closed","title":"New"}`,
		`{"labels":["a","b"],"state":"open"}`,
	}
	if got := f.sent(); !slices.Equal(got, wantBodies) {
		t.Errorf("bodies =\n%v\nwant\n%v", got, wantBodies)
	}

	comments, err := c.Comments(ctx, Ref{Repository: "mrbean/teddy", Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	wantFirst := Comment{ID: "11", Author: User{Login: "irma", WebURL: "https://github.com/irma"}, Body: "first",
		WebURL:    "https://github.com/mrbean/teddy/issues/7#issuecomment-11",
		CreatedAt: date("2024-05-01T00:00:00Z"), UpdatedAt: date("2024-05-02T00:00:00Z")}
	if len(comments) != 2 || comments[0] != wantFirst || comments[1].Body != "second" {
		t.Fatalf("Comments = %+v", comments)
	}
	hasRequest(t, f, "GET /repos/mrbean/teddy/issues/7/comments?", "per_page=100")

	cm, err := c.AddComment(ctx, ref, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if cm.ID != "13" || cm.WebURL != "https://github.com/mrbean/teddy/issues/7#issuecomment-13" {
		t.Errorf("AddComment = %+v", cm)
	}
	if b := f.sent(); b[len(b)-1] != `{"body":"hi"}` {
		t.Errorf("comment body = %s", b[len(b)-1])
	}

	// Writes and comment reads never touch a pull request.
	prRef := Ref{Source: "gh", Repository: "mrbean/teddy", Number: 8}
	n := len(f.sent())
	if _, err := c.Update(ctx, prRef, Update{Title: ptr("x")}); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("Update(pull request) err = %v", err)
	}
	if _, err := c.AddComment(ctx, prRef, "x"); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("AddComment(pull request) err = %v", err)
	}
	if _, err := c.Comments(ctx, prRef); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("Comments(pull request) err = %v", err)
	}
	if len(f.sent()) != n {
		t.Errorf("a pull request was written to")
	}
}

func TestGitHubTransferredIssue(t *testing.T) {
	f := newFake(t, "")
	moved := githubIssueJSON()
	moved["number"], moved["title"] = 9, "old"
	moved["repository_url"] = "https://api.github.com/repos/new/teddy"
	moved["html_url"] = "https://github.com/new/teddy/issues/9"
	f.on("GET", "/repos/mrbean/teddy/issues/7", reply{code: http.StatusMovedPermanently, location: "/repositories/1/issues/9"})
	f.on("PATCH", "/repos/mrbean/teddy/issues/7", reply{code: http.StatusMovedPermanently, location: "/repositories/1/issues/9"})
	f.on("GET", "/repositories/1/issues/9", moved)
	edited := githubIssueJSON()
	edited["number"], edited["title"] = 9, "NEW"
	f.on("PATCH", "/repos/new/teddy/issues/9", edited)
	f.on("POST", "/repos/new/teddy/issues/9/comments", obj{"id": 5, "body": "hi"})
	c := newClient(t, Source{ID: "gh", Session: f.session(forgeauth.GitHub)})
	ctx := context.Background()
	ref := Ref{Source: "gh", Repository: "mrbean/teddy", Number: 7}

	iss, err := c.Update(ctx, ref, Update{Title: ptr("NEW")})
	if err != nil || iss.Title != "NEW" || iss.Number != 9 {
		t.Fatalf("Update = %+v, %v, want the new title on #9", iss, err)
	}
	if _, err := c.AddComment(ctx, ref, "hi"); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.requests() {
		if !strings.HasPrefix(r, "GET") && strings.Contains(r, "/mrbean/") {
			t.Errorf("wrote to the old path: %s", r)
		}
	}
	hasRequest(t, f, "PATCH /repos/new/teddy/issues/9?")
}

func TestGone(t *testing.T) {
	gh := newFake(t, "")
	gh.on("GET", "/repos/a/b/issues", http.StatusGone)
	gh.on("GET", "/repos/a/b/issues/1", http.StatusGone)
	gh.on("GET", "/repos/a/b/issues/2", obj{"number": 2, "state": "open"})
	gh.on("GET", "/repos/a/b/issues/2/comments", http.StatusGone)
	gl := newFake(t, "/api/v4")
	gl.on("GET", "/projects/a%2Fb/issues", []any{obj{"iid": 1, "state": "opened"}})
	gl.on("GET", "/projects/a%2Fb/issues/1", obj{"iid": 1, "state": "opened"})
	c := newClient(t,
		Source{ID: "gh", Session: gh.session(forgeauth.GitHub)},
		Source{ID: "gl", Session: gl.session(forgeauth.GitLab)},
	)
	ctx := context.Background()

	// A source that answers 410 is skipped, like one without the issue.
	if iss, err := c.Get(ctx, Ref{Repository: "a/b", Number: 1}); err != nil || iss.Source.ID != "gl" {
		t.Errorf("Get = %+v, %v, want the GitLab issue", iss, err)
	}
	if list, err := c.List(ctx, ListOptions{Repository: "a/b"}); err != nil || len(list) != 1 || list[0].Source.ID != "gl" {
		t.Errorf("List = %+v, %v, want only the GitLab issue", list, err)
	}
	var fe *forgehttp.Error
	if _, err := c.Get(ctx, Ref{Source: "gh", Repository: "a/b", Number: 1}); !errors.Is(err, forgehttp.ErrNotFound) || !errors.As(err, &fe) || fe.StatusCode != http.StatusGone {
		t.Errorf("Get(gh) err = %v, want a 410 wrapping ErrNotFound", err)
	}
	if _, err := c.List(ctx, ListOptions{Source: "gh", Repository: "a/b"}); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("List(gh) err = %v, want ErrNotFound", err)
	}
	if _, err := c.Comments(ctx, Ref{Source: "gh", Repository: "a/b", Number: 2}); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("Comments(gh) err = %v, want ErrNotFound", err)
	}
}

func gitlabIssueJSON() obj {
	return obj{
		"id": 501, "iid": 3, "project_id": 42, "title": "Lost bear", "description": "Find him", "state": "opened",
		"author":    obj{"username": "mrbean", "web_url": "https://gitlab.example/mrbean"},
		"assignees": []any{obj{"username": "irma", "web_url": "https://gitlab.example/irma"}},
		"labels":    []any{"bug", "urgent"}, "milestone": obj{"title": "v2"},
		"user_notes_count": 4, "discussion_locked": true,
		"web_url":    "https://gitlab.example/group/sub/project/-/issues/3",
		"references": obj{"short": "#3", "relative": "#3", "full": "group/sub/project#3"},
		"created_at": "2025-01-02T03:04:05.123Z", "updated_at": "2025-02-03T04:05:06Z", "closed_at": nil,
	}
}

func TestGitLab(t *testing.T) {
	f := newFake(t, "/api/v4")
	closed := obj{"id": 502, "iid": 4, "title": "Done", "state": "closed", "discussion_locked": nil, "labels": []any{},
		"web_url": "https://gitlab.example/group/sub/project/-/issues/4", "closed_at": "2025-03-01T00:00:00Z"}
	f.on("GET", "/projects/group%2Fsub%2Fproject/issues", [][]any{{gitlabIssueJSON()}, {closed}})
	f.on("GET", "/projects/group%2Fsub%2Fproject/issues/3", gitlabIssueJSON())
	f.on("GET", "/issues", []any{obj{"id": 9, "iid": 1, "title": "Mine", "state": "opened",
		"web_url": "https://gitlab.example/other/thing/-/issues/1"}})
	f.on("GET", "/projects/group%2Fsub%2Fproject/issues/3/notes", [][]any{
		{obj{"id": 71, "body": "first", "system": false, "author": obj{"username": "irma", "web_url": "https://gitlab.example/irma"},
			"created_at": "2025-04-01T00:00:00Z", "updated_at": "2025-04-02T00:00:00Z"},
			obj{"id": 72, "body": "changed the description", "system": true}},
		{obj{"id": 73, "body": "second"}},
	})
	c := newClient(t, Source{ID: "gl", Session: f.session(forgeauth.GitLab)})
	ctx := context.Background()

	want := Issue{
		Source:     SourceInfo{ID: "gl", Kind: forgeauth.GitLab, Host: "gitlab.example"},
		Repository: "group/sub/project", ID: "501", Number: 3, Title: "Lost bear", Body: "Find him",
		State: Open, Author: User{Login: "mrbean", WebURL: "https://gitlab.example/mrbean"},
		Assignees: []User{{Login: "irma", WebURL: "https://gitlab.example/irma"}},
		Labels:    []string{"bug", "urgent"}, Milestone: "v2", Comments: 4, Locked: true,
		WebURL:    "https://gitlab.example/group/sub/project/-/issues/3",
		CreatedAt: date("2025-01-02T03:04:05.123Z"), UpdatedAt: date("2025-02-03T04:05:06Z"),
	}

	list, err := c.List(ctx, ListOptions{Repository: "/group/sub/project/", State: StateAll})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[1].State != Closed || list[1].Locked || list[1].ClosedAt.IsZero() || list[1].Repository != "group/sub/project" {
		t.Fatalf("List = %+v", list)
	}
	sameIssue(t, "List[0]", list[0], want)
	for _, r := range f.requests() {
		if strings.Contains(r, "state=") || strings.Contains(r, "%252F") {
			t.Errorf("request %s: state all must be omitted and paths encoded once", r)
		}
	}
	got, err := c.Get(ctx, Ref{Repository: "group/sub/project", Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	sameIssue(t, "Get", got, want)

	for state, q := range map[StateFilter]string{"": "state=opened", StateOpen: "state=opened", StateClosed: "state=closed"} {
		if _, err := c.List(ctx, ListOptions{Repository: "group/sub/project", State: state, Labels: []string{"bug"}}); err != nil {
			t.Fatal(err)
		}
		hasRequest(t, f, "GET /api/v4/projects/group%2Fsub%2Fproject/issues?", q, "labels=bug", "per_page=100")
	}

	mine, err := c.List(ctx, ListOptions{Source: "gl"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].Repository != "other/thing" {
		t.Fatalf("List(account) = %+v, want the path from web_url", mine)
	}
	hasRequest(t, f, "GET /api/v4/issues?", "scope=assigned_to_me", "state=opened")
	if _, err := c.List(ctx, ListOptions{Scope: Created}); err != nil {
		t.Fatal(err)
	}
	hasRequest(t, f, "GET /api/v4/issues?", "scope=created_by_me")

	comments, err := c.Comments(ctx, Ref{Source: "gl", Repository: "group/sub/project", Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	wantFirst := Comment{ID: "71", Author: User{Login: "irma", WebURL: "https://gitlab.example/irma"}, Body: "first",
		WebURL:    "https://gitlab.example/group/sub/project/-/issues/3#note_71",
		CreatedAt: date("2025-04-01T00:00:00Z"), UpdatedAt: date("2025-04-02T00:00:00Z")}
	if len(comments) != 2 || comments[0] != wantFirst || comments[1].ID != "73" {
		t.Fatalf("Comments = %+v, want notes 71 and 73 without the system note", comments)
	}
	hasRequest(t, f, "GET /api/v4/projects/group%2Fsub%2Fproject/issues/3/notes?", "sort=asc", "order_by=created_at", "per_page=100")
}

func TestGitLabSpecialLabels(t *testing.T) {
	f := newFake(t, "/api/v4")
	f.on("GET", "/projects/a%2Fb/issues", []any{
		obj{"iid": 1, "state": "opened", "labels": []any{"None"}},
		obj{"iid": 2, "state": "opened", "labels": []any{"bug"}},
		obj{"iid": 3, "state": "opened", "labels": []any{"any", "bug"}},
	})
	f.on("GET", "/issues", []any{})
	c := newClient(t, Source{ID: "gl", Session: f.session(forgeauth.GitLab)})
	ctx := context.Background()

	// GitLab reads None as "no labels", so it must not be sent.
	list, err := c.List(ctx, ListOptions{Repository: "a/b", Labels: []string{"None"}})
	if err != nil || len(list) != 1 || list[0].Number != 1 {
		t.Fatalf("List(None) = %+v, %v, want issue 1", list, err)
	}
	list, err = c.List(ctx, ListOptions{Repository: "a/b", Labels: []string{"ANY", "bug"}})
	if err != nil || len(list) != 1 || list[0].Number != 3 {
		t.Fatalf("List(Any, bug) = %+v, %v, want issue 3", list, err)
	}
	if _, err := c.List(ctx, ListOptions{Labels: []string{"none"}}); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if strings.Contains(reqs[0], "labels=") || !strings.Contains(reqs[1], "labels=bug&") || strings.Contains(reqs[2], "labels=") {
		t.Errorf("requests = %v, want None and Any left out", reqs)
	}
}

func TestGitLabWrites(t *testing.T) {
	f := newFake(t, "/api/v4")
	f.on("GET", "/projects/group%2Fsub%2Fproject/issues/3", gitlabIssueJSON())
	f.on("POST", "/projects/group%2Fsub%2Fproject/issues", gitlabIssueJSON())
	f.on("PUT", "/projects/group%2Fsub%2Fproject/issues/3", gitlabIssueJSON())
	f.on("POST", "/projects/group%2Fsub%2Fproject/issues/3/notes", obj{"id": 80, "body": "hi", "system": false})
	f.on("GET", "/users", func(r *http.Request) any {
		switch r.URL.Query().Get("username") {
		case "irma":
			return []any{obj{"id": 5, "username": "Irma"}}
		case "bob":
			return []any{obj{"id": 6, "username": "bob"}}
		}
		return []any{}
	})
	c := newClient(t, Source{ID: "gl", Session: f.session(forgeauth.GitLab)})
	ctx := context.Background()
	ref := Ref{Source: "gl", Repository: "group/sub/project", Number: 3}

	if _, err := c.Create(ctx, RepositoryRef{Source: "gl", Repository: "group/sub/project"}, NewIssue{Title: "Bear", Body: "Lost", Labels: []string{"bug", "urgent"}, Assignees: []string{"irma", "bob"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Update(ctx, ref, Update{Title: ptr("New"), Body: ptr("B"), State: ptr(Closed), Labels: &[]string{}, Assignees: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Update(ctx, ref, Update{State: ptr(Open), Labels: &[]string{"a"}, Assignees: &[]string{"bob"}}); err != nil {
		t.Fatal(err)
	}
	wantBodies := []string{
		`{"assignee_ids":[5,6],"description":"Lost","labels":"bug,urgent","title":"Bear"}`,
		`{"assignee_ids":[0],"description":"B","labels":"","state_event":"close","title":"New"}`,
		`{"assignee_ids":[6],"labels":"a","state_event":"reopen"}`,
	}
	if got := f.sent(); !slices.Equal(got, wantBodies) {
		t.Errorf("bodies =\n%v\nwant\n%v", got, wantBodies)
	}
	hasRequest(t, f, "GET /api/v4/users?", "username=irma")

	n := len(f.sent())
	if _, err := c.Create(ctx, RepositoryRef{Source: "gl", Repository: "group/sub/project"}, NewIssue{Title: "x", Assignees: []string{"ghost"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("Create(unknown login) err = %v, want ErrInvalid", err)
	}
	if _, err := c.Update(ctx, ref, Update{Assignees: &[]string{"ghost"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("Update(unknown login) err = %v, want ErrInvalid", err)
	}
	if len(f.sent()) != n {
		t.Errorf("an unknown login still sent a write")
	}

	cm, err := c.AddComment(ctx, ref, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if cm.ID != "80" || cm.WebURL != "https://gitlab.example/group/sub/project/-/issues/3#note_80" {
		t.Errorf("AddComment = %+v", cm)
	}

	// A body of only quick actions runs them and creates no note.
	f.on("POST", "/projects/group%2Fsub%2Fproject/issues/3/notes", reply{code: http.StatusAccepted,
		body: obj{"commands_changes": obj{"state_event": "close"}, "summary": []any{"Closed this issue."}}})
	cm, err = c.AddComment(ctx, ref, "/close")
	if err != nil || cm != (Comment{}) {
		t.Errorf("AddComment(/close) = %+v, %v, want a comment with no ID", cm, err)
	}
}

func forgejoIssueJSON() obj {
	return obj{
		"id": 901, "number": 5, "title": "Weeds", "body": "Pull them", "state": "open",
		"user":      obj{"login": "mrbean", "html_url": "https://codeberg.example/mrbean"},
		"assignees": nil,
		"labels":    []any{obj{"id": 1, "name": "garden"}, obj{"id": 2, "name": "Chore"}},
		"milestone": obj{"title": "spring"}, "comments": 2, "is_locked": true,
		"html_url":     "https://codeberg.example/mrbean/garden/issues/5",
		"repository":   obj{"id": 9, "name": "garden", "owner": "mrbean", "full_name": "mrbean/garden"},
		"pull_request": nil,
		"created_at":   "2023-01-02T03:04:05+01:00", "updated_at": "2023-02-03T04:05:06Z", "closed_at": nil,
	}
}

func TestForgejo(t *testing.T) {
	f := newFake(t, "/api/v1")
	onlyGarden := obj{"id": 902, "number": 6, "title": "Any match", "state": "open", "labels": []any{obj{"name": "garden"}},
		"html_url": "https://codeberg.example/mrbean/garden/issues/6", "repository": obj{"full_name": "mrbean/garden"}}
	pr := obj{"id": 903, "number": 7, "title": "PR", "state": "open", "pull_request": obj{"merged": false}}
	f.on("GET", "/repos/mrbean/garden/issues", [][]any{{forgejoIssueJSON(), pr}, {onlyGarden}})
	f.on("GET", "/repos/mrbean/garden/issues/5", forgejoIssueJSON())
	f.on("GET", "/repos/mrbean/garden/issues/7", pr)
	f.on("GET", "/repos/issues/search", []any{onlyGarden})
	f.on("GET", "/repos/mrbean/garden/issues/5/comments", []any{
		obj{"id": 31, "body": "first", "user": obj{"login": "irma", "html_url": "https://codeberg.example/irma"},
			"html_url": "https://codeberg.example/mrbean/garden/issues/5#issuecomment-31", "created_at": "2023-03-01T00:00:00Z", "updated_at": "2023-03-01T00:00:00Z"},
	})
	c := newClient(t, Source{ID: "fj", Session: f.session(forgeauth.Forgejo)})
	ctx := context.Background()

	want := Issue{
		Source:     SourceInfo{ID: "fj", Kind: forgeauth.Forgejo, Host: "codeberg.example"},
		Repository: "mrbean/garden", ID: "901", Number: 5, Title: "Weeds", Body: "Pull them",
		State: Open, Author: User{Login: "mrbean", WebURL: "https://codeberg.example/mrbean"},
		Assignees: []User{}, Labels: []string{"garden", "Chore"}, Milestone: "spring", Comments: 2, Locked: true,
		WebURL:    "https://codeberg.example/mrbean/garden/issues/5",
		CreatedAt: date("2023-01-02T03:04:05+01:00"), UpdatedAt: date("2023-02-03T04:05:06Z"),
	}

	// The repository list sends no labels, so the client keeps only full matches.
	list, err := c.List(ctx, ListOptions{Repository: "mrbean/garden", Labels: []string{"garden", "chore"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("List = %+v, want only issue 5", list)
	}
	sameIssue(t, "List[0]", list[0], want)
	hasRequest(t, f, "GET /api/v1/repos/mrbean/garden/issues?", "type=issues", "state=open", "limit=50")
	for _, r := range f.requests() {
		if strings.HasPrefix(r, "GET /api/v1/repos/mrbean/garden/issues?") && strings.Contains(r, "labels=") {
			t.Errorf("request %s: the repository list must filter labels on the client", r)
		}
	}
	if list, err := c.List(ctx, ListOptions{Labels: []string{"garden", "chore"}}); err != nil || len(list) != 0 {
		t.Fatalf("List(account, labels) = %+v, %v, want none", list, err)
	}
	hasRequest(t, f, "GET /api/v1/repos/issues/search?", "labels=garden%2Cchore")

	got, err := c.Get(ctx, Ref{Repository: "mrbean/garden", Number: 5})
	if err != nil {
		t.Fatal(err)
	}
	sameIssue(t, "Get", got, want)
	if _, err := c.Get(ctx, Ref{Repository: "mrbean/garden", Number: 7}); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("Get(pull request) err = %v, want ErrNotFound", err)
	}

	mine, err := c.List(ctx, ListOptions{State: StateAll})
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].Number != 6 {
		t.Fatalf("List(account) = %+v", mine)
	}
	hasRequest(t, f, "GET /api/v1/repos/issues/search?", "type=issues", "assigned=true", "state=all", "limit=50")
	if _, err := c.List(ctx, ListOptions{Scope: Created}); err != nil {
		t.Fatal(err)
	}
	hasRequest(t, f, "GET /api/v1/repos/issues/search?", "created=true")
	for _, r := range f.requests() {
		if strings.Contains(r, "created=true") && strings.Contains(r, "assigned=") {
			t.Errorf("request %s mixes scopes", r)
		}
		if strings.Contains(r, "per_page") {
			t.Errorf("request %s should page with limit", r)
		}
	}

	comments, err := c.Comments(ctx, Ref{Repository: "mrbean/garden", Number: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Author.Login != "irma" || comments[0].WebURL != "https://codeberg.example/mrbean/garden/issues/5#issuecomment-31" {
		t.Fatalf("Comments = %+v", comments)
	}
}

func TestForgejoWrites(t *testing.T) {
	f := newFake(t, "/api/v1")
	f.on("GET", "/repos/mrbean/garden/issues/5", forgejoIssueJSON())
	f.on("POST", "/repos/mrbean/garden/issues", forgejoIssueJSON())
	f.on("PATCH", "/repos/mrbean/garden/issues/5", forgejoIssueJSON())
	f.on("GET", "/repos/mrbean/garden/labels", []any{obj{"id": 3, "name": "bug"}, obj{"id": 4, "name": "Chore"}})
	f.on("GET", "/users/irma", obj{"id": 1, "login": "irma"})
	f.on("PUT", "/repos/mrbean/garden/issues/5/labels", []any{obj{"id": 3, "name": "bug"}})
	f.on("DELETE", "/repos/mrbean/garden/issues/5/labels", http.StatusNoContent)
	f.on("POST", "/repos/mrbean/garden/issues/5/comments", obj{"id": 32, "body": "hi"})
	c := newClient(t, Source{ID: "fj", Session: f.session(forgeauth.Forgejo)})
	ctx := context.Background()
	ref := Ref{Source: "fj", Repository: "mrbean/garden", Number: 5}
	repo := RepositoryRef{Source: "fj", Repository: "mrbean/garden"}

	// Create sends label IDs with the issue; mrbean is not an organization.
	if _, err := c.Create(ctx, repo, NewIssue{Title: "Weeds", Body: "B", Labels: []string{"bug", "nope"}, Assignees: []string{"irma"}}); err != nil {
		t.Fatal(err)
	}
	hasRequest(t, f, "GET /api/v1/repos/mrbean/garden/labels?", "limit=50")
	hasRequest(t, f, "GET /api/v1/orgs/mrbean/labels?")
	iss, err := c.Update(ctx, ref, Update{Labels: &[]string{"bug"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(iss.Labels, []string{"bug"}) {
		t.Errorf("Update labels = %v, want those set", iss.Labels)
	}
	if _, err := c.Update(ctx, ref, Update{Title: ptr("T"), State: ptr(Closed), Labels: &[]string{}, Assignees: &[]string{"irma"}}); err != nil {
		t.Fatal(err)
	}
	wantBodies := []string{
		`{"assignees":["irma"],"body":"B","labels":[3],"title":"Weeds"}`,
		`{"labels":[3]}`,
		`{"assignees":["irma"],"state":"closed","title":"T"}`,
	}
	if got := f.sent(); !slices.Equal(got, wantBodies) {
		t.Errorf("bodies =\n%v\nwant\n%v", got, wantBodies)
	}
	writes := func() []string {
		var out []string
		for _, r := range f.requests() {
			if !strings.HasPrefix(r, "GET") {
				out = append(out, r)
			}
		}
		return out
	}
	wantWrites := []string{
		"POST /api/v1/repos/mrbean/garden/issues?",
		"PUT /api/v1/repos/mrbean/garden/issues/5/labels?",
		"PATCH /api/v1/repos/mrbean/garden/issues/5?",
		"DELETE /api/v1/repos/mrbean/garden/issues/5/labels?",
	}
	if !slices.Equal(writes(), wantWrites) {
		t.Errorf("writes =\n%v\nwant\n%v", writes(), wantWrites)
	}
	hasRequest(t, f, "GET /api/v1/users/irma?")

	// Organization labels count too.
	f.on("GET", "/orgs/mrbean/labels", []any{obj{"id": 9, "name": "team"}})
	if _, err := c.Create(ctx, repo, NewIssue{Title: "x", Labels: []string{"team", "bug"}}); err != nil {
		t.Fatal(err)
	}
	if b := f.sent(); b[len(b)-1] != `{"body":"","labels":[3,9],"title":"x"}` {
		t.Errorf("create body = %s", b[len(b)-1])
	}

	cm, err := c.AddComment(ctx, ref, "hi")
	if err != nil || cm.ID != "32" {
		t.Errorf("AddComment = %+v, %v", cm, err)
	}

	// An unknown assignee is ErrInvalid before any write.
	n := len(writes())
	if _, err := c.Update(ctx, ref, Update{Labels: &[]string{"bug"}, Assignees: &[]string{"ghost"}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("Update(unknown login) err = %v, want ErrInvalid", err)
	}
	// A rejected PATCH leaves the labels alone.
	f.on("PATCH", "/repos/mrbean/garden/issues/5", http.StatusPreconditionFailed)
	if _, err := c.Update(ctx, ref, Update{State: ptr(Closed), Labels: &[]string{"bug"}}); err == nil {
		t.Error("Update(PATCH fails) err = nil")
	}
	if w := writes(); len(w) != n+1 || !strings.HasPrefix(w[n], "PATCH") {
		t.Errorf("writes after failures = %v, want only the PATCH", w[n:])
	}
	// Labels that fail after the PATCH say the issue was updated.
	f.on("PATCH", "/repos/mrbean/garden/issues/5", forgejoIssueJSON())
	f.on("PUT", "/repos/mrbean/garden/issues/5/labels", http.StatusForbidden)
	_, err = c.Update(ctx, ref, Update{Title: ptr("T"), Labels: &[]string{"bug"}})
	if !errors.Is(err, forgehttp.ErrForbidden) || !strings.Contains(err.Error(), "issue updated, but labels failed") {
		t.Errorf("Update(labels fail) err = %v", err)
	}
}

func TestAcrossSources(t *testing.T) {
	gh := newFake(t, "")
	gh.on("GET", "/repos/mrbean/teddy/issues", []any{obj{"number": 1, "title": "gh", "state": "open", "html_url": "https://github.com/mrbean/teddy/issues/1"}})
	gh.on("GET", "/repos/mrbean/teddy/issues/1", obj{"number": 1, "state": "open", "html_url": "https://github.com/mrbean/teddy/issues/1"})
	gh.on("GET", "/issues", []any{obj{"number": 5, "repository": obj{"full_name": "a/b"}}})
	gl := newFake(t, "/api/v4")
	gl.on("GET", "/projects/mrbean%2Fteddy/issues", []any{obj{"iid": 2, "title": "gl", "state": "opened", "web_url": "https://gitlab.com/mrbean/teddy/-/issues/2"}})
	gl.on("GET", "/projects/mrbean%2Fteddy/issues/1", obj{"iid": 1, "state": "opened", "references": obj{"full": "mrbean/teddy#1"}})
	gl.on("GET", "/projects/group%2Fsub%2Fproject/issues", []any{obj{"iid": 3, "state": "opened", "references": obj{"full": "group/sub/project#3"}}})
	gl.on("GET", "/projects/group%2Fsub%2Fproject/issues/3", obj{"iid": 3, "state": "opened", "references": obj{"full": "group/sub/project#3"}})
	gl.on("GET", "/issues", []any{obj{"iid": 6, "references": obj{"full": "c/d#6"}}})
	fj := newFake(t, "/api/v1")
	fj.on("GET", "/repos/issues/search", []any{})
	c := newClient(t,
		Source{ID: "gh", Session: gh.session(forgeauth.GitHub)},
		Source{ID: "gl", Session: gl.session(forgeauth.GitLab)},
		Source{ID: "fj", Session: fj.session(forgeauth.Forgejo)},
	)
	ctx := context.Background()

	key := func(list []Issue) string {
		var out []string
		for _, i := range list {
			out = append(out, fmt.Sprintf("%s:%s#%d", i.Source.ID, i.Repository, i.Number))
		}
		return strings.Join(out, " ")
	}

	// Forgejo lacks the repository and is skipped.
	list, err := c.List(ctx, ListOptions{Repository: "mrbean/teddy"})
	if err != nil {
		t.Fatal(err)
	}
	if k := key(list); k != "gh:mrbean/teddy#1 gl:mrbean/teddy#2" {
		t.Fatalf("List = %s", k)
	}
	if _, err := c.List(ctx, ListOptions{Repository: "mrbean/teddy", Source: "fj"}); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("List(source without repo) err = %v, want ErrNotFound", err)
	}
	if _, err := c.List(ctx, ListOptions{Repository: "nobody/here"}); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("List(missing repo) err = %v, want ErrNotFound", err)
	}

	// A nested path cannot exist on GitHub or Forgejo, so only GitLab is asked.
	list, err = c.List(ctx, ListOptions{Repository: "group/sub/project"})
	if err != nil || key(list) != "gl:group/sub/project#3" {
		t.Fatalf("List(nested) = %s, %v", key(list), err)
	}
	if r, err := c.Get(ctx, Ref{Repository: "group/sub/project", Number: 3}); err != nil || r.Source.ID != "gl" {
		t.Fatalf("Get(nested) = %+v, %v", r, err)
	}
	if _, err := c.Get(ctx, Ref{Source: "gh", Repository: "group/sub/project", Number: 3}); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("Get(nested on GitHub) err = %v", err)
	}
	for _, r := range append(gh.requests(), fj.requests()...) {
		if strings.Contains(r, "group") {
			t.Errorf("asked for a nested path: %s", r)
		}
	}

	all, err := c.List(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if k := key(all); k != "gh:a/b#5 gl:c/d#6" {
		t.Fatalf("List(account) = %s", k)
	}

	if _, err := c.Get(ctx, Ref{Repository: "mrbean/teddy", Number: 1}); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("Get(ambiguous) err = %v", err)
	}
	if _, err := c.Comments(ctx, Ref{Repository: "mrbean/teddy", Number: 1}); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("Comments(ambiguous) err = %v", err)
	}
	if r, err := c.Get(ctx, Ref{Source: "gl", Repository: "mrbean/teddy", Number: 1}); err != nil || r.Source.Kind != forgeauth.GitLab {
		t.Errorf("Get(gl) = %+v, %v", r, err)
	}
	if _, err := c.Get(ctx, Ref{Repository: "mrbean/teddy", Number: 99}); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("Get(missing) err = %v", err)
	}
	var fe *forgehttp.Error
	if _, err := c.Get(ctx, Ref{Source: "gh", Repository: "mrbean/teddy", Number: 99}); !errors.As(err, &fe) || fe.StatusCode != http.StatusNotFound {
		t.Errorf("Get(gh missing) err = %v, want a *forgehttp.Error", err)
	}
}

func TestSourceErrorFailsCall(t *testing.T) {
	gh := newFake(t, "")
	gh.on("GET", "/issues", []any{})
	bad := newFake(t, "")
	bad.on("GET", "/repos/issues/search", http.StatusForbidden)
	bad.on("GET", "/repos/a/b/issues/1", http.StatusForbidden)
	c := newClient(t,
		Source{ID: "gh", Session: gh.session(forgeauth.GitHub)},
		Source{ID: "fj", Session: bad.session(forgeauth.Forgejo)},
	)
	_, err := c.List(context.Background(), ListOptions{})
	if !errors.Is(err, forgehttp.ErrForbidden) || !strings.Contains(err.Error(), "source fj") {
		t.Fatalf("List err = %v, want forbidden from source fj", err)
	}
	// A forbidden source is not treated as "not there" when searching.
	if _, err := c.Get(context.Background(), Ref{Repository: "a/b", Number: 1}); !errors.Is(err, forgehttp.ErrForbidden) {
		t.Fatalf("Get err = %v, want forbidden", err)
	}
}

func TestProxyBasePath(t *testing.T) {
	f := newFake(t, "/c/gh-mrbean")
	f.on("GET", "/repos/mrbean/teddy/issues", [][]any{
		{obj{"number": 1, "state": "open"}},
		{obj{"number": 2, "state": "open"}},
	})
	f.on("GET", "/repos/mrbean/teddy/issues/2", obj{"number": 2, "state": "open", "html_url": "https://github.com/mrbean/teddy/issues/2"})
	f.on("POST", "/repos/mrbean/teddy/issues/2/comments", obj{"id": 1, "body": "hi"})
	c := newClient(t, Source{ID: "gh-mrbean", Session: f.session(forgeauth.GitHub)})
	ctx := context.Background()

	list, err := c.List(ctx, ListOptions{Repository: "mrbean/teddy"})
	if err != nil || len(list) != 2 || list[1].Repository != "mrbean/teddy" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	got, err := c.Get(ctx, Ref{Repository: "mrbean/teddy", Number: 2})
	if err != nil || got.Source.Host != "github.com" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := c.AddComment(ctx, got.Ref(), "hi"); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.requests() {
		if !strings.Contains(r, " /c/gh-mrbean/repos/") {
			t.Errorf("request %s left the proxy connection", r)
		}
	}
}

func TestPaginationStaysOnSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `</c/other/issues>; rel="next"`)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := newClient(t, Source{ID: "gh", Session: &forgeauth.Session{Kind: forgeauth.GitHub, BaseURL: srv.URL + "/c/mine", Client: srv.Client()}})
	if _, err := c.List(context.Background(), ListOptions{}); err == nil || !strings.Contains(err.Error(), "leaves the source") {
		t.Fatalf("List err = %v, want a pagination error", err)
	}
}

func TestValidation(t *testing.T) {
	f := newFake(t, "")
	c := newClient(t, Source{ID: "gh", Session: f.session(forgeauth.GitHub)})
	ctx := context.Background()
	repo := RepositoryRef{Source: "gh", Repository: "mrbean/teddy"}
	ref := Ref{Source: "gh", Repository: "mrbean/teddy", Number: 1}

	for _, p := range []string{"", "solo", "a//b", "a/../b", "a/./b", "a/b%2Fc", "a/b?x", "a/b#x", `a\b/c`, "a/b c", "a/b\tc"} {
		if _, err := c.Get(ctx, Ref{Repository: p, Number: 1}); !errors.Is(err, ErrInvalid) {
			t.Errorf("Get(%q) err = %v, want ErrInvalid", p, err)
		}
		if p == "" {
			continue // an empty repository lists account-wide
		}
		if _, err := c.List(ctx, ListOptions{Repository: p}); !errors.Is(err, ErrInvalid) {
			t.Errorf("List(%q) err = %v, want ErrInvalid", p, err)
		}
	}

	calls := map[string]func() error{
		"get number 0":      func() error { _, err := c.Get(ctx, Ref{Repository: "a/b"}); return err },
		"get number -1":     func() error { _, err := c.Get(ctx, Ref{Repository: "a/b", Number: -1}); return err },
		"list bad state":    func() error { _, err := c.List(ctx, ListOptions{State: "opened"}); return err },
		"list bad scope":    func() error { _, err := c.List(ctx, ListOptions{Scope: "mentioned"}); return err },
		"list scope + repo": func() error { _, err := c.List(ctx, ListOptions{Repository: "a/b", Scope: Created}); return err },
		"list empty label":  func() error { _, err := c.List(ctx, ListOptions{Labels: []string{" "}}); return err },
		"list comma label":  func() error { _, err := c.List(ctx, ListOptions{Labels: []string{"a,b"}}); return err },
		"create no source": func() error {
			_, err := c.Create(ctx, RepositoryRef{Repository: "a/b"}, NewIssue{Title: "x"})
			return err
		},
		"create no title": func() error { _, err := c.Create(ctx, repo, NewIssue{Title: " "}); return err },
		"create bad path": func() error {
			_, err := c.Create(ctx, RepositoryRef{Source: "gh", Repository: "a"}, NewIssue{Title: "x"})
			return err
		},
		"create bad label": func() error { _, err := c.Create(ctx, repo, NewIssue{Title: "x", Labels: []string{""}}); return err },
		"create bad login": func() error {
			_, err := c.Create(ctx, repo, NewIssue{Title: "x", Assignees: []string{"a,b"}})
			return err
		},
		"update no source": func() error {
			_, err := c.Update(ctx, Ref{Repository: "a/b", Number: 1}, Update{Title: ptr("x")})
			return err
		},
		"update empty":       func() error { _, err := c.Update(ctx, ref, Update{}); return err },
		"update empty title": func() error { _, err := c.Update(ctx, ref, Update{Title: ptr("")}); return err },
		"update bad state":   func() error { _, err := c.Update(ctx, ref, Update{State: ptr(State("opened"))}); return err },
		"update bad label":   func() error { _, err := c.Update(ctx, ref, Update{Labels: &[]string{"a,b"}}); return err },
		"update bad login":   func() error { _, err := c.Update(ctx, ref, Update{Assignees: &[]string{" "}}); return err },
		"update number 0": func() error {
			_, err := c.Update(ctx, Ref{Source: "gh", Repository: "a/b"}, Update{Title: ptr("x")})
			return err
		},
		"comment no source": func() error { _, err := c.AddComment(ctx, Ref{Repository: "a/b", Number: 1}, "x"); return err },
		"comment empty":     func() error { _, err := c.AddComment(ctx, ref, " \n"); return err },
		"comments bad path": func() error { _, err := c.Comments(ctx, Ref{Repository: "a", Number: 1}); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("invalid arguments sent %d requests: %v", n, f.requests())
	}

	unknown := map[string]func() error{
		"list": func() error { _, err := c.List(ctx, ListOptions{Source: "nope"}); return err },
		"get":  func() error { _, err := c.Get(ctx, Ref{Source: "nope", Repository: "a/b", Number: 1}); return err },
		"create": func() error {
			_, err := c.Create(ctx, RepositoryRef{Source: "nope", Repository: "a/b"}, NewIssue{Title: "x"})
			return err
		},
		"update": func() error {
			_, err := c.Update(ctx, Ref{Source: "nope", Repository: "a/b", Number: 1}, Update{Title: ptr("x")})
			return err
		},
		"comment": func() error {
			_, err := c.AddComment(ctx, Ref{Source: "nope", Repository: "a/b", Number: 1}, "x")
			return err
		},
	}
	for name, call := range unknown {
		if err := call(); !errors.Is(err, ErrUnknownSource) {
			t.Errorf("%s: err = %v, want ErrUnknownSource", name, err)
		}
	}
	if _, err := c.Create(ctx, RepositoryRef{Source: "gh", Repository: "a/b/c"}, NewIssue{Title: "x"}); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("Create(nested on GitHub) err = %v, want ErrNotFound", err)
	}
}

func TestNew(t *testing.T) {
	f := newFake(t, "")
	ok := f.session(forgeauth.GitHub)
	cases := map[string][]Source{
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
	none := newClient(t)
	if list, err := none.List(context.Background(), ListOptions{}); err != nil || len(list) != 0 {
		t.Errorf("List without sources = %v, %v", list, err)
	}
	if _, err := none.Get(context.Background(), Ref{Repository: "a/b", Number: 1}); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("Get without sources = %v", err)
	}
	c := newClient(t, Source{ID: "a", Session: ok}, Source{ID: "b", Session: ok})
	if s := c.Sources(); len(s) != 2 || s[0].ID != "a" || s[1].ID != "b" {
		t.Errorf("Sources = %+v", s)
	}
}

func TestDecodeErrors(t *testing.T) {
	bad := []any{obj{"number": "seven"}}
	gh := newFake(t, "")
	gh.on("GET", "/repos/a/b/issues", bad)
	gh.on("GET", "/repos/a/b/issues/1", obj{"number": 1, "state": "open"})
	gh.on("GET", "/repos/a/b/issues/1/comments", []any{obj{"id": "x"}})
	gh.on("POST", "/repos/a/b/issues/1/comments", obj{"id": "x"})
	c := newClient(t, Source{ID: "gh", Session: gh.session(forgeauth.GitHub)})
	ctx := context.Background()
	ref := Ref{Source: "gh", Repository: "a/b", Number: 1}
	var syntax *json.UnmarshalTypeError
	if _, err := c.List(ctx, ListOptions{Repository: "a/b"}); !errors.As(err, &syntax) || !strings.Contains(err.Error(), "source gh") {
		t.Errorf("List err = %v, want a decode error from source gh", err)
	}
	if _, err := c.Comments(ctx, ref); !errors.As(err, &syntax) {
		t.Errorf("Comments err = %v, want a decode error", err)
	}
	if _, err := c.AddComment(ctx, ref, "x"); !errors.As(err, &syntax) {
		t.Errorf("AddComment err = %v, want a decode error", err)
	}
}
