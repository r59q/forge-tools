package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

func testClient(t *testing.T, kind forgeauth.Kind, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, err := New(Source{ID: "source", Session: &forgeauth.Session{Kind: kind, BaseURL: s.URL, Client: s.Client()}})
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func TestForgeConformance(t *testing.T) {
	for _, tc := range []struct {
		kind       forgeauth.Kind
		repository string
		base       string
		pipeline   string
		job        string
		artifact   string
	}{
		{forgeauth.GitHub, "o/r", "/repos/o/r",
			`{"id":7,"display_title":"Build","head_sha":"abc","head_branch":"main","workflow_id":3,"event":"push","status":"completed","conclusion":"success","run_attempt":2,"created_at":"2026-01-02T03:04:05Z"}`,
			`{"id":9,"run_id":7,"name":"test","status":"completed","conclusion":"failure","steps":[{"number":1,"name":"go test","status":"completed","conclusion":"failure"}]}`,
			`{"id":11,"name":"build.zip","size_in_bytes":3,"expired":false}`},
		{forgeauth.GitLab, "group/sub/r", "/projects/group%2Fsub%2Fr",
			`{"id":7,"name":"Build","sha":"abc","ref":"main","source":"push","status":"success","created_at":"2026-01-02T03:04:05Z"}`,
			`{"id":9,"pipeline":{"id":7},"name":"test","stage":"test","status":"failed","allow_failure":true,"artifacts_file":{"filename":"build.zip","size":3}}`,
			``},
		{forgeauth.Forgejo, "o/r", "/repos/o/r",
			`{"id":7,"title":"Build","commit_sha":"abc","prettyref":"main","workflow_id":"ci.yml","event":"push","status":"success","created":"2026-01-02T03:04:05Z"}`,
			`{"id":9,"run_id":7,"name":"test","status":"failure","attempt":2,"steps":[{"number":0,"name":"Set up job","status":"success"}]}`,
			`{"id":11,"run_id":7,"name":"build.zip","size_in_bytes":3,"expired":false}`},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			runPath := tc.base + "/actions/runs"
			jobPath := tc.base + "/actions/jobs/9"
			artifactPath := tc.base + "/actions/artifacts/11/zip"
			logPath := jobPath + "/logs"
			if tc.kind == forgeauth.GitLab {
				runPath = tc.base + "/pipelines"
				jobPath = tc.base + "/jobs/9"
				logPath = jobPath + "/trace"
				artifactPath = jobPath + "/artifacts"
			}

			c := testClient(t, tc.kind, func(w http.ResponseWriter, r *http.Request) {
				path := r.URL.EscapedPath()
				if r.Method != "GET" {
					t.Errorf("method %s", r.Method)
				}

				switch path {
				case runPath:
					q := r.URL.Query()
					if q.Get("page") != "1" {
						t.Error("missing page")
					}
					if tc.kind == forgeauth.GitLab {
						if q.Get("sha") != "abc" || q.Get("ref") != "main" {
							t.Errorf("filters %v", q)
						}

						fmt.Fprintf(w, "[%s]", tc.pipeline)
					} else {
						if q.Get("head_sha") != "abc" {
							t.Errorf("filters %v", q)
						}
						if tc.kind == forgeauth.Forgejo && q.Get("ref") != "refs/heads/main" {
							t.Errorf("Forgejo ref %v", q)
						}

						fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[%s]}`, tc.pipeline)
					}
				case runPath + "/7":
					fmt.Fprint(w, tc.pipeline)
				case runPath + "/7/jobs":
					if tc.kind == forgeauth.GitHub {
						fmt.Fprintf(w, `{"total_count":1,"jobs":[%s]}`, tc.job)
					} else {
						fmt.Fprintf(w, "[%s]", tc.job)
					}
				case jobPath:
					fmt.Fprint(w, tc.job)
				case logPath:
					fmt.Fprint(w, "test output\n")
				case runPath + "/7/artifacts":
					if tc.kind == forgeauth.GitHub {
						fmt.Fprintf(w, `{"total_count":1,"artifacts":[%s]}`, tc.artifact)
					} else {
						fmt.Fprintf(w, "[%s]", tc.artifact)
					}
				case artifactPath:
					fmt.Fprint(w, "zip")
				default:
					t.Errorf("unexpected path %s", r.URL)
					http.NotFound(w, r)
				}
			})
			repo := RepositoryRef{Source: "source", Repository: tc.repository}
			ref := Ref{RepositoryRef: repo, ID: "7"}
			page, err := c.ListPipelines(t.Context(), ListOptions{
				RepositoryRef: repo,
				Commit:        "abc",
				Branch:        "main",
				Result:        "success",
			})
			if err != nil || len(page.Pipelines) != 1 || page.NextPage != 0 {
				t.Fatalf("list %+v %v", page, err)
			}

			p, err := c.GetPipeline(t.Context(), ref)
			if err != nil ||
				p.Ref != ref || p.Commit != "abc" || p.Status.Phase != "finished" || p.Status.Result != "success" || p.CreatedAt.IsZero() {
				t.Fatalf("pipeline %+v %v", p, err)
			}

			jobs, err := c.ListJobs(t.Context(), ref)
			if err != nil || len(jobs) != 1 || jobs[0].PipelineID != "7" {
				t.Fatalf("jobs %+v %v", jobs, err)
			}

			jobRef := Ref{RepositoryRef: repo, ID: "9"}
			j, err := c.GetJob(t.Context(), jobRef)
			if err != nil || j.Status.Result != "failure" || j.StepsKnown != (tc.kind != forgeauth.GitLab) {
				t.Fatalf("job %+v %v", j, err)
			}
			if tc.kind == forgeauth.GitLab && (j.Stage != "test" || !j.AllowFailure) {
				t.Fatal("lost job grouping or allowed failure")
			}

			log, err := c.ReadJobLog(t.Context(), jobRef)
			if err != nil || log.Text != "test output\n" || log.Truncated {
				t.Fatalf("log %+v %v", log, err)
			}

			artifacts, err := c.ListArtifacts(t.Context(), ref)
			if err != nil || len(artifacts) != 1 || artifacts[0].Availability != "available" || artifacts[0].Size != 3 {
				t.Fatalf("artifacts %+v %v", artifacts, err)
			}

			data, err := c.DownloadArtifact(t.Context(), artifacts[0].Ref)
			if err != nil || string(data) != "zip" {
				t.Fatalf("download %q %v", data, err)
			}
		})
	}
}

func TestPaginationAndFiltering(t *testing.T) {
	c := testClient(t, forgeauth.GitHub, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			// Do not follow untrusted Link URLs. Only its presence is used to
			// construct the next numbered request on the original source.
			w.Header().Set("Link", `<https://untrusted.invalid/private>; rel="next"`)
			fmt.Fprint(w, `{"workflow_runs":[{"id":1,"status":"in_progress"}]}`)
		} else {
			fmt.Fprint(w, `{"workflow_runs":[{"id":2,"status":"completed","conclusion":"failure"}]}`)
		}
	})
	o := ListOptions{RepositoryRef: RepositoryRef{Source: "source", Repository: "o/r"}, Result: "failure"}
	p, err := c.ListPipelines(t.Context(), o)
	if err != nil || len(p.Pipelines) != 0 || p.NextPage != 2 {
		t.Fatalf("filtered page %+v %v", p, err)
	}

	o.Page = p.NextPage
	p, err = c.ListPipelines(t.Context(), o)
	if err != nil || len(p.Pipelines) != 1 || p.Pipelines[0].ID != "2" || p.NextPage != 0 {
		t.Fatalf("next page %+v %v", p, err)
	}
}

func TestStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		kind    forgeauth.Kind
		state   string
		result  string
		phase   string
		outcome string
	}{
		{forgeauth.GitHub, "waiting", "", "waiting", ""},
		{forgeauth.GitHub, "completed", "timed_out", "finished", "failure"},
		{forgeauth.GitHub, "completed", "neutral", "finished", "neutral"},
		{forgeauth.GitHub, "completed", "new-result", "finished", "unknown"},
		{forgeauth.GitLab, "manual", "", "waiting", ""},
		{forgeauth.GitLab, "canceled", "", "finished", "cancelled"},
		{forgeauth.GitLab, "waiting_for_resource", "", "queued", ""},
		{forgeauth.Forgejo, "blocked", "", "waiting", ""},
		{forgeauth.Forgejo, "waiting", "", "queued", ""},
		{forgeauth.Forgejo, "skipped", "", "finished", "skipped"},
		{forgeauth.Forgejo, "future-state", "", "unknown", ""},
	} {
		s := normalize(tc.kind, tc.state, tc.result)
		if s.Phase != tc.phase || s.Result != tc.outcome || s.NativeStatus != tc.state || s.NativeResult != tc.result {
			t.Errorf("%s %s: %+v", tc.kind, tc.state, s)
		}
	}
}

