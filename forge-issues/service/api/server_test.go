package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-issues/library/issues"
	pb "github.com/r59q/forge-tools/forge-issues/service/issuespb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	serviceToken = "service-secret"
	forgeToken   = "forge-secret-sentinel"
)

// request is one request a fake forge received.
type request struct {
	method, path, query, body string
}

// forge serves fixed JSON keyed by "METHOD escaped-path" below prefix and
// records requests. An empty response is 204 No Content. Paths below
// /broken/, /limited/ and /denied/ fail, as do writes below /bad/; their
// bodies must never reach callers.
type forge struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []request
}

func newForge(t *testing.T, prefix string, routes map[string]string) *forge {
	t.Helper()
	f := &forge{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
		key := r.Method + " " + path
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, request{r.Method, path, r.URL.RawQuery, string(body)})
		f.mu.Unlock()
		switch resp, ok := routes[key]; {
		case strings.Contains(key, "/broken/"):
			http.Error(w, `{"message":"upstream exploded with `+forgeToken+`"}`, http.StatusBadGateway)
		case strings.Contains(key, "/limited/"):
			w.Header().Set("X-RateLimit-Remaining", "0")
			http.Error(w, `{"message":"rate limited"}`, http.StatusForbidden)
		case strings.Contains(key, "/denied/"):
			http.Error(w, `{"message":"Must have admin rights"}`, http.StatusForbidden)
		case r.Method != http.MethodGet && strings.Contains(key, "/bad/"):
			http.Error(w, `{"message":"Validation Failed"}`, http.StatusUnprocessableEntity)
		case !ok:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case resp == "":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, resp)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// last returns the last request with method.
func (f *forge) last(t *testing.T, method string) request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.requests) - 1; i >= 0; i-- {
		if f.requests[i].method == method {
			return f.requests[i]
		}
	}
	t.Fatalf("no %s request in %+v", method, f.requests)
	return request{}
}

// sent decodes the body of the last request with method.
func (f *forge) sent(t *testing.T, method string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(f.last(t, method).body), &v); err != nil {
		t.Fatalf("%s body: %v", method, err)
	}
	return v
}

const (
	ghIssue = `{"id":101,"number":1,"title":"Bear is lost","body":"Find him","state":"open",
		"user":{"login":"mrbean","html_url":"https://github.com/mrbean"},
		"assignees":[{"login":"irma","html_url":"https://github.com/irma"}],
		"labels":[{"name":"bug"},"ui"],"milestone":{"title":"v1"},"comments":2,"locked":true,
		"html_url":"https://github.com/mrbean/teddy/issues/1","repository_url":"https://api.github.com/repos/mrbean/teddy",
		"created_at":"2024-01-02T03:04:05Z","updated_at":"2024-01-03T03:04:05Z","closed_at":null}`
	ghClosed = `{"id":101,"number":1,"title":"Bear found","state":"closed","labels":[],
		"html_url":"https://github.com/mrbean/teddy/issues/1","repository_url":"https://api.github.com/repos/mrbean/teddy",
		"closed_at":"2024-02-01T00:00:00Z"}`
	ghPull = `{"id":102,"number":2,"title":"Fix","state":"open","pull_request":{"url":"x"},
		"html_url":"https://github.com/mrbean/teddy/pull/2"}`
	ghComment = `{"id":9,"body":"Seen him","user":{"login":"irma"},"html_url":"https://github.com/mrbean/teddy/issues/1#issuecomment-9","created_at":"2024-01-04T00:00:00Z"}`
	glIssue   = `{"id":700,"iid":7,"title":"CLI crash","description":"Boom","state":"opened",
		"author":{"username":"mrbean","web_url":"https://gitlab.com/mrbean"},"assignees":[],"labels":["bug"],
		"user_notes_count":1,"web_url":"https://gitlab.com/mrbean/tools/cli/-/issues/7",
		"references":{"full":"mrbean/tools/cli#7"},"created_at":"2024-03-01T00:00:00Z"}`
	fjIssue = `{"id":500,"number":5,"title":"Weeds","state":"open","labels":[{"name":"garden"}],
		"html_url":"https://codeberg.org/mrbean/garden/issues/5","repository":{"full_name":"mrbean/garden"}}`
)

