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
