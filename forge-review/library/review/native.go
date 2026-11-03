package review

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// Capabilities describes this adapter, subject to forge permissions/version.
// Unsupported operations fail before writing and never fall back to local.
type Capabilities struct {
	Kind           string `json:"kind"`
	RequestReview  bool   `json:"requestReview"`
	Approve        bool   `json:"approve"`
	RequestChanges bool   `json:"requestChanges"`
	Inline         bool   `json:"inline"`
	Ranges         bool   `json:"ranges"`
	FileComments   bool   `json:"fileComments"`
	Replies        bool   `json:"replies"`
	Resolve        bool   `json:"resolve"`
}

// Capabilities returns native adapter support for a configured source.
func (c *Client) Capabilities(source string) (Capabilities, error) {
	s := c.sources[source]
	if s == nil {
		return Capabilities{}, ErrInvalid
	}

	v := Capabilities{
		Kind:          string(s.Kind),
		RequestReview: true,
		Approve:       true,
		Inline:        true,
	}
	switch s.Kind {
	case forgeauth.GitHub:
		v.RequestChanges = true
		v.Ranges = true
		v.FileComments = true
		v.Replies = true
	case forgeauth.GitLab:
		v.Replies = true
		v.Resolve = true
	case forgeauth.Forgejo:
		v.RequestChanges = true
	}

	return v, nil
}

// NativeResult contains an unmodified forge JSON response or paginated array.
// Storage is native: mutations are published immediately as the session user.
// SubjectID identifies the fixed local snapshot, not a local Review record.
// Lists retain upstream revision fields; they may include older/newer reviews.
type NativeResult struct {
	Storage   string          `json:"storage"`
	SubjectID string          `json:"subjectId"`
	Resource  string          `json:"resource"`
	Data      json.RawMessage `json:"data"`
}

func nativeResult(s Subject, resource string, data json.RawMessage) NativeResult {
	if len(data) == 0 {
		data = json.RawMessage(`null`)
	}

	return NativeResult{
		Storage:   "native",
		SubjectID: s.ID,
		Resource:  resource,
		Data:      data,
	}
}

func (c *Client) native(ctx context.Context, sid string, guard bool) (Subject, *forgeauth.Session, string, error) {
	sub, err := c.GetSubject(ctx, sid)
	if err != nil {
		return sub, nil, "", err
	}
	if sub.Kind != "change_request" {
		return sub, nil, "", ErrUnsupported
	}

	s, _, request, err := c.locate(sub.Ref)
	if err != nil {
		return sub, nil, "", err
	}
	if guard {
		rev, err := revision(ctx, s, request)
		if err != nil {
			return sub, nil, "", err
		}
		if rev != sub.Revision {
			return sub, nil, "", ErrConflict
		}
	}

	return sub, s, request, nil
}

func sendNative(ctx context.Context, sub Subject, s *forgeauth.Session, method, path, resource string, body any) (NativeResult, error) {
	var data json.RawMessage
	err := forgerest.Send(ctx, s, method, path, body, &data)

	return nativeResult(sub, resource, data), err
}

// RequestNativeReview publishes reviewer assignments. GitHub/Forgejo take
// usernames; GitLab takes numeric user IDs and replaces its reviewer list.
func (c *Client) RequestNativeReview(ctx context.Context, sid string, reviewers []string) (NativeResult, error) {
	if len(reviewers) == 0 {
		return NativeResult{}, ErrInvalid
	}

	for _, r := range reviewers {
		if strings.TrimSpace(r) == "" {
			return NativeResult{}, ErrInvalid
		}
	}

	sub, s, path, err := c.native(ctx, sid, true)
	if err != nil {
		return NativeResult{}, err
	}
	if s.Kind == forgeauth.GitLab {
		ids := make([]int64, len(reviewers))
		for i, v := range reviewers {
			n, e := strconv.ParseInt(v, 10, 64)
			if e != nil || n <= 0 {
				return NativeResult{}, ErrInvalid
			}

			ids[i] = n
		}

		return sendNative(ctx, sub, s, "PUT", path, "reviewers", map[string]any{"reviewer_ids": ids})
	}

	return sendNative(ctx, sub, s, "POST", path+"/requested_reviewers", "reviewers", map[string]any{"reviewers": reviewers})
}

