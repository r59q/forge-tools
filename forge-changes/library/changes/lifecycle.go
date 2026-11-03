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

	query := "mutation($id:ID!){" + mutation + "(input:{pullRequestId:$id}){pullRequest{isDraft}}}"
	body := map[string]any{"query": query, "variables": map[string]string{"id": v.nodeID}}
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

	method, body := mergeRequest(s.Session.Kind, o)
	var response mergeResponse
	var destination any = &response
	if s.Session.Kind == forgeauth.Forgejo {
		destination = nil
	}

	if err := forgerest.Send(ctx, s.Session, method, api+"/merge", body, destination); err != nil {
		return MergeResult{}, mergeFailure(s.Session.Kind, o.ExpectedHead, err)
	}

	return response.result(s.Session.Kind)
}

func mergeRequest(kind forgeauth.Kind, options MergeOptions) (string, map[string]any) {
	method := "PUT"
	body := map[string]any{}
	switch kind {
	case forgeauth.GitHub:
		body["merge_method"] = options.Method
		if options.ExpectedHead != "" {
			body["sha"] = options.ExpectedHead
		}
	case forgeauth.GitLab:
		if options.Method == "squash" {
			body["squash"] = true
		}
		if options.ExpectedHead != "" {
			body["sha"] = options.ExpectedHead
		}
	case forgeauth.Forgejo:
		method = "POST"
		body["Do"] = options.Method
		if options.ExpectedHead != "" {
			body["head_commit_id"] = options.ExpectedHead
		}
	}

	return method, body
}

func mergeFailure(kind forgeauth.Kind, expectedHead string, err error) error {
	var response *forgehttp.Error
	if !errors.As(err, &response) {
		return err
	}

	switch response.StatusCode {
	case 405, 409, 422:
		// Forgejo also uses 409 for conflicts unrelated to a changed head.
		if expectedHead != "" && response.StatusCode == 409 && kind != forgeauth.Forgejo {
			return fmt.Errorf("%w: %w", ErrHeadChanged, err)
		}
		return fmt.Errorf("%w: %w", ErrMergeRejected, err)
	default:
		return err
	}
}

type mergeResponse struct {
	Merged          bool
	SHA             string
	State           string
	MergeCommitSHA  string `json:"merge_commit_sha"`
	SquashCommitSHA string `json:"squash_commit_sha"`
}

func (r mergeResponse) result(kind forgeauth.Kind) (MergeResult, error) {
	switch kind {
	case forgeauth.GitHub:
		if !r.Merged {
			return MergeResult{}, ErrMergeRejected
		}
		return MergeResult{true, r.SHA}, nil
	case forgeauth.GitLab:
		if r.State != string(Merged) {
			return MergeResult{}, ErrMergeRejected
		}
		commit := r.MergeCommitSHA
		if commit == "" {
			commit = r.SquashCommitSHA
		}
		return MergeResult{true, commit}, nil
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
		if !lowerHex(v) {
			return false
		}

		nonzero = nonzero || v != '0'
	}

	return nonzero
}

// lowerHex reports whether v is a lowercase hexadecimal digit.
func lowerHex(v rune) bool {
	return '0' <= v && v <= '9' || 'a' <= v && v <= 'f'
}

// Readiness reports the conditions exposed by the proposal endpoint. Missing
// review or check information stays unknown; it is never treated as approval.
func (c *Client) Readiness(ctx context.Context, r Ref) (Readiness, error) {
	v, err := c.read(ctx, r)
	if err != nil {
		return Readiness{}, err
	}

	out := Readiness{
		Revision: v.change.Revision,
		Status:   statusUnknown,
		Conditions: []Condition{
			{conditionState, statusMet, string(v.change.State)},
			{conditionDraft, statusMet, "ready for review"},
			{conditionConflicts, statusUnknown, "not exposed"},
			{conditionReviews, statusUnknown, "not exposed"},
			{conditionChecks, statusUnknown, "not exposed"},
			{conditionForge, statusUnknown, v.mergeStatus},
		},
	}
	if v.mergeable != nil {
		out.Conditions[2] = Condition{conditionConflicts, statusMet, "forge mergeable flag"}
		if !*v.mergeable {
			out.Conditions[2].Status = statusUnmet
			out.Status = statusBlocked
		}
	}

	switch {
	case v.mergeStatus == "clean" || v.mergeStatus == "mergeable":
		out.Status = "ready"
		out.Conditions[5].Status = statusMet
	case v.mergeStatus == "unstable":
		if v.change.Kind == forgeauth.GitHub {
			// Non-passing checks may be optional; their requirements are unknown.
			out.Conditions[4] = Condition{conditionChecks, statusUnknown, "non-passing commit status; merge requirements not exposed"}
		}
	case blocking(v.mergeStatus):
		out.Status = statusBlocked
		out.Conditions[5].Status = statusUnmet
	}

	if v.mergeStatus == "not_approved" || v.mergeStatus == "requested_changes" {
		out.Conditions[3] = Condition{conditionReviews, statusUnmet, v.mergeStatus}
	}
	if v.mergeStatus == "ci_must_pass" || v.mergeStatus == "ci_still_running" || v.mergeStatus == "status_checks_must_pass" {
		out.Conditions[4] = Condition{conditionChecks, statusUnmet, v.mergeStatus}
	}
	if v.mergeable != nil && !*v.mergeable {
		out.Status = statusBlocked
	}
	if v.change.Draft {
		out.Status = statusBlocked
		out.Conditions[1] = Condition{conditionDraft, statusUnmet, "draft"}
	}

	if v.change.State != Open {
		out.Status = statusBlocked
		out.Conditions[0].Status = statusUnmet
	}

	return out, nil
}

// blocking reports whether a GitHub mergeable state or GitLab detailed
// merge status blocks merging.
func blocking(status string) bool {
	switch status {
	case "dirty", "blocked", "behind", "draft", "not_open", "conflict", "need_rebase",
		"not_approved", "discussions_not_resolved", "ci_must_pass", "ci_still_running",
		"status_checks_must_pass", "requested_changes", "security_policy_violations",
		"draft_status", "broken_status", "commits_status", "merge_time", "locked_paths",
		"locked_lfs_files", "jira_association_missing", "title_regex":
		return true
	}

	return false
}
