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

func numericID(v string) bool {
	n, e := strconv.ParseInt(v, 10, 64)
	return e == nil && n > 0 && strconv.FormatInt(n, 10) == v
}

func discussionID(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}

	for _, r := range v {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
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
		if err = checkComment(ctx, s, path, threadID, sub.Revision.Head); err != nil {
			return NativeResult{}, err
		}
		return sendNative(ctx, sub, s, "POST", path+"/comments/"+threadID+"/replies", "threads", map[string]string{"body": body})
	case forgeauth.GitLab:
		if err = checkDiscussion(ctx, s, path, threadID, sub.Revision.Head, false); err != nil {
			return NativeResult{}, err
		}
		return sendNative(ctx, sub, s, "POST", path+"/discussions/"+url.PathEscape(threadID)+"/notes", "threads", map[string]string{"body": body})
	default:
		return NativeResult{}, ErrUnsupported
	}
}

// githubComment is the part of a GitHub review comment that replies check.
type githubComment struct {
	ID     int64
	Commit string `json:"original_commit_id"`
	Reply  int64  `json:"in_reply_to_id"`
}

// checkComment verifies that a GitHub root comment exists on the given head.
func checkComment(ctx context.Context, s *forgeauth.Session, path, id, head string) error {
	if !numericID(id) {
		return ErrInvalid
	}

	items, err := forgerest.Pages(ctx, s, path+"/comments?per_page=100", func(raw json.RawMessage) (githubComment, error) {
		var v githubComment
		e := json.Unmarshal(raw, &v)

		return v, e
	})
	if err != nil {
		return err
	}

	for _, v := range items {
		if strconv.FormatInt(v.ID, 10) != id {
			continue
		}
		if v.Commit != head || v.Reply != 0 {
			return ErrConflict
		}

		return nil
	}

	return ErrNotFound
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
