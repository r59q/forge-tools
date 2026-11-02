package review

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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
	v := Capabilities{Kind: string(s.Kind), RequestReview: true, Approve: true, Inline: true}
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
	return NativeResult{Storage: "native", SubjectID: s.ID, Resource: resource, Data: data}
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
	items, err := forgerest.Pages(ctx, s, path+"?per_page=100&limit=100", func(raw json.RawMessage) (json.RawMessage, error) { return raw, nil })
	data, _ := json.Marshal(items)
	return nativeResult(sub, resource, data), err
}

// SubmitNative publishes an assessment immediately. GitLab approvals accept no
// body; submit feedback separately. GitLab changes_requested is unsupported.
// Native writes are not retried: reconcile ambiguous failures with ListNative.
func (c *Client) SubmitNative(ctx context.Context, sid, status, body string) (NativeResult, error) {
	if !assessment(status) || (status != "approved" && strings.TrimSpace(body) == "") {
		return NativeResult{}, ErrInvalid
	}
	sub, s, path, err := c.native(ctx, sid, true)
	if err != nil {
		return NativeResult{}, err
	}
	if s.Kind == forgeauth.GitLab {
		switch status {
		case "changes_requested":
			return NativeResult{}, ErrUnsupported
		case "approved":
			if body != "" {
				return NativeResult{}, ErrUnsupported
			}
			return sendNative(ctx, sub, s, "POST", path+"/approve", "reviews", map[string]string{"sha": sub.Revision.Head})
		default:
			return sendNative(ctx, sub, s, "POST", path+"/discussions", "threads", map[string]string{"body": body})
		}
	}
	event := map[string]string{"commented": "COMMENT", "approved": "APPROVE", "changes_requested": "REQUEST_CHANGES"}[status]
	return sendNative(ctx, sub, s, "POST", path+"/reviews", "reviews", map[string]string{"commit_id": sub.Revision.Head, "event": event, "body": body})
}

