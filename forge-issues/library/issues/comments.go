package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// Comments returns the comments on the issue ref names, oldest first.
// GitLab system notes are left out.
func (c *Client) Comments(ctx context.Context, ref Ref) ([]Comment, error) {
	s, iss, api, err := c.get(ctx, ref)
	if err != nil {
		return nil, err
	}

	list, _ := s.forge.commentsPath(api)
	type entry struct {
		comment Comment
		keep    bool
	}
	entries, err := forgerest.Pages(ctx, s.Session, list, func(raw json.RawMessage) (entry, error) {
		cm, keep, err := s.forge.comment(raw)
		return entry{cm, keep}, err
	})
	if err = gone(err); err != nil {
		return nil, fmt.Errorf("issues: source %s: %w", s.ID, err)
	}

	out := []Comment{}
	for _, e := range entries {
		if e.keep {
			out = append(out, withURL(e.comment, iss))
		}
	}

	return out, nil
}

// AddComment adds a comment to the issue ref names and returns it.
// ref.Source and a non-empty body are required. On GitLab, a body of only
// quick actions, such as "/close", runs them and adds no comment; the
// returned Comment then has no ID.
func (c *Client) AddComment(ctx context.Context, ref Ref, body string) (Comment, error) {
	if strings.TrimSpace(body) == "" {
		return Comment{}, fmt.Errorf("%w: empty comment", ErrInvalid)
	}
	if ref.Source == "" {
		return Comment{}, fmt.Errorf("%w: source required", ErrInvalid)
	}

	// Read the issue first, so a pull request never gets the comment.
	s, iss, api, err := c.get(ctx, ref)
	if err != nil {
		return Comment{}, err
	}

	_, add := s.forge.commentsPath(api)
	var raw json.RawMessage
	if err := forgerest.Send(ctx, s.Session, "POST", add, map[string]string{"body": body}, &raw); err != nil {
		return Comment{}, fmt.Errorf("issues: source %s: %w", s.ID, err)
	}

	cm, kept, err := s.forge.comment(raw)
	if err != nil {
		return Comment{}, fmt.Errorf("issues: source %s: %w", s.ID, err)
	}
	if !kept {
		return Comment{}, nil // only quick actions ran
	}

	return withURL(cm, iss), nil
}

// withURL fills a comment's missing web URL from its issue, as GitLab
// notes have none.
func withURL(cm Comment, iss Issue) Comment {
	if cm.WebURL == "" && cm.ID != "" && iss.WebURL != "" {
		cm.WebURL = iss.WebURL + "#note_" + cm.ID
	}

	return cm
}
