package changes

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

const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const target = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const newHead = "cccccccccccccccccccccccccccccccccccccccc"

func fixture(kind forgeauth.Kind, state, title, sha string, draft bool) map[string]any {
	if kind == forgeauth.GitLab {
		if state == "open" {
			state = "opened"
		}
		return map[string]any{"id": 90, "iid": 7, "state": state, "title": title, "description": "body", "draft": draft,
			"author": map[string]any{"username": "sam", "web_url": "https://forge/sam"}, "source_branch": "feature", "target_branch": "main",
			"source_project_id": 3, "target_project_id": 3, "diff_refs": map[string]string{"head_sha": sha, "start_sha": target, "base_sha": target},
			"web_url": "https://forge/o/r/-/merge_requests/7", "detailed_merge_status": "mergeable", "created_at": "2026-01-01T00:00:00Z"}
	}
	return map[string]any{"id": 90, "number": 7, "node_id": "PR_node", "state": state, "title": title, "body": "body", "draft": draft,
		"user": map[string]string{"login": "sam", "html_url": "https://forge/sam"}, "head": map[string]any{"ref": "feature", "sha": sha, "repo": map[string]string{"full_name": "o/r"}},
		"base":     map[string]any{"ref": "main", "sha": target, "repo": map[string]string{"full_name": "o/r"}},
		"html_url": "https://forge/o/r/pulls/7", "mergeable": true, "mergeable_state": "clean", "changed_files": 1, "created_at": "2026-01-01T00:00:00Z"}
}

func client(t *testing.T, kind forgeauth.Kind, h http.HandlerFunc) (*Client, Ref) {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, err := New(Source{ID: "connection", Session: &forgeauth.Session{Kind: kind, BaseURL: s.URL, Client: s.Client()}})
	if err != nil {
		t.Fatal(err)
	}
	return c, Ref{"connection", "o/r", 7}
}

func endpoint(kind forgeauth.Kind) string {
	if kind == forgeauth.GitLab {
		return "/projects/o%2Fr/merge_requests"
	}
	return "/repos/o/r/pulls"
}

func TestLifecycle(t *testing.T) {
	for _, kind := range []forgeauth.Kind{forgeauth.GitHub, forgeauth.GitLab, forgeauth.Forgejo} {
		t.Run(string(kind), func(t *testing.T) {
			state, title, draft := "open", "proposal", false
			writes := 0
			c, r := client(t, kind, func(w http.ResponseWriter, req *http.Request) {
				path := req.URL.EscapedPath()
				if path == "/graphql" {
					var b struct {
						Query     string
						Variables map[string]string
					}
					_ = json.NewDecoder(req.Body).Decode(&b)
					if b.Variables["id"] != "PR_node" {
						t.Error("missing node ID")
					}
					name := "markPullRequestReadyForReview"
					draft = false
					if strings.Contains(b.Query, "convertPullRequestToDraft") {
						name = "convertPullRequestToDraft"
						draft = true
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{name: map[string]any{"pullRequest": map[string]bool{"isDraft": draft}}}})
					return
				}
				if path != endpoint(kind) && path != endpoint(kind)+"/7" {
					t.Errorf("path %s", path)
					http.NotFound(w, req)
					return
				}
				if req.Method != "GET" {
					writes++
					var b map[string]any
					if err := json.NewDecoder(req.Body).Decode(&b); err != nil {
						t.Error(err)
					}
					if v, ok := b["title"].(string); ok {
						title = v
						if kind != forgeauth.GitHub {
							draft = strings.HasPrefix(v, "Draft:")
						}
					}
					if v, ok := b["draft"].(bool); ok {
						draft = v
					}
					if v, ok := b["state"].(string); ok {
						state = v
					}
					if v, ok := b["state_event"].(string); ok {
						if v == "close" {
							state = "closed"
						} else {
							state = "open"
						}
					}
					if req.Method == "POST" {
						key := "head"
						if kind == forgeauth.GitLab {
							key = "source_branch"
						}
						if b[key] != "feature" {
							t.Errorf("create body %v", b)
						}
					} else {
						method := "PATCH"
						if kind == forgeauth.GitLab {
							method = "PUT"
						}
						if req.Method != method {
							t.Errorf("method %s", req.Method)
						}
						if v, ok := b["body"]; ok && v != "" {
							t.Error("description was not cleared")
						}
					}
				}
				_ = json.NewEncoder(w).Encode(fixture(kind, state, title, head, draft))
			})
			ctx := context.Background()
			v, err := c.Create(ctx, RepositoryRef{r.Source, r.Repository}, NewChange{Title: title, SourceBranch: "feature", TargetBranch: "main"})
			if err != nil || v.Ref != r || v.Author.Login != "sam" || v.Revision.Head != head || v.State != Open || v.CreatedAt.IsZero() {
				t.Fatalf("create: %+v %v", v, err)
			}
			for _, want := range []bool{true, false} {
				v, err = c.SetDraft(ctx, r, want)
				if err != nil || v.Draft != want {
					t.Fatalf("draft %v: %+v %v", want, v, err)
				}
			}
			empty := ""
			for _, want := range []State{Closed, Open} {
				v, err = c.Update(ctx, r, Update{State: &want, Description: &empty})
				if err != nil || v.State != want {
					t.Fatalf("state: %+v %v", v, err)
				}
			}
			before := writes
			_, err = c.Update(ctx, r, Update{SourceBranch: &empty, Description: &empty})
			if !errors.Is(err, ErrUnsupported) || writes != before {
				t.Fatalf("unsupported write: %v, writes %d", err, writes)
			}
		})
	}
}