// ListNative retrieves reviews, threads or reviewers with native IDs/statuses.
// Forgejo threads require a native review ID. GitLab reviews are approval state;
// its feedback is in threads. No list is silently filtered to the snapshot head.
func (c *Client) ListNative(ctx context.Context, sid, resource, reviewID string) (NativeResult, error) {
	sub, s, path, err := c.native(ctx, sid, false)
	if err != nil {
		return NativeResult{}, err
	}

	switch resource {
	case "reviews":
		if s.Kind == forgeauth.GitLab {
			return sendNative(ctx, sub, s, "GET", path+"/approvals", resource, nil)
		}
		path += "/reviews"
	case "threads":
		switch s.Kind {
		case forgeauth.GitHub:
			path += "/comments"
		case forgeauth.GitLab:
			path += "/discussions"
		case forgeauth.Forgejo:
			if !numericID(reviewID) {
				return NativeResult{}, ErrInvalid
			}
			path += "/reviews/" + reviewID + "/comments"
		}
	case "reviewers":
		if s.Kind == forgeauth.GitLab || s.Kind == forgeauth.Forgejo {
			return sendNative(ctx, sub, s, "GET", path, resource, nil)
		}
		return sendNative(ctx, sub, s, "GET", path+"/requested_reviewers", resource, nil)
	default:
		return NativeResult{}, ErrInvalid
	}

	items, err := forgerest.Pages(
		ctx,
		s,
		path+"?per_page=100&limit=100",
		func(raw json.RawMessage) (json.RawMessage, error) { return raw, nil },
	)
	data, _ := json.Marshal(items)

	return nativeResult(sub, resource, data), err
}

// SubmitNative publishes an assessment immediately. GitLab approvals accept no
// body; submit feedback separately. GitLab changes_requested is unsupported.
// Native writes are not retried: reconcile ambiguous failures with ListNative.
func (c *Client) SubmitNative(ctx context.Context, sid, status, body string) (NativeResult, error) {
	if !assessment(status) || (status != StatusApproved && strings.TrimSpace(body) == "") {
		return NativeResult{}, ErrInvalid
	}

	sub, s, path, err := c.native(ctx, sid, true)
	if err != nil {
		return NativeResult{}, err
	}
	if s.Kind == forgeauth.GitLab {
		return submitGitLab(ctx, sub, s, path, status, body)
	}

	event := map[string]string{
		StatusCommented:        "COMMENT",
		StatusApproved:         "APPROVE",
		StatusChangesRequested: "REQUEST_CHANGES",
	}[status]
	if s.Kind == forgeauth.Forgejo && status == StatusApproved {
		event = "APPROVED"
	}

	return postReview(ctx, sub, s, path, map[string]any{"event": event, "body": body})
}

func submitGitLab(ctx context.Context, sub Subject, s *forgeauth.Session, path, status, body string) (NativeResult, error) {
	switch status {
	case StatusChangesRequested:
		return NativeResult{}, ErrUnsupported
	case StatusApproved:
		if body != "" {
			return NativeResult{}, ErrUnsupported
		}

		return sendNative(ctx, sub, s, "POST", path+"/approve", "reviews", map[string]string{"sha": sub.Revision.Head})
	default:
		return sendNative(ctx, sub, s, "POST", path+"/discussions", "threads", map[string]string{"body": body})
	}
}

// postReview creates a GitHub or Forgejo review on the snapshot head.
func postReview(ctx context.Context, sub Subject, s *forgeauth.Session, path string, fields map[string]any) (NativeResult, error) {
	fields["commit_id"] = sub.Revision.Head

	return sendNative(ctx, sub, s, "POST", path+"/reviews", "reviews", fields)
}