// CommentNative immediately publishes general, file or inline feedback.
// GitLab/Forgejo support single-line inline comments here; GitHub also supports
// same-side ranges and file comments. Local drafts are never synchronized.
func (c *Client) CommentNative(ctx context.Context, sid, body string, a *Anchor) (NativeResult, error) {
	if strings.TrimSpace(body) == "" {
		return NativeResult{}, ErrInvalid
	}
	sub, s, path, err := c.native(ctx, sid, true)
	if err != nil {
		return NativeResult{}, err
	}
	if err = ValidateAnchor(sub, a); err != nil {
		return NativeResult{}, err
	}
	if a == nil {
		if s.Kind == forgeauth.GitLab {
			return sendNative(ctx, sub, s, "POST", path+"/discussions", "threads", map[string]string{"body": body})
		}
		return sendNative(ctx, sub, s, "POST", path+"/reviews", "reviews", map[string]string{"body": body, "event": "COMMENT", "commit_id": sub.Revision.Head})
	}
	// Full context can validate local lines outside the diff. Native APIs require
	// the position to exist in the diff itself.
	diffOnly := sub
	diffOnly.Context = nil
	if err = ValidateAnchor(diffOnly, a); err != nil {
		return NativeResult{}, err
	}
	if s.Kind != forgeauth.GitHub && (a.Start == 0 || a.Start != a.End) {
		return NativeResult{}, ErrUnsupported
	}
	nativePath := a.Path
	for _, f := range sub.Files {
		if (a.Side == "old" && f.OldPath == a.Path) || (a.Side == "new" && f.Path == a.Path) {
			if f.Path != "" {
				nativePath = f.Path
			}
			break
		}
	}
	switch s.Kind {
	case forgeauth.GitHub:
		b := map[string]any{"body": body, "commit_id": sub.Revision.Head, "path": nativePath, "side": map[string]string{"old": "LEFT", "new": "RIGHT"}[a.Side]}
		if a.Start == 0 {
			b["subject_type"] = "file"
		} else {
			b["line"] = a.End
			if a.Start != a.End {
				b["start_line"] = a.Start
				b["start_side"] = b["side"]
			}
		}
		return sendNative(ctx, sub, s, "POST", path+"/comments", "threads", b)
	case forgeauth.GitLab:
		if !commitID(sub.Revision.Base) {
			return NativeResult{}, ErrUnsupported
		}
		pos := map[string]any{"position_type": "text", "base_sha": sub.Revision.Base, "head_sha": sub.Revision.Head, "start_sha": sub.Revision.Target}
		for _, f := range sub.Files {
			if (a.Side == "old" && f.OldPath == a.Path) || (a.Side == "new" && f.Path == a.Path) {
				old, new := f.OldPath, f.Path
				if old == "" {
					old = new
				}
				if new == "" {
					new = old
				}
				pos["old_path"] = old
				pos["new_path"] = new
			}
		}
		pos[a.Side+"_line"] = a.Start
		parsed, _ := parsePatch(sub.Patch)
		if other := parsed.pairs[fileSide{a.Path, a.Side}][a.Start]; other > 0 {
			side := "old"
			if a.Side == "old" {
				side = "new"
			}
			pos[side+"_line"] = other
		}
		return sendNative(ctx, sub, s, "POST", path+"/discussions", "threads", map[string]any{"body": body, "position": pos})
	default:
		b := map[string]any{"body": body, "path": a.Path, a.Side + "_position": a.Start}
		return sendNative(ctx, sub, s, "POST", path+"/reviews", "reviews", map[string]any{"event": "COMMENT", "commit_id": sub.Revision.Head, "comments": []any{b}})
	}
}
func numericID(v string) bool {
	n, e := strconv.ParseInt(v, 10, 64)
	return e == nil && n > 0 && strconv.FormatInt(n, 10) == v
}
func discussionID(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// ReplyNative replies to a native GitHub root comment or GitLab discussion.
// The original thread must refer to this snapshot's head. General discussions
// with no revision metadata cannot be validated and are unsupported here.
func (c *Client) ReplyNative(ctx context.Context, sid, threadID, body string) (NativeResult, error) {
	if strings.TrimSpace(body) == "" {
		return NativeResult{}, ErrInvalid
	}
	sub, s, path, err := c.native(ctx, sid, false)
	if err != nil {
		return NativeResult{}, err
	}
	switch s.Kind {
	case forgeauth.GitHub:
		if !numericID(threadID) {
			return NativeResult{}, ErrInvalid
		}
		items, err := forgerest.Pages(ctx, s, path+"/comments?per_page=100", func(raw json.RawMessage) (struct {
			ID     int64
			Commit string `json:"original_commit_id"`
			Reply  int64  `json:"in_reply_to_id"`
		}, error) {
			var v struct {
				ID     int64
				Commit string `json:"original_commit_id"`
				Reply  int64  `json:"in_reply_to_id"`
			}
			e := json.Unmarshal(raw, &v)
			return v, e
		})
		if err != nil {
			return NativeResult{}, err
		}
		for _, v := range items {
			if strconv.FormatInt(v.ID, 10) == threadID {
				if v.Commit != sub.Revision.Head || v.Reply != 0 {
					return NativeResult{}, ErrConflict
				}
				return sendNative(ctx, sub, s, "POST", path+"/comments/"+threadID+"/replies", "threads", map[string]string{"body": body})
			}
		}
		return NativeResult{}, ErrNotFound
	case forgeauth.GitLab:
		if err = checkDiscussion(ctx, s, path, threadID, sub.Revision.Head, false); err != nil {
			return NativeResult{}, err
		}
		return sendNative(ctx, sub, s, "POST", path+"/discussions/"+url.PathEscape(threadID)+"/notes", "threads", map[string]string{"body": body})
	default:
		return NativeResult{}, ErrUnsupported
	}
}
func checkDiscussion(ctx context.Context, s *forgeauth.Session, path, tid, head string, resolve bool) error {
	if !discussionID(tid) {
		return ErrInvalid
	}
	var v struct {
		Notes []struct {
			Resolvable bool
			Position   *struct {
				Head string `json:"head_sha"`
			}
		}
	}
	if err := forgerest.Send(ctx, s, "GET", path+"/discussions/"+url.PathEscape(tid), nil, &v); err != nil {
		return err
	}
	if len(v.Notes) == 0 {
		return ErrNotFound
	}
	n := v.Notes[0]
	if n.Position == nil {
		return fmt.Errorf("%w: thread revision unavailable", ErrUnsupported)
	}
	if n.Position.Head != head {
		return ErrConflict
	}
	if resolve && !n.Resolvable {
		return ErrUnsupported
	}
	return nil
}

// ResolveNative resolves or reopens a GitLab diff discussion at its original
// snapshot. Other adapters return ErrUnsupported; no local state is changed.
func (c *Client) ResolveNative(ctx context.Context, sid, threadID string, resolved bool) (NativeResult, error) {
	sub, s, path, err := c.native(ctx, sid, false)
	if err != nil {
		return NativeResult{}, err
	}
	if s.Kind != forgeauth.GitLab {
		return NativeResult{}, ErrUnsupported
	}
	if err = checkDiscussion(ctx, s, path, threadID, sub.Revision.Head, true); err != nil {
		return NativeResult{}, err
	}
	return sendNative(ctx, sub, s, "PUT", path+"/discussions/"+url.PathEscape(threadID), "threads", map[string]bool{"resolved": resolved})
}