func TestListFiltersAndPagination(t *testing.T) {
	for _, kind := range []forgeauth.Kind{forgeauth.GitHub, forgeauth.GitLab, forgeauth.Forgejo} {
		t.Run(string(kind), func(t *testing.T) {
			calls := 0
			c, _ := client(t, kind, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.EscapedPath() != endpoint(kind) {
					t.Errorf("path %s", r.URL.EscapedPath())
				}
				a := fixture(kind, "closed", "rejected", head, false)
				if r.URL.Query().Get("page") == "2" {
					a = fixture(kind, "closed", "merged", head, true)
					a["merged_at"] = "2026-01-02T00:00:00Z"
				} else {
					w.Header().Set("Link", "<"+endpoint(kind)+"?page=2>; rel=\"next\"")
				}
				_ = json.NewEncoder(w).Encode([]any{a})
			})
			v, err := c.List(context.Background(), ListOptions{Repository: "o/r", State: "merged", Author: "sam", Draft: ptr(true)})
			if err != nil || len(v) != 1 || v[0].State != Merged || calls != 2 {
				t.Fatalf("list: %+v %v calls=%d", v, err, calls)
			}
			v, err = c.List(context.Background(), ListOptions{Repository: "o/r", State: "closed"})
			if err != nil || len(v) != 1 || v[0].Title != "rejected" {
				t.Fatalf("closed: %+v %v", v, err)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestDiscussionAndContent(t *testing.T) {
	for _, kind := range []forgeauth.Kind{forgeauth.GitHub, forgeauth.GitLab, forgeauth.Forgejo} {
		t.Run(string(kind), func(t *testing.T) {
			c, r := client(t, kind, func(w http.ResponseWriter, r *http.Request) {
				p := r.URL.Path
				switch {
				case strings.HasSuffix(p, "/notes"), strings.HasSuffix(p, "/comments"):
					cm := map[string]any{"id": 1, "body": "hello", "user": map[string]string{"login": "sam"}, "author": map[string]string{"username": "sam"}}
					if r.Method == "POST" {
						var b map[string]string
						_ = json.NewDecoder(r.Body).Decode(&b)
						if b["body"] != "hello" {
							t.Error(b)
						}
						_ = json.NewEncoder(w).Encode(cm)
					} else {
						_ = json.NewEncoder(w).Encode([]any{cm, map[string]any{"id": 2, "system": true}, map[string]any{"id": 3, "type": "DiffNote"}, map[string]any{"id": 4, "position": map[string]int{"line": 1}}})
					}
				case strings.HasSuffix(p, "/commits"):
					if kind == forgeauth.GitLab {
						fmt.Fprintf(w, `[{"id":%q,"message":"message","author_name":"Sam"}]`, head)
					} else {
						fmt.Fprintf(w, `[{"sha":%q,"commit":{"message":"message","author":{"name":"Sam"}}}]`, head)
					}
				case strings.HasSuffix(p, "/files"):
					fmt.Fprint(w, `[{"filename":"new.go","previous_filename":"old.go","status":"renamed","patch":"@@ -1 +1 @@\n-old\n+new"}]`)
				case strings.HasSuffix(p, "/diffs"):
					fmt.Fprint(w, `[{"old_path":"old.go","new_path":"new.go","renamed_file":true,"diff":"@@ -1 +1 @@\n-old\n+new"}]`)
				case strings.HasSuffix(p, "/raw_diffs"), strings.HasSuffix(p, ".diff"), r.Header.Get("Accept") == "application/vnd.github.diff":
					fmt.Fprint(w, "diff --git a/old.go b/new.go\n")
				default:
					_ = json.NewEncoder(w).Encode(fixture(kind, "open", "title", head, false))
				}
			})
			ctx := context.Background()
			cm, err := c.Comments(ctx, r)
			if err != nil || len(cm) != 1 || cm[0].Body != "hello" {
				t.Fatalf("comments %+v %v", cm, err)
			}
			added, err := c.AddComment(ctx, r, "hello")
			if err != nil || added.ID != "1" {
				t.Fatalf("add %+v %v", added, err)
			}
			commits, err := c.Commits(ctx, r)
			if err != nil || len(commits) != 1 || commits[0].ID != head || commits[0].Message != "message" {
				t.Fatalf("commits %+v %v", commits, err)
			}
			files, err := c.Files(ctx, r)
			if err != nil || len(files.Files) != 1 || files.Files[0].OldPath != "old.go" || files.Files[0].Status != "renamed" {
				t.Fatalf("files %+v %v", files, err)
			}
			diff, err := c.Diff(ctx, r)
			if err != nil || diff.Revision.Head != head || !strings.HasPrefix(diff.Patch, "diff --git") {
				t.Fatalf("diff %+v %v", diff, err)
			}
		})
	}
}

func TestMergeGuardsAndFailures(t *testing.T) {
	for _, kind := range []forgeauth.Kind{forgeauth.GitHub, forgeauth.GitLab, forgeauth.Forgejo} {
		t.Run(string(kind), func(t *testing.T) {
			writes, statusCode, success := 0, 200, true
			c, r := client(t, kind, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/merge") {
					_ = json.NewEncoder(w).Encode(fixture(kind, "open", "title", head, false))
					return
				}
				writes++
				var b map[string]any
				_ = json.NewDecoder(r.Body).Decode(&b)
				key := "sha"
				if kind == forgeauth.Forgejo {
					key = "head_commit_id"
					if r.Method != "POST" || b["Do"] != "squash" {
						t.Error("forgejo merge", b)
					}
				} else if r.Method != "PUT" {
					t.Error(r.Method)
				}
				if b[key] != head {
					t.Errorf("atomic head guard missing: %v", b)
				}
				if _, ok := b["force_merge"]; ok {
					t.Error("force merge sent")
				}
				w.WriteHeader(statusCode)
				if statusCode != 200 {
					fmt.Fprint(w, `{"message":"merge conflict"}`)
					return
				}
				switch kind {
				case forgeauth.GitHub:
					_ = json.NewEncoder(w).Encode(map[string]any{"merged": success, "sha": target})
				case forgeauth.GitLab:
					state := "opened"
					if success {
						state = "merged"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"state": state, "merge_commit_sha": target})
				}
			})
			ctx := context.Background()
			_, err := c.Merge(ctx, r, MergeOptions{Method: "squash", ExpectedHead: newHead})
			if !errors.Is(err, ErrHeadChanged) || writes != 0 {
				t.Fatalf("stale: %v writes=%d", err, writes)
			}
			_, err = c.Merge(ctx, r, MergeOptions{Method: "invented", ExpectedHead: head})
			if !errors.Is(err, ErrUnsupported) || writes != 0 {
				t.Fatalf("method: %v", err)
			}
			v, err := c.Merge(ctx, r, MergeOptions{Method: "squash", ExpectedHead: head})
			if err != nil || !v.Merged {
				t.Fatalf("merge %+v %v", v, err)
			}
			for _, code := range []int{409, 405, 422} {
				statusCode = code
				_, err = c.Merge(ctx, r, MergeOptions{Method: "squash", ExpectedHead: head})
				want := ErrMergeRejected
				if code == 409 && kind != forgeauth.Forgejo {
					want = ErrHeadChanged
				}
				if !errors.Is(err, want) {
					t.Errorf("status %d: %v", code, err)
				}
			}
			if kind != forgeauth.Forgejo {
				statusCode = 200
				success = false
				_, err = c.Merge(ctx, r, MergeOptions{Method: "squash", ExpectedHead: head})
				if !errors.Is(err, ErrMergeRejected) {
					t.Errorf("false success %v", err)
				}
			}
		})
	}
}

func TestReadinessAndRevisionRace(t *testing.T) {
	reads := 0
	c, r := client(t, forgeauth.GitHub, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/vnd.github.diff" {
			fmt.Fprint(w, "diff")
			return
		}
		reads++
		sha := head
		if reads > 1 {
			sha = newHead
		}
		v := fixture(forgeauth.GitHub, "open", "title", sha, false)
		v["mergeable"] = nil
		delete(v, "mergeable_state")
		_ = json.NewEncoder(w).Encode(v)
	})
	readiness, err := c.Readiness(context.Background(), r)
	if err != nil || readiness.Status != "unknown" || readiness.Conditions[3].Status != "unknown" {
		t.Fatalf("readiness %+v %v", readiness, err)
	}
	reads = 0
	_, err = c.Diff(context.Background(), r)
	if !errors.Is(err, ErrHeadChanged) {
		t.Fatalf("race %v", err)
	}
}

func TestValidationAndForgeErrors(t *testing.T) {
	c, r := client(t, forgeauth.GitHub, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "denied", http.StatusForbidden) })
	_, err := c.Get(context.Background(), r)
	if !errors.Is(err, forgehttp.ErrForbidden) {
		t.Fatal(err)
	}
	for _, repo := range []string{"", "owner", "o/../r", "o/r?token=bad", "o/%2f"} {
		r.Repository = repo
		_, err = c.Get(context.Background(), r)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", repo, err)
		}
	}
	r = Ref{"connection", "o/r", 7}
	_, err = c.Merge(context.Background(), r, MergeOptions{ExpectedHead: "short"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	r.Source = ""
	_, err = c.Get(context.Background(), r)
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
