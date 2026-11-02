package changes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

func TestGitLabLockedProposal(t *testing.T) {
	locked := fixture(forgeauth.GitLab, "locked", "Merging", head, false)
	open := fixture(forgeauth.GitLab, "opened", "Open", head, false)
	open["iid"] = 8
	c, r := client(t, forgeauth.GitLab, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected write: %s %s", r.Method, r.URL.Path)
		}
		if strings.HasSuffix(r.URL.Path, "/merge_requests") {
			if r.URL.Query().Get("state") != "all" {
				t.Errorf("state query = %q", r.URL.Query().Get("state"))
			}
			_ = json.NewEncoder(w).Encode([]any{locked, open})
			return
		}
		_ = json.NewEncoder(w).Encode(locked)
	})
	ctx := context.Background()
	v, err := c.Get(ctx, r)
	if err != nil || v.State != Locked || v.Ref != r || v.Revision.Head != head {
		t.Fatalf("Get: %+v, %v", v, err)
	}
	list, err := c.List(ctx, ListOptions{Source: r.Source, Repository: r.Repository, State: "all"})
	if err != nil || len(list) != 2 || list[0].State != Locked || list[1].State != Open {
		t.Fatalf("List: %+v, %v", list, err)
	}
	ready, err := c.Readiness(ctx, r)
	if err != nil || ready.Status != "blocked" {
		t.Fatalf("Readiness: %+v, %v", ready, err)
	}
	stateUnmet := false
	for _, condition := range ready.Conditions {
		stateUnmet = stateUnmet || condition == (Condition{"state", "unmet", "locked"})
	}
	if !stateUnmet {
		t.Errorf("missing locked state condition: %+v", ready.Conditions)
	}
	_, err = c.Merge(ctx, r, MergeOptions{ExpectedHead: head})
	if !errors.Is(err, ErrMergeRejected) {
		t.Fatalf("Merge: %v", err)
	}
}

func TestSourceBranchNormalization(t *testing.T) {
	for _, tt := range []struct {
		name             string
		kind             forgeauth.Kind
		label, ref, want string
	}{
		{"forgejo deleted branch", forgeauth.Forgejo, "feature", "refs/pull/7/head", "feature"},
		{"forgejo existing branch", forgeauth.Forgejo, "feature", "feature", "feature"},
		{"forgejo missing label", forgeauth.Forgejo, "", "feature", "feature"},
		{"github qualified label", forgeauth.GitHub, "owner:feature", "feature", "feature"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := fixture(tt.kind, "closed", "Merged", head, false)
			v["merged"] = true
			v["head"] = map[string]string{"label": tt.label, "ref": tt.ref, "sha": head}
			c, r := client(t, tt.kind, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/pulls") {
					_ = json.NewEncoder(w).Encode([]any{v})
				} else {
					_ = json.NewEncoder(w).Encode(v)
				}
			})
			got, err := c.Get(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			list, err := c.List(context.Background(), ListOptions{Source: r.Source, Repository: r.Repository, State: "all"})
			if err != nil || len(list) != 1 {
				t.Fatalf("List: %+v, %v", list, err)
			}
			for _, item := range []ChangeRequest{got, list[0]} {
				if item.SourceBranch != tt.want || item.Revision.Head != head || item.TargetBranch != "main" || item.State != Merged {
					t.Errorf("proposal = %+v", item)
				}
			}
		})
	}
}