// setup serves a GitHub source "gh", a GitLab source "gl" and a Forgejo
// source "fj" through a Server, and returns the Server, its REST handler
// and the fake forges.
func setup(t *testing.T) (*Server, http.Handler, *forge, *forge, *forge) {
	t.Helper()
	gh := newForge(t, "", map[string]string{
		"GET /issues":                                `[` + ghIssue + `,` + ghPull + `]`,
		"GET /repos/mrbean/teddy/issues":             `[` + ghIssue + `,` + ghPull + `]`,
		"POST /repos/mrbean/teddy/issues":            ghIssue,
		"GET /repos/mrbean/teddy/issues/1":           ghIssue,
		"PATCH /repos/mrbean/teddy/issues/1":         ghClosed,
		"GET /repos/mrbean/teddy/issues/2":           ghPull,
		"GET /repos/mrbean/teddy/issues/1/comments":  `[` + ghComment + `]`,
		"POST /repos/mrbean/teddy/issues/1/comments": ghComment,
		"GET /repos/mrbean/shared/issues/4":          `{"id":104,"number":4,"title":"Shared","state":"open","html_url":"https://github.com/mrbean/shared/issues/4"}`,
		"GET /repos/bad/assignee/issues/1":           strings.ReplaceAll(ghIssue, "mrbean/teddy", "bad/assignee"),
	})
	gl := newForge(t, "/api/v4", map[string]string{
		"GET /issues": `[` + glIssue + `]`,
		"GET /projects/mrbean%2Ftools%2Fcli/issues":         `[` + glIssue + `]`,
		"GET /projects/mrbean%2Ftools%2Fcli/issues/7":       glIssue,
		"PUT /projects/mrbean%2Ftools%2Fcli/issues/7":       glIssue,
		"GET /projects/mrbean%2Ftools%2Fcli/issues/7/notes": `[{"id":1,"body":"changed the description","system":true},{"id":2,"body":"Same here","author":{"username":"irma"},"created_at":"2024-03-02T00:00:00Z"}]`,
		"GET /users":                             `[{"id":42,"username":"alice"}]`,
		"GET /projects/mrbean%2Fshared/issues/4": `{"id":804,"iid":4,"title":"Shared","state":"opened","web_url":"https://gitlab.com/mrbean/shared/-/issues/4"}`,
	})
	fj := newForge(t, "/api/v1", map[string]string{
		"GET /repos/issues/search":                    `[` + fjIssue + `]`,
		"GET /repos/mrbean/garden/issues/5":           fjIssue,
		"PATCH /repos/mrbean/garden/issues/5":         fjIssue,
		"GET /repos/mrbean/garden/issues/5/comments":  `[]`,
		"GET /repos/mrbean/garden/labels":             `[{"id":3,"name":"bug"}]`,
		"PUT /repos/mrbean/garden/issues/5/labels":    `[{"name":"bug"}]`,
		"DELETE /repos/mrbean/garden/issues/5/labels": "",
	})
	ctx := context.Background()
	open := func(p forgeauth.Provider, base string) *forgeauth.Session {
		s, err := p.Open(ctx, forgeauth.Connection{BaseURL: base}, forgeauth.StaticToken(forgeToken))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	client, err := issues.New(
		issues.Source{ID: "gh", Session: open(github.Provider{}, gh.srv.URL)},
		issues.Source{ID: "gl", Session: open(gitlab.Provider{}, gl.srv.URL+"/api/v4")},
		issues.Source{ID: "fj", Session: open(forgejo.Provider{}, fj.srv.URL+"/api/v1")},
	)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Fixed(client), forgeauth.StaticToken(serviceToken), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.REST(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return s, h, gh, gl, fj
}

func rest(t *testing.T, h http.Handler, method, target, body, token string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("%s %s: Cache-Control = %q", method, target, got)
	}
	if strings.Contains(w.Body.String(), forgeToken) {
		t.Fatalf("%s %s leaked forge data: %s", method, target, w.Body)
	}
	// protojson varies its whitespace on purpose; compare compact JSON.
	var out bytes.Buffer
	if err := json.Compact(&out, w.Body.Bytes()); err != nil {
		t.Fatalf("%s %s: %v: %s", method, target, err, w.Body)
	}
	return w.Code, out.String()
}

func TestNew(t *testing.T) {
	client, _ := issues.New()
	for _, tc := range []struct {
		sources Sources
		token   forgeauth.Credentials
		timeout time.Duration
	}{{nil, forgeauth.StaticToken("x"), time.Second}, {Fixed(client), nil, time.Second}, {Fixed(client), forgeauth.StaticToken("x"), 0}} {
		if _, err := New(tc.sources, tc.token, tc.timeout); err != ErrInvalid {
			t.Errorf("New(%v, %v) = %v, want ErrInvalid", tc.token, tc.timeout, err)
		}
	}
}

func TestSourcesUnavailable(t *testing.T) {
	failing := func(context.Context) (*issues.Client, error) {
		return nil, errors.New("connector " + forgeToken)
	}
	s, err := New(failing, forgeauth.StaticToken(serviceToken), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.REST(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if code, body := rest(t, h, "GET", "/v1/issues", "", serviceToken); code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %s", code, body)
	}
	if code, _ := rest(t, h, "GET", "/v1/issues", "", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated call reached sources: %d", code)
	}
}

func TestRESTReads(t *testing.T) {
	_, h, gh, gl, fj := setup(t)

	code, body := rest(t, h, "GET", "/v1/sources", "", serviceToken)
	if code != 200 || body != `{"sources":[{"id":"gh","kind":"github"},{"id":"gl","kind":"gitlab"},{"id":"fj","kind":"forgejo"}]}` {
		t.Fatalf("sources: %d %s", code, body)
	}

	// Only GitHub has mrbean/teddy; the others are skipped. The pull
	// request is dropped.
	code, body = rest(t, h, "GET", "/v1/issues?repository=mrbean/teddy&state=all&labels=bug&labels=UI", "", serviceToken)
	want := `{"issues":[{"source":{"id":"gh","kind":"github","host":"github.com"},"repository":"mrbean/teddy","id":"101","number":"1",` +
		`"title":"Bear is lost","body":"Find him","state":"open","author":{"login":"mrbean","webUrl":"https://github.com/mrbean"},` +
		`"assignees":[{"login":"irma","webUrl":"https://github.com/irma"}],"labels":["bug","ui"],"milestone":"v1","comments":2,"locked":true,` +
		`"webUrl":"https://github.com/mrbean/teddy/issues/1","createdAt":"2024-01-02T03:04:05Z","updatedAt":"2024-01-03T03:04:05Z"}]}`
	if code != 200 || body != want {
		t.Fatalf("list:\n got %d %s\nwant %s", code, body, want)
	}
	if q := gh.last(t, "GET").query; q != "labels=bug%2CUI&per_page=100&state=all" {
		t.Errorf("GitHub query = %q", q)
	}

	// Account-wide listings use each forge's own scope parameters.
	code, body = rest(t, h, "GET", "/v1/issues?scope=created&state=closed", "", serviceToken)
	var list struct {
		Issues []struct {
			Repository string
			Source     struct{ ID string }
		}
	}
	if err := json.Unmarshal([]byte(body), &list); code != 200 || err != nil || len(list.Issues) != 3 {
		t.Fatalf("account list: %d %s %v", code, body, err)
	}
	if list.Issues[1].Repository != "mrbean/tools/cli" || list.Issues[2].Source.ID != "fj" {
		t.Errorf("account list = %+v", list.Issues)
	}
	for _, tc := range []struct {
		f    *forge
		want string
	}{
		{gh, "filter=created&per_page=100&state=closed"},
		{gl, "per_page=100&scope=created_by_me&state=closed"},
		{fj, "created=true&limit=50&state=closed&type=issues"},
	} {
		if q := tc.f.last(t, "GET").query; q != tc.want {
			t.Errorf("account query = %q, want %q", q, tc.want)
		}
	}

	code, body = rest(t, h, "GET", "/v1/issues?source=fj", "", serviceToken)
	if code != 200 || !strings.Contains(body, `"repository":"mrbean/garden"`) || strings.Contains(body, "github") {
		t.Errorf("source filter: %d %s", code, body)
	}
	if q := fj.last(t, "GET").query; !strings.Contains(q, "assigned=true") {
		t.Errorf("default scope query = %q", q)
	}

	// A nested GitLab path reaches GitLab as one encoded segment.
	code, body = rest(t, h, "GET", "/v1/issues/7?repository=mrbean/tools/cli", "", serviceToken)
	if code != 200 || !strings.Contains(body, `"repository":"mrbean/tools/cli"`) || !strings.Contains(body, `"state":"open"`) {
		t.Errorf("get nested: %d %s", code, body)
	}

	// GitLab system notes are left out; note URLs come from the issue.
	code, body = rest(t, h, "GET", "/v1/issues/7/comments?repository=mrbean/tools/cli&source=gl", "", serviceToken)
	if want := `{"comments":[{"id":"2","author":{"login":"irma"},"body":"Same here","webUrl":"https://gitlab.com/mrbean/tools/cli/-/issues/7#note_2","createdAt":"2024-03-02T00:00:00Z"}]}`; code != 200 || body != want {
		t.Errorf("gitlab comments: %d %s", code, body)
	}
	code, body = rest(t, h, "GET", "/v1/issues/1/comments?repository=mrbean/teddy", "", serviceToken)
	if code != 200 || !strings.Contains(body, `"webUrl":"https://github.com/mrbean/teddy/issues/1#issuecomment-9"`) {
		t.Errorf("github comments: %d %s", code, body)
	}
	// An issue without comments is an empty list, not an error.
	code, body = rest(t, h, "GET", "/v1/issues/5/comments?repository=mrbean/garden&source=fj", "", serviceToken)
	if code != 200 || body != `{}` {
		t.Errorf("forgejo comments: %d %s", code, body)
	}
}

func TestRESTWrites(t *testing.T) {
	_, h, gh, gl, fj := setup(t)

	code, body := rest(t, h, "POST", "/v1/issues", `{"source":"gh","repository":"mrbean/teddy","title":"Bear is lost","body":"Find him","labels":["bug"],"assignees":["irma"]}`, serviceToken)
	if code != 200 || !strings.Contains(body, `"number":"1"`) {
		t.Fatalf("create: %d %s", code, body)
	}
	if got, _ := json.Marshal(gh.sent(t, "POST")); string(got) != `{"assignees":["irma"],"body":"Find him","labels":["bug"],"title":"Bear is lost"}` {
		t.Errorf("GitHub create got %s", got)
	}

	// Labels set with no values clear; unset assignees are left alone.
	code, body = rest(t, h, "PATCH", "/v1/issues/1", `{"source":"gh","repository":"mrbean/teddy","title":"Bear found","state":"closed","labels":{}}`, serviceToken)
	if code != 200 || !strings.Contains(body, `"state":"closed"`) || !strings.Contains(body, `"closedAt":"2024-02-01T00:00:00Z"`) {
		t.Fatalf("update: %d %s", code, body)
	}
	if got, _ := json.Marshal(gh.sent(t, "PATCH")); string(got) != `{"labels":[],"state":"closed","title":"Bear found"}` {
		t.Errorf("GitHub update got %s", got)
	}

	// GitLab looks up assignee IDs and closes with state_event.
	code, body = rest(t, h, "PATCH", "/v1/issues/7", `{"source":"gl","repository":"mrbean/tools/cli","state":"closed","assignees":{"values":["alice"]},"body":""}`, serviceToken)
	if code != 200 {
		t.Fatalf("gitlab update: %d %s", code, body)
	}
	if got, _ := json.Marshal(gl.sent(t, "PUT")); string(got) != `{"assignee_ids":[42],"description":"","state_event":"close"}` {
		t.Errorf("GitLab update got %s", got)
	}
	if q := gl.last(t, "GET").query; q != "username=alice" {
		t.Errorf("GitLab user lookup = %q", q)
	}

	// Forgejo sets labels by ID, and clears them with DELETE.
	code, body = rest(t, h, "PATCH", "/v1/issues/5", `{"source":"fj","repository":"mrbean/garden","labels":{"values":["bug"]}}`, serviceToken)
	if code != 200 {
		t.Fatalf("forgejo labels: %d %s", code, body)
	}
	if got, _ := json.Marshal(fj.sent(t, "PUT")); string(got) != `{"labels":[3]}` {
		t.Errorf("Forgejo labels got %s", got)
	}
	code, body = rest(t, h, "PATCH", "/v1/issues/5", `{"source":"fj","repository":"mrbean/garden","labels":{"values":[]}}`, serviceToken)
	if code != 200 {
		t.Fatalf("forgejo clear: %d %s", code, body)
	}
	if r := fj.last(t, "DELETE"); r.path != "/repos/mrbean/garden/issues/5/labels" {
		t.Errorf("Forgejo clear = %+v", r)
	}

	code, body = rest(t, h, "POST", "/v1/issues/1/comments", `{"source":"gh","repository":"mrbean/teddy","body":"Seen him"}`, serviceToken)
	if code != 200 || !strings.Contains(body, `"id":"9"`) {
		t.Fatalf("comment: %d %s", code, body)
	}
	if r := gh.last(t, "POST"); r.path != "/repos/mrbean/teddy/issues/1/comments" || r.body != `{"body":"Seen him"}` {
		t.Errorf("GitHub comment got %+v", r)
	}

	// Unknown body fields are rejected rather than silently ignored.
	if code, _ := rest(t, h, "PATCH", "/v1/issues/1", `{"source":"gh","repository":"mrbean/teddy","milestone":"v2"}`, serviceToken); code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", code)
	}
}

func TestRESTErrors(t *testing.T) {
	_, h, _, _, _ := setup(t)
	for _, tc := range []struct {
		method, target, body, token string
		code                        int
	}{
		{"GET", "/v1/sources", "", "", http.StatusUnauthorized},
		{"GET", "/v1/issues", "", "wrong", http.StatusUnauthorized},
		{"GET", "/v1/issues?repository=nobody/nothing", "", serviceToken, http.StatusNotFound},
		{"GET", "/v1/issues/9?repository=mrbean/teddy", "", serviceToken, http.StatusNotFound},
		{"GET", "/v1/issues/2?repository=mrbean/teddy", "", serviceToken, http.StatusNotFound},    // pull request
		{"GET", "/v1/issues/4?repository=mrbean/shared", "", serviceToken, http.StatusBadRequest}, // ambiguous
		{"GET", "/v1/issues/4?repository=mrbean/shared&source=gl", "", serviceToken, http.StatusOK},
		{"GET", "/v1/issues/0?repository=mrbean/teddy", "", serviceToken, http.StatusBadRequest},
		{"GET", "/v1/issues/one?repository=mrbean/teddy", "", serviceToken, http.StatusBadRequest},
		{"GET", "/v1/issues/1?repository=teddy", "", serviceToken, http.StatusBadRequest},
		{"GET", "/v1/issues?source=svn", "", serviceToken, http.StatusBadRequest},
		{"GET", "/v1/issues?state=merged", "", serviceToken, http.StatusBadRequest},
		{"GET", "/v1/issues?repository=mrbean/teddy&scope=created", "", serviceToken, http.StatusBadRequest},
		{"GET", "/v1/issues?labels=a,b", "", serviceToken, http.StatusBadRequest},
		{"GET", "/v1/issues/1/comments?repository=mrbean/teddy&source=nope", "", serviceToken, http.StatusBadRequest},
		{"POST", "/v1/issues", `{"source":"gh","repository":"mrbean/teddy","title":" "}`, serviceToken, http.StatusBadRequest},
		{"POST", "/v1/issues", `{"repository":"mrbean/teddy","title":"x"}`, serviceToken, http.StatusBadRequest},      // no source
		{"PATCH", "/v1/issues/1", `{"source":"gh","repository":"mrbean/teddy"}`, serviceToken, http.StatusBadRequest}, // nothing to update
		{"PATCH", "/v1/issues/1", `{"source":"gh","repository":"mrbean/teddy","state":"merged"}`, serviceToken, http.StatusBadRequest},
		{"PATCH", "/v1/issues/2", `{"source":"gh","repository":"mrbean/teddy","title":"x"}`, serviceToken, http.StatusNotFound}, // pull request
		{"PATCH", "/v1/issues/7", `{"source":"gl","repository":"mrbean/tools/cli","assignees":{"values":["bob"]}}`, serviceToken, http.StatusBadRequest},
		{"PATCH", "/v1/issues/1", `{"source":"gh","repository":"bad/assignee","assignees":{"values":["bob"]}}`, serviceToken, http.StatusBadRequest},
		{"POST", "/v1/issues/1/comments", `{"source":"gh","repository":"mrbean/teddy","body":""}`, serviceToken, http.StatusBadRequest},
		{"POST", "/v1/issues/1/comments", `{"repository":"mrbean/teddy","body":"hi"}`, serviceToken, http.StatusBadRequest},
		{"POST", "/v1/issues", `{"source":"gh","repository":"denied/repo","title":"x"}`, serviceToken, http.StatusForbidden},
		{"GET", "/v1/issues?repository=broken/repo&source=gh", "", serviceToken, http.StatusServiceUnavailable},
		{"GET", "/v1/issues?repository=limited/repo&source=gh", "", serviceToken, http.StatusTooManyRequests},
	} {
		code, body := rest(t, h, tc.method, tc.target, tc.body, tc.token)
		if code != tc.code {
			t.Errorf("%s %s %s: %d %s, want %d", tc.method, tc.target, tc.body, code, body, tc.code)
		}
		if code >= 400 && (strings.Contains(body, "mrbean") || strings.Contains(body, "nobody") || strings.Contains(body, "upstream") || strings.Contains(body, "Validation")) {
			t.Errorf("%s %s: error echoes input or forge response: %s", tc.method, tc.target, body)
		}
	}
}

func TestRPCError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want codes.Code
	}{
		{context.Canceled, codes.Canceled},
		{context.DeadlineExceeded, codes.DeadlineExceeded},
		{fmt.Errorf("x: %w", issues.ErrUnknownSource), codes.InvalidArgument},
		{&forgehttp.Error{StatusCode: http.StatusConflict, Err: forgehttp.ErrConflict}, codes.FailedPrecondition},
		{&forgehttp.Error{StatusCode: http.StatusBadRequest}, codes.InvalidArgument},
		{&forgehttp.Error{StatusCode: http.StatusUnauthorized, Err: forgehttp.ErrUnauthorized}, codes.Unavailable},
		{&forgehttp.Error{StatusCode: http.StatusGone}, codes.Unavailable},
	} {
		err := rpcError(tc.err)
		if status.Code(err) != tc.want || status.Convert(err).Message() != tc.want.String() {
			t.Errorf("rpcError(%v) = %v, want %v", tc.err, err, tc.want)
		}
	}
}

