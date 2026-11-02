package changes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

func TestReadinessConditions(t *testing.T) {
	for _, tt := range []struct {
		kind                      forgeauth.Kind
		native, status, condition string
		mergeable                 *bool
		draft                     bool
	}{
		{forgeauth.GitHub, "clean", "ready", "", ptr(true), false},
		{forgeauth.GitHub, "blocked", "blocked", "forge", ptr(true), false},
		{forgeauth.GitHub, "dirty", "blocked", "conflicts", ptr(false), false},
		{forgeauth.GitHub, "unknown", "unknown", "", nil, false},
		{forgeauth.GitHub, "clean", "blocked", "draft", ptr(true), true},
		{forgeauth.GitLab, "not_approved", "blocked", "reviews", nil, false},
		{forgeauth.GitLab, "ci_must_pass", "blocked", "checks", nil, false},
		{forgeauth.GitLab, "checking", "unknown", "", nil, false},
		{forgeauth.GitLab, "future_status", "unknown", "", nil, false},
		{forgeauth.Forgejo, "", "unknown", "", ptr(true), false},
		{forgeauth.Forgejo, "", "blocked", "conflicts", ptr(false), false},
	} {
		t.Run(string(tt.kind)+"/"+tt.native+"/"+tt.condition, func(t *testing.T) {
			c, r := client(t, tt.kind, func(w http.ResponseWriter, r *http.Request) {
				v := fixture(tt.kind, "open", "title", head, tt.draft)
				v["mergeable"] = tt.mergeable
				v["mergeable_state"] = tt.native
				v["detailed_merge_status"] = tt.native
				_ = json.NewEncoder(w).Encode(v)
			})
			v, err := c.Readiness(context.Background(), r)
			if err != nil || v.Status != tt.status {
				t.Fatalf("%+v %v", v, err)
			}
			if tt.condition != "" {
				found := false
				for _, cond := range v.Conditions {
					if cond.Name == tt.condition && cond.Status == "unmet" {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing unmet %s: %+v", tt.condition, v)
				}
			}
		})
	}
}

func TestMergedCannotReopenOrMerge(t *testing.T) {
	for _, kind := range []forgeauth.Kind{forgeauth.GitHub, forgeauth.GitLab, forgeauth.Forgejo} {
		t.Run(string(kind), func(t *testing.T) {
			c, r := client(t, kind, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("mutated merged proposal")
				}
				v := fixture(kind, "closed", "title", head, false)
				v["merged_at"] = "2026-01-01T01:00:00Z"
				_ = json.NewEncoder(w).Encode(v)
			})
			v, err := c.Get(context.Background(), r)
			if err != nil || v.State != Merged {
				t.Fatalf("%+v %v", v, err)
			}
			_, err = c.Update(context.Background(), r, Update{State: ptr(Open)})
			if !errors.Is(err, forgehttp.ErrConflict) {
				t.Fatal(err)
			}
			_, err = c.Merge(context.Background(), r, MergeOptions{})
			if !errors.Is(err, ErrMergeRejected) {
				t.Fatal(err)
			}
			_, err = c.SetDraft(context.Background(), r, true)
			if !errors.Is(err, forgehttp.ErrConflict) {
				t.Fatal(err)
			}
		})
	}
}

func TestGraphQLFailureIsNotSuccess(t *testing.T) {
	for _, status := range []int{200, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c, r := client(t, forgeauth.GitHub, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/graphql" {
					w.WriteHeader(status)
					fmt.Fprint(w, `{"errors":[{"message":"denied"}]}`)
					return
				}
				_ = json.NewEncoder(w).Encode(fixture(forgeauth.GitHub, "open", "title", head, false))
			})
			_, err := c.SetDraft(context.Background(), r, true)
			if err == nil {
				t.Fatal("GraphQL failure accepted")
			}
			if status == 404 && !errors.Is(err, ErrUnsupported) {
				t.Fatal(err)
			}
		})
	}
}

func TestContentLimits(t *testing.T) {
	c, r := client(t, forgeauth.GitHub, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/commits"):
			items := make([]map[string]string, 250)
			for i := range items {
				items[i] = map[string]string{"sha": head}
			}
			_ = json.NewEncoder(w).Encode(items)
		case strings.HasSuffix(r.URL.Path, "/files"):
			fmt.Fprint(w, `[{"filename":"binary","status":"added"}]`)
		case r.Header.Get("Accept") == "application/vnd.github.diff":
			_, _ = w.Write([]byte(strings.Repeat("x", (16<<20)+1)))
		default:
			v := fixture(forgeauth.GitHub, "open", "title", head, false)
			v["changed_files"] = 3001
			_ = json.NewEncoder(w).Encode(v)
		}
	})
	_, err := c.Commits(context.Background(), r)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	f, err := c.Files(context.Background(), r)
	if err != nil || f.Completeness != "truncated" || !f.Files[0].PatchUnavailable || f.Files[0].OldPath != "" {
		t.Fatalf("%+v %v", f, err)
	}
	_, err = c.Diff(context.Background(), r)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
}

func TestSourceSelection(t *testing.T) {
	c, r := client(t, forgeauth.GitLab, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/projects/group%2Fsub%2Frepo/merge_requests" {
			t.Error(r.URL.EscapedPath())
		}
		if r.URL.Query().Get("scope") != "all" || r.URL.Query().Get("state") != "opened" {
			t.Error(r.URL.RawQuery)
		}
		fmt.Fprint(w, `[]`)
	})
	_, err := c.List(context.Background(), ListOptions{Source: r.Source, Repository: "group/sub/repo"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.List(context.Background(), ListOptions{Source: "missing", Repository: "o/r"})
	if !errors.Is(err, ErrUnknownSource) {
		t.Fatal(err)
	}
	_, err = c.List(context.Background(), ListOptions{Repository: "o/r", State: "bad"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	sources := c.Sources()
	sources[0].ID = "changed"
	if c.Sources()[0].ID != r.Source {
		t.Fatal("Sources aliases client")
	}
	_, err = New(c.Sources()[0], c.Sources()[0])
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
