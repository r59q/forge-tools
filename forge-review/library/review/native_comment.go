package review

import (
	"context"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

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
		return generalNativeComment(ctx, sub, s, path, body)
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

	nativePath := nativeCommentPath(sub, a)

	switch s.Kind {
	case forgeauth.GitHub:
		return sendNative(ctx, sub, s, "POST", path+"/comments", "threads", githubCommentBody(sub, a, body, nativePath))
	case forgeauth.GitLab:
		position, err := gitlabCommentPosition(sub, a)
		if err != nil {
			return NativeResult{}, err
		}
		return sendNative(ctx, sub, s, "POST", path+"/discussions", "threads", map[string]any{"body": body, "position": position})
	default:
		b := map[string]any{"body": body, "path": a.Path, a.Side + "_position": a.Start}
		return postReview(ctx, sub, s, path, map[string]any{"event": "COMMENT", "comments": []any{b}})
	}
}

func generalNativeComment(ctx context.Context, sub Subject, session *forgeauth.Session, path, body string) (NativeResult, error) {
	if session.Kind == forgeauth.GitLab {
		return sendNative(ctx, sub, session, "POST", path+"/discussions", "threads", map[string]string{"body": body})
	}

	return postReview(ctx, sub, session, path, map[string]any{"body": body, "event": "COMMENT"})
}

func nativeCommentPath(sub Subject, a *Anchor) string {
	nativePath := a.Path
	for _, f := range sub.Files {
		if (a.Side == "old" && f.OldPath == a.Path) || (a.Side == "new" && f.Path == a.Path) {
			if f.Path != "" {
				nativePath = f.Path
			}

			break
		}
	}

	return nativePath
}

func githubCommentBody(sub Subject, a *Anchor, body, path string) map[string]any {
	b := map[string]any{
		"body":      body,
		"commit_id": sub.Revision.Head,
		"path":      path,
		"side":      map[string]string{"old": "LEFT", "new": "RIGHT"}[a.Side],
	}
	if a.Start == 0 {
		b["subject_type"] = "file"
	} else {
		b["line"] = a.End
		if a.Start != a.End {
			b["start_line"] = a.Start
			b["start_side"] = b["side"]
		}
	}
	return b
}

func gitlabCommentPosition(sub Subject, a *Anchor) (map[string]any, error) {
	if !commitID(sub.Revision.Base) {
		return nil, ErrUnsupported
	}
	pos := map[string]any{
		"position_type": "text",
		"base_sha":      sub.Revision.Base,
		"head_sha":      sub.Revision.Head,
		"start_sha":     sub.Revision.Target,
	}
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
	return pos, nil
}
