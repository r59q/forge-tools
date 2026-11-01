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
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-repositories/library/repositories"
	pb "github.com/r59q/forge-tools/forge-repositories/service/repositoriespb"
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

// forge serves fixed JSON keyed by "METHOD escaped-path" below prefix and
// records request bodies. A "secret" route answers with a body that must
// never reach callers.
type forge struct {
	srv    *httptest.Server
	routes map[string]string

	mu     sync.Mutex
	bodies []string
}

func newForge(t *testing.T, prefix string, routes map[string]string) *forge {
	t.Helper()
	f := &forge{routes: routes}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + strings.TrimPrefix(r.URL.EscapedPath(), prefix)
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(body))
		f.mu.Unlock()
		switch resp, ok := routes[key]; {
		case strings.HasSuffix(key, "/broken/repo"):
			http.Error(w, `{"message":"upstream exploded with `+forgeToken+`"}`, http.StatusBadGateway)
		case strings.HasSuffix(key, "/limited/repo"):
			w.Header().Set("X-RateLimit-Remaining", "0")
			http.Error(w, `{"message":"rate limited"}`, http.StatusForbidden)
		case strings.HasSuffix(key, "/bad/branch"):
			http.Error(w, `{"message":"Validation Failed"}`, http.StatusUnprocessableEntity)
		case !ok:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, resp)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// setup serves a GitHub source "gh" and a GitLab source "gl" through a
// Server, and returns the Server and its REST handler.
func setup(t *testing.T) (*Server, http.Handler, *forge, *forge) {
	t.Helper()
	gh := newForge(t, "", map[string]string{
		"GET /user/repos":                  `[{"id":1,"full_name":"mrbean/teddy","default_branch":"main","visibility":"public","html_url":"https://github.com/mrbean/teddy","created_at":"2024-01-02T03:04:05Z"},{"id":3,"full_name":"mrbean/shared","html_url":"https://github.com/mrbean/shared"}]`,
		"GET /repos/mrbean/teddy":          `{"id":1,"full_name":"mrbean/teddy","default_branch":"main","html_url":"https://github.com/mrbean/teddy"}`,
		"PATCH /repos/mrbean/teddy":        `{"id":1,"full_name":"mrbean/teddy","description":"bear","default_branch":"dev","html_url":"https://github.com/mrbean/teddy"}`,
		"GET /repos/mrbean/shared":         `{"id":3,"full_name":"mrbean/shared","html_url":"https://github.com/mrbean/shared"}`,
		"GET /repos/mrbean/teddy/tags":     `[{"name":"v1","commit":{"sha":"abc"}}]`,
		"GET /repos/mrbean/teddy/branches": `[{"name":"main","commit":{"sha":"m41n"},"protected":true},{"name":"dev","commit":{"sha":"d3v"}}]`,
	})
	gl := newForge(t, "/api/v4", map[string]string{
		"GET /projects":                                          `[{"id":7,"path_with_namespace":"mrbean/tools/cli","default_branch":"main","visibility":"internal","web_url":"https://gitlab.com/mrbean/tools/cli"}]`,
		"GET /projects/mrbean%2Ftools%2Fcli":                     `{"id":7,"path_with_namespace":"mrbean/tools/cli","default_branch":"main","web_url":"https://gitlab.com/mrbean/tools/cli"}`,
		"GET /projects/mrbean%2Ftools%2Fcli/repository/branches": `[{"name":"main","commit":{"id":"m41n"}}]`,
		"GET /projects/mrbean%2Fshared":                          `{"id":8,"path_with_namespace":"mrbean/shared","web_url":"https://gitlab.com/mrbean/shared"}`,
	})
	ctx := context.Background()
	ghs, err := github.Provider{}.Open(ctx, forgeauth.Connection{BaseURL: gh.srv.URL}, forgeauth.StaticToken(forgeToken))
	if err != nil {
		t.Fatal(err)
	}
	gls, err := gitlab.Provider{}.Open(ctx, forgeauth.Connection{BaseURL: gl.srv.URL + "/api/v4"}, forgeauth.StaticToken(forgeToken))
	if err != nil {
		t.Fatal(err)
	}
	client, err := repositories.New(repositories.Source{ID: "gh", Session: ghs}, repositories.Source{ID: "gl", Session: gls})
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
	return s, h, gh, gl
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
	client, _ := repositories.New()
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
	failing := func(context.Context) (*repositories.Client, error) {
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
	if code, body := rest(t, h, "GET", "/v1/repositories", "", serviceToken); code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %s", code, body)
	}
	if code, _ := rest(t, h, "GET", "/v1/repositories", "", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated call reached sources: %d", code)
	}
}

