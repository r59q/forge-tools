package changes

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// SetDraft marks an open proposal draft or ready. GitHub uses GraphQL through
// the same session's /graphql endpoint; a session without that route returns
// ErrUnsupported. GitLab and Forgejo use their default draft title prefixes.
func (c *Client) SetDraft(ctx context.Context, r Ref, draft bool) (ChangeRequest, error) {
	s, _, _, err := c.locate(r)
	if err != nil {
		return ChangeRequest{}, err
	}
	v, err := c.read(ctx, r)
	if err != nil {
		return ChangeRequest{}, err
	}
	if v.change.State != Open {
		return ChangeRequest{}, fmt.Errorf("%w: draft requires an open proposal", forgehttp.ErrConflict)
	}
	if v.change.Draft == draft {
		return v.change, nil
	}
	if s.Session.Kind != forgeauth.GitHub {
		title := draftTitle(v.change.Title, draft)
		out, err := c.Update(ctx, r, Update{Title: &title})
		if err == nil && out.Draft != draft {
			return out, fmt.Errorf("%w: forge draft prefixes differ", ErrUnsupported)
		}
		return out, err
	}
	if v.nodeID == "" {
		return ChangeRequest{}, fmt.Errorf("%w: missing GitHub node ID", ErrUnsupported)
	}
	mutation := "markPullRequestReadyForReview"
	if draft {
		mutation = "convertPullRequestToDraft"
	}
	body := map[string]any{"query": "mutation($id:ID!){" + mutation + "(input:{pullRequestId:$id}){pullRequest{isDraft}}}", "variables": map[string]string{"id": v.nodeID}}
	var result struct {
		Data map[string]*struct {
			PullRequest *struct {
				IsDraft bool `json:"isDraft"`
			} `json:"pullRequest"`
		}
		Errors []struct{ Message string }
	}
	if err := forgerest.Send(ctx, s.Session, "POST", "/graphql", body, &result); err != nil {
		if errors.Is(err, forgehttp.ErrNotFound) {
			return ChangeRequest{}, fmt.Errorf("%w: GraphQL route unavailable", ErrUnsupported)
		}
		return ChangeRequest{}, err
	}
	payload := result.Data[mutation]
	if len(result.Errors) > 0 || payload == nil || payload.PullRequest == nil || payload.PullRequest.IsDraft != draft {
		return ChangeRequest{}, fmt.Errorf("changes: GitHub draft mutation failed")
	}
	return c.Get(ctx, r)
}

// Merge requests an immediate merge. ExpectedHead is checked locally and
// passed to the forge for an atomic check; it is never silently dropped.
// GitLab accepts default (project policy) or squash, not a per-call strategy.
// The forge enforces reviews, checks and permissions without forge-review.
func (c *Client) Merge(ctx context.Context, r Ref, o MergeOptions) (MergeResult, error) {
	s, api, _, err := c.locate(r)
	if err != nil {
		return MergeResult{}, err
	}
	if o.ExpectedHead != "" && !commitID(o.ExpectedHead) {
		return MergeResult{}, fmt.Errorf("%w: full lowercase expected head required", ErrInvalid)
	}
	if o.Method == "" {
		if s.Session.Kind == forgeauth.GitLab {
			o.Method = "default"
		} else {
			o.Method = "merge"
		}
	}
	caps, _ := c.Capabilities(s.ID)
	if !slices.Contains(caps.MergeMethods, o.Method) {
		return MergeResult{}, fmt.Errorf("%w: merge method %s", ErrUnsupported, o.Method)
	}
	v, err := c.Get(ctx, r)
	if err != nil {
		return MergeResult{}, err
	}
	if o.ExpectedHead != "" && v.Revision.Head != o.ExpectedHead {
		return MergeResult{}, ErrHeadChanged
	}
	if v.State != Open || v.Draft {
		return MergeResult{}, ErrMergeRejected
	}
	method := "PUT"
	body := map[string]any{}
	switch s.Session.Kind {
	case forgeauth.GitHub:
		body["merge_method"] = o.Method
		if o.ExpectedHead != "" {
			body["sha"] = o.ExpectedHead
		}
	case forgeauth.GitLab:
		if o.Method == "squash" {
			body["squash"] = true
		}
		if o.ExpectedHead != "" {
			body["sha"] = o.ExpectedHead
		}
	case forgeauth.Forgejo:
		method = "POST"
		body["Do"] = o.Method
		if o.ExpectedHead != "" {
			body["head_commit_id"] = o.ExpectedHead
		}
	}
	var response struct {
		Merged          bool
		SHA             string
		State           string
		MergeCommitSHA  string `json:"merge_commit_sha"`
		SquashCommitSHA string `json:"squash_commit_sha"`
	}
	var dest any = &response
	if s.Session.Kind == forgeauth.Forgejo {
		dest = nil
	}
	err = forgerest.Send(ctx, s.Session, method, api+"/merge", body, dest)
	if err != nil {
		var fe *forgehttp.Error
		if errors.As(err, &fe) && (fe.StatusCode == 405 || fe.StatusCode == 409 || fe.StatusCode == 422) {
			// Only GitHub/GitLab document 409 as a head mismatch. Forgejo also
			// uses 409 for other merge conflicts, so retain the generic failure.
			if o.ExpectedHead != "" && fe.StatusCode == 409 && s.Session.Kind != forgeauth.Forgejo {
				return MergeResult{}, fmt.Errorf("%w: %w", ErrHeadChanged, err)
			}
			return MergeResult{}, fmt.Errorf("%w: %w", ErrMergeRejected, err)
		}
		return MergeResult{}, err
	}
	switch s.Session.Kind {
	case forgeauth.GitHub:
		if !response.Merged {
			return MergeResult{}, ErrMergeRejected
		}
		return MergeResult{true, response.SHA}, nil
	case forgeauth.GitLab:
		if response.State != "merged" {
			return MergeResult{}, ErrMergeRejected
		}
		id := response.MergeCommitSHA
		if id == "" {
			id = response.SquashCommitSHA
		}
		return MergeResult{true, id}, nil
	default:
		return MergeResult{Merged: true}, nil
	}
}

