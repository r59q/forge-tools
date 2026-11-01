package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-issues/library/issues"
	"github.com/r59q/forge-tools/forge-repositories/library/repositories"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type issueReaderFunc func(context.Context, issues.ListOptions) ([]issues.Issue, error)

func (f issueReaderFunc) List(ctx context.Context, o issues.ListOptions) ([]issues.Issue, error) {
	return f(ctx, o)
}

func page(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	h, err := s.HTTP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", path, nil)
	r.SetBasicAuth("operator", operatorToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestIssueListingStates(t *testing.T) {
	found := []issues.Issue{
		{Source: issues.SourceInfo{ID: "personal", Kind: forgeauth.GitHub, Host: "github.com"}, Repository: "group/repo", Number: 7, Title: "Crash", State: issues.Open,
			Author: issues.User{Login: "ann"}, Assignees: []issues.User{{Login: "bob"}}, Labels: []string{"bug"}, Comments: 2, UpdatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{Source: issues.SourceInfo{ID: "work", Kind: forgeauth.GitLab, Host: "gitlab.example"}, Repository: "group/sub/repo", Number: 7, Title: "Crash", State: issues.Closed},
	}
	var options issues.ListOptions
	for _, tc := range []struct {
		name       string
		reader     management.IssueReader
		query      string
		configured bool
		count      int
		code       codes.Code
		httpCode   int
		message    string
	}{
		{name: "unconfigured", httpCode: 200, message: "No issue capability configured"},
		{name: "empty", reader: issueReaderFunc(func(context.Context, issues.ListOptions) ([]issues.Issue, error) { return nil, nil }), configured: true, httpCode: 200, message: "No issues found"},
		{name: "issues", reader: issueReaderFunc(func(_ context.Context, o issues.ListOptions) ([]issues.Issue, error) {
			options = o
			return found, nil
		}), query: "?state=all&source=work&repository=group/sub/repo", configured: true, count: 2, httpCode: 200, message: "Complete issue listing"},
		{name: "invalid", query: "?scope=all", code: codes.InvalidArgument, httpCode: 400, message: "Invalid filter"},
		{name: "failure", reader: issueReaderFunc(func(context.Context, issues.ListOptions) ([]issues.Issue, error) {
			return found, errors.New("backend-secret-sentinel")
		}), code: codes.Unavailable, httpCode: 503, message: "Issue listing failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, h, client := setupConfig(t, management.Config{Issues: tc.reader}, time.Second)
			ctx, cancel := rpcContext()
			defer cancel()
			req := &pb.ListIssuesRequest{}
			if tc.query != "" {
				q := strings.TrimPrefix(tc.query, "?")
				for _, kv := range strings.Split(q, "&") {
					k, v, _ := strings.Cut(kv, "=")
					switch k {
					case "state":
						req.State = v
					case "scope":
						req.Scope = v
					case "source":
						req.Source = v
					case "repository":
						req.Repository = v
					}
				}
			}
			got, err := client.ListIssues(ctx, req)
			if status.Code(err) != tc.code {
				t.Fatalf("gRPC: %v", err)
			}
			w := request(h, "/v1/issues"+tc.query, operatorToken)
			if w.Code != tc.httpCode {
				t.Fatalf("REST: %d %s", w.Code, w.Body)
			}
			if err == nil {
				var rest pb.ListIssuesResponse
				if err := protojson.Unmarshal(w.Body.Bytes(), &rest); err != nil {
					t.Fatal(err)
				}
				if !proto.Equal(got, &rest) || got.Configured != tc.configured || len(got.Issues) != tc.count {
					t.Fatalf("transport mismatch: %v / %v", got, &rest)
				}
				if tc.count == 2 {
					if !reflect.DeepEqual(options, issues.ListOptions{State: issues.StateAll, Source: "work", Repository: "group/sub/repo"}) {
						t.Fatalf("options: %+v", options)
					}
					first := got.Issues[0]
					if first.SourceId != "personal" || got.Issues[1].SourceId != "work" || first.Number != 7 || first.Author != "ann" || first.Assignees[0] != "bob" || first.Labels[0] != "bug" || first.Comments != 2 || first.UpdatedAt == nil {
						t.Fatal(got)
					}
				}
			} else if strings.Contains(w.Body.String(), "Crash") || strings.Contains(w.Body.String(), "sentinel") {
				t.Fatal("failure exposed partial results or error details")
			}
			web := page(t, s, "/issues"+tc.query)
			if web.Code != tc.httpCode || !strings.Contains(web.Body.String(), tc.message) || strings.Contains(web.Body.String(), "sentinel") {
				t.Fatalf("web: %d %s", web.Code, web.Body)
			}
			if tc.code != codes.OK && strings.Contains(web.Body.String(), "Crash") {
				t.Fatal("web exposed partial results")
			}
		})
	}
}

func TestIssueAccessAndRendering(t *testing.T) {
	const attack = `<img src=x onerror="alert(1)">`
	calls := 0
	s, h, client := setupConfig(t, management.Config{Issues: issueReaderFunc(func(context.Context, issues.ListOptions) ([]issues.Issue, error) {
		calls++
		return []issues.Issue{{
			Source: issues.SourceInfo{ID: attack, Kind: forgeauth.Kind(attack), Host: attack}, Repository: attack, Number: 1, Title: attack,
			Body: "body-secret", State: issues.State(attack), Author: issues.User{Login: attack}, Labels: []string{attack}, WebURL: "javascript:alert(1)",
		}}, nil
	})}, time.Second)
	for _, token := range []string{"", "wrong"} {
		if w := request(h, "/v1/issues", token); w.Code != 401 {
			t.Fatalf("REST accepted %q", token)
		}
	}
	if _, err := client.ListIssues(context.Background(), &pb.ListIssuesRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("gRPC accepted a missing token")
	}
	if calls != 0 {
		t.Fatal("unauthorized request reached the backend")
	}
	w := page(t, s, "/issues?repository="+strings.ReplaceAll(`"><b>`, `"`, "%22"))
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, attack) || !strings.Contains(body, "&lt;img") || strings.Contains(body, "body-secret") || strings.Contains(body, "javascript:") || strings.Contains(body, `"><b>`) {
		t.Fatalf("unsafe web content: %s", body)
	}
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		r := httptest.NewRequest(method, "/v1/issues", nil)
		r.Header.Set("Authorization", "Bearer "+operatorToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != 405 {
			t.Fatalf("%s /v1/issues = %d", method, rec.Code)
		}
	}
}

func TestRepositoryIssueLinks(t *testing.T) {
	repos := readerFunc(func(context.Context, repositories.ListOptions) ([]repositories.Repository, error) {
		return []repositories.Repository{{Source: repositories.SourceInfo{ID: "work", Kind: forgeauth.GitLab}, Path: "group/sub repo"}}, nil
	})
	without, _, _ := setupConfig(t, management.Config{Repositories: repos}, time.Second)
	if w := page(t, without, "/"); strings.Contains(w.Body.String(), "/issues?") {
		t.Fatal("issue link without issue capability")
	}
	reader := issueReaderFunc(func(context.Context, issues.ListOptions) ([]issues.Issue, error) { return nil, nil })
	with, _, _ := setupConfig(t, management.Config{Repositories: repos, Issues: reader}, time.Second)
	w := page(t, with, "/")
	if !strings.Contains(w.Body.String(), `href="/issues?source=work&amp;repository=group%2fsub%20repo"`) {
		t.Fatalf("missing or unescaped issue link: %s", w.Body)
	}
}