func TestMetadataHeadersCannotAuthenticate(t *testing.T) {
	_, h, _, _, _ := setup(t)
	r := httptest.NewRequest("GET", "/v1/sources", nil)
	r.Header.Set("Grpc-Metadata-Authorization", "Bearer "+serviceToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Grpc-Metadata-Authorization accepted: %d", w.Code)
	}
}

func TestGRPC(t *testing.T) {
	s, _, gh, _, _ := setup(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterIssuesServer(srv, s)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c := pb.NewIssuesClient(conn)

	if _, err := c.ListSources(context.Background(), &emptypb.Empty{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no token: %v", err)
	}
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+serviceToken)
	iss, err := c.GetIssue(ctx, &pb.IssueRequest{Repository: "mrbean/tools/cli", Number: 7})
	if err != nil || iss.GetSource().GetId() != "gl" || iss.GetNumber() != 7 || iss.GetAuthor().GetLogin() != "mrbean" || iss.GetClosedAt() != nil {
		t.Fatalf("get: %v %v", iss, err)
	}
	list, err := c.ListIssues(ctx, &pb.ListIssuesRequest{Repository: "mrbean/teddy", Labels: []string{"bug"}})
	if err != nil || len(list.GetIssues()) != 1 || list.GetIssues()[0].GetComments() != 2 {
		t.Fatalf("list: %v %v", list, err)
	}

	// Assignees set with no values clear; unset labels are left alone.
	title := "Bear found"
	iss, err = c.UpdateIssue(ctx, &pb.UpdateIssueRequest{Source: "gh", Repository: "mrbean/teddy", Number: 1, Title: &title, Assignees: &pb.StringList{}})
	if err != nil || iss.GetState() != "closed" {
		t.Fatalf("update: %v %v", iss, err)
	}
	if got, _ := json.Marshal(gh.sent(t, "PATCH")); string(got) != `{"assignees":[],"title":"Bear found"}` {
		t.Errorf("GitHub update got %s", got)
	}

	cm, err := c.AddComment(ctx, &pb.AddCommentRequest{Source: "gh", Repository: "mrbean/teddy", Number: 1, Body: "Seen him"})
	if err != nil || cm.GetAuthor().GetLogin() != "irma" {
		t.Fatalf("comment: %v %v", cm, err)
	}
	cms, err := c.ListComments(ctx, &pb.IssueRequest{Repository: "mrbean/teddy", Number: 1})
	if err != nil || len(cms.GetComments()) != 1 {
		t.Fatalf("comments: %v %v", cms, err)
	}
	_, err = c.CreateIssue(ctx, &pb.CreateIssueRequest{Source: "gh", Repository: "mrbean/teddy"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("create without title: %v", err)
	}
	_, err = c.GetIssue(ctx, &pb.IssueRequest{Repository: "mrbean/shared", Number: 4})
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != codes.FailedPrecondition.String() {
		t.Fatalf("ambiguous: %v", err)
	}
}