func commitID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	nonzero := false
	for _, v := range id {
		if !(v >= '0' && v <= '9' || v >= 'a' && v <= 'f') {
			return false
		}
		nonzero = nonzero || v != '0'
	}
	return nonzero
}

// Readiness reports the conditions exposed by the proposal endpoint. Missing
// review or check information stays unknown; it is never treated as approval.
func (c *Client) Readiness(ctx context.Context, r Ref) (Readiness, error) {
	v, err := c.read(ctx, r)
	if err != nil {
		return Readiness{}, err
	}
	out := Readiness{Revision: v.change.Revision, Status: "unknown", Conditions: []Condition{
		{"state", "met", string(v.change.State)}, {"draft", "met", "ready for review"},
		{"conflicts", "unknown", "not exposed"}, {"reviews", "unknown", "not exposed"},
		{"checks", "unknown", "not exposed"}, {"forge", "unknown", v.mergeStatus},
	}}
	if v.mergeable != nil {
		out.Conditions[2] = Condition{"conflicts", "met", "forge mergeable flag"}
		if !*v.mergeable {
			out.Conditions[2].Status = "unmet"
			out.Status = "blocked"
		}
	}
	switch v.mergeStatus {
	case "clean", "mergeable":
		out.Status = "ready"
		out.Conditions[5].Status = "met"
	case "dirty", "blocked", "behind", "unstable", "draft", "not_open", "conflict", "need_rebase", "not_approved", "discussions_not_resolved", "ci_must_pass", "ci_still_running", "status_checks_must_pass", "requested_changes", "security_policy_violations", "draft_status", "broken_status", "commits_status", "merge_time", "locked_paths", "locked_lfs_files", "jira_association_missing", "title_regex":
		out.Status = "blocked"
		out.Conditions[5].Status = "unmet"
	}
	if v.mergeStatus == "not_approved" || v.mergeStatus == "requested_changes" {
		out.Conditions[3] = Condition{"reviews", "unmet", v.mergeStatus}
	}
	if v.mergeStatus == "ci_must_pass" || v.mergeStatus == "ci_still_running" || v.mergeStatus == "status_checks_must_pass" {
		out.Conditions[4] = Condition{"checks", "unmet", v.mergeStatus}
	}
	if v.mergeable != nil && !*v.mergeable {
		out.Status = "blocked"
	}
	if v.change.Draft {
		out.Status = "blocked"
		out.Conditions[1] = Condition{"draft", "unmet", "draft"}
	}
	if v.change.State != Open {
		out.Status = "blocked"
		out.Conditions[0].Status = "unmet"
	}
	return out, nil
}
