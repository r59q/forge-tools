package review

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

const baseSHA = "1111111111111111111111111111111111111111"
const headSHA = "2222222222222222222222222222222222222222"
const nextSHA = "3333333333333333333333333333333333333333"

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(r *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}
func connected(t *testing.T, kind forgeauth.Kind, fn func(*http.Request) string) *Client {
	t.Helper()
	c, err := New(&MemoryStore{}, Source{ID: "source", Session: &forgeauth.Session{Kind: kind, BaseURL: "https://forge.test/api", Client: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) { return response(r, fn(r)), nil })}}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func revisionJSON(kind forgeauth.Kind, head string) string {
	if kind == forgeauth.GitLab {
		return `{"diff_refs":{"base_sha":"` + baseSHA + `","head_sha":"` + head + `","start_sha":"` + baseSHA + `"}}`
	}
	return `{"head":{"sha":"` + head + `"},"base":{"sha":"` + baseSHA + `"}}`
}
func TestChangeRequestCaptureIsFixedAndDetectsPush(t *testing.T) {
	for _, kind := range []forgeauth.Kind{forgeauth.GitHub, forgeauth.GitLab, forgeauth.Forgejo} {
		t.Run(string(kind), func(t *testing.T) {
			calls := 0
			head := headSHA
			during := false
			c := connected(t, kind, func(r *http.Request) string {
				calls++
				if strings.Contains(r.URL.Path, "raw_diffs") || strings.HasSuffix(r.URL.Path, ".diff") || r.Header.Get("Accept") == "application/vnd.github.diff" {
					if during {
						head = nextSHA
					}
					return patch
				}
				return revisionJSON(kind, head)
			})
			ctx := context.Background()
			in := Input{Kind: "change_request", Ref: Ref{Source: "source", Repository: "o/r", Number: 7}}
			s, err := c.CreateSubject(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if s.Revision.Head != headSHA || s.Ref.Number != 7 || s.Completeness != "unknown" {
				t.Fatal(s)
			}
			before := calls
			head = nextSHA
			got, err := c.GetSubject(ctx, s.ID)
			if err != nil || calls != before || got.Revision.Head != headSHA {
				t.Fatal("refetched snapshot")
			}
			if _, err = c.SubmitNative(ctx, s.ID, "approved", ""); !errors.Is(err, ErrConflict) {
				t.Fatal("stale native write", err)
			}
			head = headSHA
			during = true
			if _, err = c.CreateSubject(ctx, in); !errors.Is(err, ErrConflict) {
				t.Fatal("mixed revisions", err)
			}
		})
	}
}
func TestComparisonResolvesExactIDs(t *testing.T) {
	for _, kind := range []forgeauth.Kind{forgeauth.GitHub, forgeauth.GitLab, forgeauth.Forgejo} {
		t.Run(string(kind), func(t *testing.T) {
			c := connected(t, kind, func(r *http.Request) string {
				p := r.URL.Path
				if strings.Contains(p, "/commits/") {
					sha := baseSHA
					if strings.HasSuffix(p, "/topic") {
						sha = headSHA
					}
					return `{"sha":"` + sha + `","id":"` + sha + `"}`
				}
				if strings.Contains(p, "/compare") {
					if kind == forgeauth.GitLab {
						if r.URL.Query().Get("from") != baseSHA || r.URL.Query().Get("to") != headSHA || r.URL.Query().Get("straight") != "true" {
							t.Error(r.URL)
						}
						return `{"diffs":[{"old_path":"a","new_path":"a","diff":"@@ -1 +1 @@\n-old\n+new\n"}]}`
					}
					if !strings.Contains(p, baseSHA) || !strings.Contains(p, headSHA) {
						t.Error("moving reference", p)
					}
					if kind == forgeauth.Forgejo {
						return `{"files":[{"filename":"a"}]}`
					}
					if r.Header.Get("Accept") == "application/vnd.github.diff" {
						return patch
					}
					return `{"merge_base_commit":{"sha":"` + baseSHA + `"}}`
				}
				if strings.Contains(p, "/raw/") {
					if r.URL.Query().Get("ref") == baseSHA {
						return "old\n"
					}
					if r.URL.Query().Get("ref") != headSHA {
						t.Error("unpinned file", r.URL)
					}
					return "new\n"
				}
				t.Fatal("unexpected route", r.URL)
				return ""
			})
			s, err := c.CreateSubject(context.Background(), Input{Kind: "comparison", Ref: Ref{Source: "source", Repository: "o/r"}, From: "main", To: "topic"})
			if err != nil {
				t.Fatal(err)
			}
			if s.Revision.Base != baseSHA || s.Revision.Target != baseSHA || s.Revision.Head != headSHA || len(s.Files) != 1 {
				t.Fatal(s)
			}
			if kind == forgeauth.Forgejo && len(s.Context) != 2 {
				t.Fatal("missing reconstructed context")
			}
		})
	}
}
func TestNativeWritesAndUnsupportedCapabilities(t *testing.T) {
	for _, kind := range []forgeauth.Kind{forgeauth.GitHub, forgeauth.GitLab, forgeauth.Forgejo} {
		t.Run(string(kind), func(t *testing.T) {
			var writes []map[string]any
			var paths []string
			c := connected(t, kind, func(r *http.Request) string {
				if r.Method != "GET" {
					var b map[string]any
					if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
						t.Fatal(err)
					}
					writes = append(writes, b)
					paths = append(paths, r.URL.Path)
					return `{"id":123}`
				}
				if strings.HasSuffix(r.URL.Path, ".diff") || strings.HasSuffix(r.URL.Path, "/raw_diffs") || r.Header.Get("Accept") == "application/vnd.github.diff" {
					return patch
				}
				if strings.Contains(r.URL.Path, "/discussions/") {
					return `{"notes":[{"resolvable":true,"position":{"head_sha":"` + headSHA + `"}}]}`
				}
				if strings.HasSuffix(r.URL.Path, "/comments") {
					return `[{"id":123,"original_commit_id":"` + headSHA + `"}]`
				}
				return revisionJSON(kind, headSHA)
			})
			ctx := context.Background()
			s, err := c.CreateSubject(ctx, Input{Kind: "change_request", Ref: Ref{Source: "source", Repository: "o/r", Number: 7}})
			if err != nil {
				t.Fatal(err)
			}
			caps, _ := c.Capabilities("source")
			if !caps.Inline || !caps.Approve || caps.Resolve != (kind == forgeauth.GitLab) {
				t.Fatal(caps)
			}
			result, err := c.SubmitNative(ctx, s.ID, "approved", "")
			if err != nil || result.Storage != "native" {
				t.Fatal(result, err)
			}
			field := "commit_id"
			if kind == forgeauth.GitLab {
				field = "sha"
			}
			if writes[0][field] != headSHA {
				t.Fatal("approval not pinned", writes)
			}
			before := len(writes)
			if kind == forgeauth.GitLab {
				if _, err = c.SubmitNative(ctx, s.ID, "changes_requested", "change this"); !errors.Is(err, ErrUnsupported) {
					t.Fatal(err)
				}
				if len(writes) != before {
					t.Fatal("unsupported operation wrote")
				}
			}
			if _, err = c.CommentNative(ctx, s.ID, "inline", &Anchor{"main.go", "new", 3, 3}); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case forgeauth.GitHub:
				if writes[len(writes)-1]["side"] != "RIGHT" || writes[len(writes)-1]["line"] != float64(3) {
					t.Fatal(writes)
				}
			case forgeauth.GitLab:
				pos := writes[len(writes)-1]["position"].(map[string]any)
				if pos["head_sha"] != headSHA || pos["new_line"] != float64(3) {
					t.Fatal(pos)
				}
			case forgeauth.Forgejo:
				comments := writes[len(writes)-1]["comments"].([]any)
				if comments[0].(map[string]any)["new_position"] != float64(3) {
					t.Fatal(comments)
				}
			}
			before = len(writes)
			if _, err = c.CommentNative(ctx, s.ID, "bad", &Anchor{"main.go", "old", 4, 4}); !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
			if len(writes) != before {
				t.Fatal("invalid anchor wrote")
			}
			_, err = c.ReplyNative(ctx, s.ID, "123", "reply")
			if kind == forgeauth.Forgejo {
				if !errors.Is(err, ErrUnsupported) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			_, err = c.ResolveNative(ctx, s.ID, "123", true)
			if kind != forgeauth.GitLab {
				if !errors.Is(err, ErrUnsupported) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			reviewers := []string{"alice"}
			if kind == forgeauth.GitLab {
				reviewers = []string{"42"}
			}
			if _, err = c.RequestNativeReview(ctx, s.ID, reviewers); err != nil {
				t.Fatal(err)
			}
			local, _ := c.ListReviews(ctx, s.ID, "", "")
			if len(local) != 0 {
				t.Fatal("native became local")
			}
		})
	}
}
func TestNativePaginationAndOriginalRevision(t *testing.T) {
	c := connected(t, forgeauth.GitHub, func(r *http.Request) string {
		if strings.HasSuffix(r.URL.Path, "/comments") {
			return `[{"id":123,"original_commit_id":"` + nextSHA + `"}]`
		}
		if r.Method != "GET" {
			t.Fatal("unexpected write")
		}
		return "[]"
	})
	ctx := context.Background()
	s := Subject{ID: "snap", Kind: "change_request", Ref: Ref{Source: "source", Repository: "o/r", Number: 7}, Revision: Revision{Head: headSHA}}
	c.store.Update(ctx, func(st *State) error { st.Subjects[s.ID] = s; return nil })
	result, err := c.ListNative(ctx, s.ID, "threads", "")
	if err != nil || !strings.Contains(string(result.Data), nextSHA) {
		t.Fatal(result, err)
	}
	if _, err = c.ReplyNative(ctx, s.ID, "123", "reply"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestGitLabContextLineAndRenamedGitHubPath(t *testing.T) {
	for _, kind := range []forgeauth.Kind{forgeauth.GitLab, forgeauth.GitHub} {
		t.Run(string(kind), func(t *testing.T) {
			var body map[string]any
			c := connected(t, kind, func(r *http.Request) string {
				if r.Method == "POST" {
					json.NewDecoder(r.Body).Decode(&body)
					return `{"id":1}`
				}
				return revisionJSON(kind, headSHA)
			})
			ctx := context.Background()
			s := Subject{ID: "snapshot", Kind: "change_request", Ref: Ref{Source: "source", Repository: "o/r", Number: 1}, Revision: Revision{Head: headSHA, Target: baseSHA}, Patch: "--- a/old\n+++ b/new\n@@ -2 +3 @@\n same\n", Files: []File{{OldPath: "old", Path: "new"}}}
			if kind == forgeauth.GitLab {
				s.Revision.Base = baseSHA
			}
			c.store.Update(ctx, func(st *State) error { st.Subjects[s.ID] = s; return nil })
			if _, err := c.CommentNative(ctx, s.ID, "context", &Anchor{Path: "old", Side: "old", Start: 2, End: 2}); err != nil {
				t.Fatal(err)
			}
			if kind == forgeauth.GitHub {
				if body["path"] != "new" || body["side"] != "LEFT" {
					t.Fatal(body)
				}
			} else {
				pos := body["position"].(map[string]any)
				if pos["old_line"] != float64(2) || pos["new_line"] != float64(3) || pos["old_path"] != "old" || pos["new_path"] != "new" {
					t.Fatal(pos)
				}
			}
		})
	}
}

func TestForgejoMissingComparisonFilesIsUnsupported(t *testing.T) {
	c := connected(t, forgeauth.Forgejo, func(r *http.Request) string {
		if strings.Contains(r.URL.Path, "/commits/") {
			return `{"sha":"` + headSHA + `"}`
		}
		return `{"commits":[],"total_commits":1}`
	})
	_, err := c.CreateSubject(context.Background(), Input{Kind: "comparison", Ref: Ref{Source: "source", Repository: "o/r"}, From: "main", To: "topic"})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}
