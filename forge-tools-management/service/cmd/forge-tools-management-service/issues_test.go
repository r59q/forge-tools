package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-issues/library/issues"
	issueapi "github.com/r59q/forge-tools/forge-issues/service/api"
	issuespb "github.com/r59q/forge-tools/forge-issues/service/issuespb"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestRunWithNativeIssues(t *testing.T) {
	var mode atomic.Int32
	var calls atomic.Int32
	const issue = `{"id":11,"number":7,"title":"<script>alert(1)</script>","body":"body-synthetic","state":"open",` +
		`"user":{"login":"ann"},"assignees":[{"login":"bob"}],"labels":[{"name":"bug"}],"comments":2,` +
		`"html_url":"https://github.example/group/repo/issues/7","repository_url":"https://api.github.example/repos/group/repo",` +
		`"updated_at":"2026-10-01T12:00:00Z"}`
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer forge-synthetic" {
			t.Error("wrong forge credential")
			http.Error(w, "denied", 401)
			return
		}
		switch {
		case mode.Load() == 1:
			http.Error(w, "forge-synthetic issues-synthetic", 500)
		case r.URL.Path == "/issues" && r.URL.Query().Get("filter") == "created" && r.URL.Query().Get("state") == "closed":
			fmt.Fprint(w, `[]`)
		case r.URL.Path == "/issues" && r.URL.Query().Get("filter") == "assigned":
			fmt.Fprint(w, "["+issue+"]")
		case r.URL.Path == "/repos/group/repo/issues":
			fmt.Fprint(w, "["+issue+"]")
		default:
			t.Errorf("unexpected forge request %s", r.URL)
			http.Error(w, "unexpected", 404)
		}
	}))
	defer forge.Close()
	session, err := github.Provider{}.Open(context.Background(), forgeauth.Connection{BaseURL: forge.URL}, forgeauth.StaticToken("forge-synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := issues.New(issues.Source{ID: "personal", Session: session})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := issueapi.New(issueapi.Fixed(client), forgeauth.StaticToken("issues-synthetic"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	issuespb.RegisterIssuesServer(g, backend)
	go func() { _ = g.Serve(l) }()
	defer g.Stop()
	t.Setenv("MANAGEMENT_TEST_ISSUES", "issues-synthetic")
	s, err := parse([]string{"--token-file=" + writeToken(t, "operator-synthetic"), "--http-listen=127.0.0.1:0", "--grpc-listen=127.0.0.1:0", "--issues-address=" + l.Addr().String(), "--issues-token-env=MANAGEMENT_TEST_ISSUES", "--issues-insecure=true"}, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	a := startManagement(t, s)
	for _, tc := range []struct {
		name, query string
		mode        int32
		code, count int
		message     string
	}{
		{"assigned", "", 0, 200, 1, "Complete issue listing"},
		{"repository", "?source=personal&repository=group/repo&state=all", 0, 200, 1, "Complete issue listing"},
		{"empty", "?scope=created&state=closed", 0, 200, 0, "No issues found"},
		{"invalid filter", "?scope=everyone", 0, 400, 0, "Invalid filter"},
		{"upstream failure", "", 1, 503, 0, "Issue listing failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode.Store(tc.mode)
			code, body := httpGet(t, a[1], "/v1/issues"+tc.query, false)
			if code != tc.code {
				t.Fatalf("REST: %d %s", code, body)
			}
			if code == 200 {
				var result pb.ListIssuesResponse
				if err := protojson.Unmarshal([]byte(body), &result); err != nil {
					t.Fatal(err)
				}
				if !result.Configured || len(result.Issues) != tc.count {
					t.Fatal(&result)
				}
				if tc.count == 1 {
					got := result.Issues[0]
					if got.SourceId != "personal" || got.Repository != "group/repo" || got.Number != 7 || got.Author != "ann" ||
						len(got.Assignees) != 1 || got.Assignees[0] != "bob" || len(got.Labels) != 1 || got.Comments != 2 || got.UpdatedAt == nil {
						t.Fatal(got)
					}
				}
			}
			code, body = httpGet(t, a[1], "/issues"+tc.query, true)
			if code != tc.code || !strings.Contains(body, tc.message) || strings.Contains(body, "<script>") {
				t.Fatalf("web: %d %s", code, body)
			}
			for _, secret := range []string{"forge-synthetic", "issues-synthetic", "operator-synthetic", "body-synthetic"} {
				if strings.Contains(body, secret) {
					t.Fatal("secret or issue body leaked in HTML")
				}
			}
		})
	}
	before := calls.Load()
	t.Setenv("MANAGEMENT_TEST_ISSUES", "rejected-issues-synthetic")
	if code, _ := httpGet(t, a[1], "/v1/issues", false); code != 503 {
		t.Fatal("backend rejection was not unavailable")
	}
	if calls.Load() != before {
		t.Fatal("invalid backend credential reached forge")
	}
}