func TestValidationBeforeRequests(t *testing.T) {
	c := testClient(t, forgeauth.GitHub, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid request contacted forge") })
	for _, r := range []Ref{
		{RepositoryRef: RepositoryRef{Source: "source", Repository: "o/../r"}, ID: "1"},
		{RepositoryRef: RepositoryRef{Source: "source", Repository: "o/r"}, ID: "../secrets"},
		{RepositoryRef: RepositoryRef{Source: "source", Repository: "o/r"}, ID: "+1"},
		{RepositoryRef: RepositoryRef{Source: "missing", Repository: "o/r"}, ID: "1"},
	} {
		if _, err := c.GetPipeline(t.Context(), r); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}

	if _, err := c.ListPipelines(t.Context(), ListOptions{RepositoryRef: RepositoryRef{Source: "source", Repository: "o/r"}, Phase: "bad"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := New(c.sources[0], c.sources[0]); !errors.Is(err, ErrInvalid) {
		t.Fatal("duplicate source accepted")
	}
}

func TestLogsAndDownloadLimits(t *testing.T) {
	for _, tc := range []struct {
		name      string
		code      int
		body      string
		want      error
		truncated bool
	}{
		{"missing", 404, "private upstream detail", forgehttp.ErrNotFound, false},
		{"denied", 403, "private upstream detail", forgehttp.ErrForbidden, false},
		{"redirect", 302, "", ErrUnsupported, false},
		{"large", 200, strings.Repeat("x", (1<<20)+1), nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, forgeauth.GitHub, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				fmt.Fprint(w, tc.body)
			})
			log, err := c.ReadJobLog(t.Context(), Ref{RepositoryRef: RepositoryRef{Source: "source", Repository: "o/r"}, ID: "1"})
			if !errors.Is(err, tc.want) || log.Truncated != tc.truncated || len(log.Text) > 1<<20 {
				t.Fatalf("log length %d truncated %v error %v", len(log.Text), log.Truncated, err)
			}
		})
	}

	c := testClient(
		t,
		forgeauth.Forgejo,
		func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", MaxArtifactBytes+1)) },
	)
	data, err := c.DownloadArtifact(t.Context(), Ref{RepositoryRef: RepositoryRef{Source: "source", Repository: "o/r"}, ID: "1"})
	if !errors.Is(err, ErrTooLarge) || data != nil {
		t.Fatal("oversized archive returned")
	}
}