func TestRESTReads(t *testing.T) {
	_, h, _, _ := setup(t)

	code, body := rest(t, h, "GET", "/v1/sources", "", serviceToken)
	if code != 200 || body != `{"sources":[{"id":"gh","kind":"github"},{"id":"gl","kind":"gitlab"}]}` {
		t.Fatalf("sources: %d %s", code, body)
	}

	code, body = rest(t, h, "GET", "/v1/repositories?owner=MrBean", "", serviceToken)
	var list struct {
		Repositories []struct {
			Path, Visibility, WebURL, CreatedAt string
			Source                              struct{ ID, Kind, Host string }
		}
	}
	if err := json.Unmarshal([]byte(body), &list); code != 200 || err != nil || len(list.Repositories) != 3 {
		t.Fatalf("list: %d %s %v", code, body, err)
	}
	first, last := list.Repositories[0], list.Repositories[2]
	if first.Path != "mrbean/teddy" || first.Source.Kind != "github" || first.Source.Host != "github.com" || first.CreatedAt != "2024-01-02T03:04:05Z" {
		t.Errorf("first = %+v", first)
	}
	if last.Path != "mrbean/tools/cli" || last.Source.ID != "gl" || last.Visibility != "internal" {
		t.Errorf("last = %+v", last)
	}

	code, body = rest(t, h, "GET", "/v1/repositories?source=gl", "", serviceToken)
	if code != 200 || strings.Contains(body, "github") {
		t.Errorf("source filter: %d %s", code, body)
	}

	// A nested GitLab path in the URL reaches GitLab as one encoded segment.
	code, body = rest(t, h, "GET", "/v1/repositories/mrbean/tools/cli", "", serviceToken)
	if code != 200 || !strings.Contains(body, `"path":"mrbean/tools/cli"`) || !strings.Contains(body, `"webUrl":"https://gitlab.com/mrbean/tools/cli"`) {
		t.Errorf("get nested: %d %s", code, body)
	}
	code, body = rest(t, h, "GET", "/v1/branches/mrbean/tools/cli?source=gl", "", serviceToken)
	if code != 200 || body != `{"branches":[{"name":"main","sha":"m41n","default":true}]}` {
		t.Errorf("gitlab branches: %d %s", code, body)
	}

	code, body = rest(t, h, "GET", "/v1/branches/mrbean/teddy", "", serviceToken)
	if code != 200 || body != `{"branches":[{"name":"main","sha":"m41n","default":true,"protected":true},{"name":"dev","sha":"d3v"}]}` {
		t.Errorf("github branches: %d %s", code, body)
	}
	code, body = rest(t, h, "GET", "/v1/tags/mrbean/teddy", "", serviceToken)
	if code != 200 || body != `{"tags":[{"name":"v1","sha":"abc"}]}` {
		t.Errorf("tags: %d %s", code, body)
	}
}

func TestRESTUpdate(t *testing.T) {
	_, h, gh, _ := setup(t)
	code, body := rest(t, h, "PATCH", "/v1/repositories/mrbean/teddy", `{"source":"gh","description":"bear","defaultBranch":"dev"}`, serviceToken)
	if code != 200 || !strings.Contains(body, `"defaultBranch":"dev"`) {
		t.Fatalf("update: %d %s", code, body)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(gh.bodies[len(gh.bodies)-1]), &sent); err != nil || sent["description"] != "bear" || sent["default_branch"] != "dev" {
		t.Errorf("GitHub got %q", gh.bodies)
	}
	// Unknown body fields are rejected rather than silently ignored.
	if code, _ := rest(t, h, "PATCH", "/v1/repositories/mrbean/teddy", `{"name":"renamed"}`, serviceToken); code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", code)
	}
}

func TestRESTErrors(t *testing.T) {
	_, h, _, _ := setup(t)
	for _, tc := range []struct {
		method, target, body, token string
		code                        int
	}{
		{"GET", "/v1/sources", "", "", http.StatusUnauthorized},
		{"GET", "/v1/repositories", "", "wrong", http.StatusUnauthorized},
		{"GET", "/v1/repositories/nobody/nothing", "", serviceToken, http.StatusNotFound},
		{"GET", "/v1/repositories/mrbean/shared", "", serviceToken, http.StatusBadRequest}, // ambiguous
		{"GET", "/v1/repositories/mrbean/shared?source=gl", "", serviceToken, http.StatusOK},
		{"GET", "/v1/repositories?source=svn", "", serviceToken, http.StatusBadRequest},
		{"GET", "/v1/tags/mrbean/teddy?source=nope", "", serviceToken, http.StatusBadRequest},
		{"PATCH", "/v1/repositories/mrbean/teddy", `{"source":"gh"}`, serviceToken, http.StatusBadRequest}, // nothing to update
		{"PATCH", "/v1/repositories/bad/branch", `{"source":"gh","defaultBranch":"nope"}`, serviceToken, http.StatusBadRequest},
		{"GET", "/v1/repositories/broken/repo?source=gh", "", serviceToken, http.StatusServiceUnavailable},
		{"GET", "/v1/repositories/limited/repo?source=gh", "", serviceToken, http.StatusTooManyRequests},
	} {
		code, body := rest(t, h, tc.method, tc.target, tc.body, tc.token)
		if code != tc.code {
			t.Errorf("%s %s: %d %s, want %d", tc.method, tc.target, code, body, tc.code)
		}
		if code >= 400 && (strings.Contains(body, "mrbean") || strings.Contains(body, "nobody") || strings.Contains(body, "upstream")) {
			t.Errorf("%s %s: error echoes input or forge response: %s", tc.method, tc.target, body)
		}
	}
}

func TestMetadataHeadersCannotAuthenticate(t *testing.T) {
	_, h, _, _ := setup(t)
	r := httptest.NewRequest("GET", "/v1/sources", nil)
	r.Header.Set("Grpc-Metadata-Authorization", "Bearer "+serviceToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Grpc-Metadata-Authorization accepted: %d", w.Code)
	}
}

func TestGRPC(t *testing.T) {
	s, _, _, _ := setup(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterRepositoriesServer(srv, s)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c := pb.NewRepositoriesClient(conn)

	if _, err := c.ListSources(context.Background(), &emptypb.Empty{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no token: %v", err)
	}
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+serviceToken)
	repo, err := c.GetRepository(ctx, &pb.RepositoryRequest{Path: "mrbean/tools/cli"})
	if err != nil || repo.GetSource().GetId() != "gl" || repo.GetDefaultBranch() != "main" {
		t.Fatalf("get: %v %v", repo, err)
	}
	tags, err := c.ListTags(ctx, &pb.RepositoryRequest{Path: "mrbean/teddy", Source: "gh"})
	if err != nil || len(tags.GetTags()) != 1 || tags.GetTags()[0].GetSha() != "abc" {
		t.Fatalf("tags: %v %v", tags, err)
	}
	_, err = c.GetRepository(ctx, &pb.RepositoryRequest{Path: "mrbean/shared"})
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != codes.FailedPrecondition.String() {
		t.Fatalf("ambiguous: %v", err)
	}
}