func TestDefinitionsAndMalformedResponses(t *testing.T) {
	c := testClient(t, forgeauth.GitHub, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"workflows":[{"id":3,"name":"CI","path":".github/workflows/ci.yml","state":"active"}]}`)
	})
	r := RepositoryRef{Source: "source", Repository: "o/r"}
	d, err := c.ListDefinitions(t.Context(), r)
	if err != nil || len(d) != 1 || d[0].ID != "3" {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := c.ListPipelines(t.Context(), ListOptions{RepositoryRef: r}); err == nil {
		t.Fatal("wrong envelope accepted")
	}

	c = testClient(t, forgeauth.GitLab, func(w http.ResponseWriter, r *http.Request) { t.Error("unsupported feature made a request") })
	if _, err := c.ListDefinitions(t.Context(), r); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := c.ListPipelines(t.Context(), ListOptions{RepositoryRef: r, DefinitionID: "ci"}); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.ListPipelines(ctx, ListOptions{RepositoryRef: r}); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestJobListingLimit(t *testing.T) {
	c := testClient(t, forgeauth.Forgejo, func(w http.ResponseWriter, r *http.Request) {
		jobs := make([]map[string]int, 1001)
		for i := range jobs {
			jobs[i] = map[string]int{"id": i + 1}
		}

		_ = json.NewEncoder(w).Encode(jobs)
	})
	if _, err := c.ListJobs(t.Context(), Ref{RepositoryRef: RepositoryRef{Source: "source", Repository: "o/r"}, ID: "1"}); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
}

func TestGitHubFilteredHistoryLimit(t *testing.T) {
	calls := 0
	c := testClient(t, forgeauth.GitHub, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("page") == "20" {
			w.Header().Set("Link", `<https://example.invalid/first>; rel="first"`)
			fmt.Fprint(w, `{"total_count":1001,"workflow_runs":[{"id":1000,"status":"completed","conclusion":"success"}]}`)
		} else {
			fmt.Fprint(w, `{"total_count":1001,"workflow_runs":[{"id":1,"status":"completed","conclusion":"success"}]}`)
		}
	})
	o := ListOptions{RepositoryRef: RepositoryRef{Source: "source", Repository: "o/r"}, Page: 20, Branch: "main"}
	p, err := c.ListPipelines(t.Context(), o)
	if err != nil || p.NextPage != 21 {
		t.Fatalf("capped history presented as complete: %+v %v", p, err)
	}

	o.Page = 21
	if _, err := c.ListPipelines(t.Context(), o); !errors.Is(err, ErrTooLarge) || calls != 1 {
		t.Fatal("filtered limit ignored", err, calls)
	}

	o.Branch = ""
	if _, err := c.ListPipelines(t.Context(), o); err != nil {
		t.Fatal("unfiltered history incorrectly capped", err)
	}
}

func TestMalformedListsAndMismatchedResources(t *testing.T) {
	for _, body := range []string{`null`, `{"workflow_runs":null}`, `{"wrong":[]}`} {
		c := testClient(t, forgeauth.GitHub, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
		if _, err := c.ListPipelines(t.Context(), ListOptions{RepositoryRef: RepositoryRef{Source: "source", Repository: "o/r"}}); err == nil {
			t.Errorf("malformed response accepted: %s", body)
		}
	}

	c := testClient(t, forgeauth.Forgejo, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"id":8,"status":"running"}`) })
	r := Ref{RepositoryRef: RepositoryRef{Source: "source", Repository: "o/r"}, ID: "7"}
	if _, err := c.GetPipeline(t.Context(), r); err == nil {
		t.Fatal("mismatched pipeline accepted")
	}
	if _, err := c.GetJob(t.Context(), r); err == nil {
		t.Fatal("mismatched job accepted")
	}
}
