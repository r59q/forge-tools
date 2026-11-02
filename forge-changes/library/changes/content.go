package changes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

func discussionPath(s Source, api string) string {
	if s.Session.Kind == forgeauth.GitLab {
		return api + "/notes"
	}
	return strings.Replace(api, "/pulls/", "/issues/", 1) + "/comments"
}

type wireComment struct {
	ID                  int64
	User                wireUser
	Author              wireUser
	Body                string
	HTMLURL             string    `json:"html_url"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
	System              bool
	Type                string
	PullRequestReviewID *int64 `json:"pull_request_review_id"`
	Position            json.RawMessage
}

func (w wireComment) general() bool {
	return !w.System && w.Type != "DiffNote" && w.Type != "DiscussionNote" && w.PullRequestReviewID == nil && (len(w.Position) == 0 || string(w.Position) == "null")
}
func (w wireComment) comment(s Source, web string) Comment {
	u := w.User.user()
	link := w.HTMLURL
	if s.Session.Kind == forgeauth.GitLab {
		u = w.Author.user()
		link = web + "#note_" + strconv.FormatInt(w.ID, 10)
	}
	return Comment{strconv.FormatInt(w.ID, 10), u, w.Body, link, w.CreatedAt, w.UpdatedAt}
}

// Comments reads general discussion in forge order (oldest first where
// supported). GitLab system, diff and discussion-thread notes are excluded.
func (c *Client) Comments(ctx context.Context, r Ref) ([]Comment, error) {
	s, api, _, err := c.locate(r)
	if err != nil {
		return nil, err
	}
	proposal, err := c.Get(ctx, r)
	if err != nil {
		return nil, err
	}
	items, err := forgerest.Pages(ctx, s.Session, discussionPath(s, api)+"?per_page=100&limit=100&sort=asc&order_by=created_at", func(raw json.RawMessage) (wireComment, error) {
		var w wireComment
		err := json.Unmarshal(raw, &w)
		return w, err
	})
	if err != nil {
		return nil, err
	}
	var out []Comment
	for _, w := range items {
		if w.general() {
			out = append(out, w.comment(s, proposal.WebURL))
		}
	}
	return out, nil
}

// AddComment adds a general comment. GitLab may execute quick actions in the
// text; a quick-action-only response with no note returns an empty Comment.
func (c *Client) AddComment(ctx context.Context, r Ref, body string) (Comment, error) {
	if strings.TrimSpace(body) == "" {
		return Comment{}, ErrInvalid
	}
	s, api, _, err := c.locate(r)
	if err != nil {
		return Comment{}, err
	}
	proposal, err := c.Get(ctx, r)
	if err != nil {
		return Comment{}, err
	}
	var w wireComment
	if err := forgerest.Send(ctx, s.Session, "POST", discussionPath(s, api), map[string]string{"body": body}, &w); err != nil {
		return Comment{}, err
	}
	if w.ID == 0 {
		return Comment{}, nil
	}
	return w.comment(s, proposal.WebURL), nil
}

// Commits reads the proposal's commits in forge order. GitHub caps its native
// endpoint at 250 commits; reaching that limit returns ErrTooLarge instead of
// silently returning an incomplete history.
func (c *Client) Commits(ctx context.Context, r Ref) ([]Commit, error) {
	s, api, _, err := c.locate(r)
	if err != nil {
		return nil, err
	}
	before, err := c.Get(ctx, r)
	if err != nil {
		return nil, err
	}
	out, err := forgerest.Pages(ctx, s.Session, api+"/commits?per_page=100&limit=100", func(raw json.RawMessage) (Commit, error) {
		var w struct {
			SHA          string
			ID           string
			Message      string
			AuthorName   string    `json:"author_name"`
			AuthorEmail  string    `json:"author_email"`
			AuthoredDate time.Time `json:"authored_date"`
			Commit       struct {
				Message string
				Author  struct {
					Name  string
					Email string
					Date  time.Time
				}
			}
		}
		if err := json.Unmarshal(raw, &w); err != nil {
			return Commit{}, err
		}
		if s.Session.Kind == forgeauth.GitLab {
			return Commit{w.ID, w.Message, w.AuthorName, w.AuthorEmail, w.AuthoredDate}, nil
		}
		return Commit{w.SHA, w.Commit.Message, w.Commit.Author.Name, w.Commit.Author.Email, w.Commit.Author.Date}, nil
	})
	if err != nil {
		return nil, err
	}
	if s.Session.Kind == forgeauth.GitHub && len(out) >= 250 {
		return nil, ErrTooLarge
	}
	if err = c.unchanged(ctx, r, before.Revision); err != nil {
		return nil, err
	}
	return out, nil
}

// Files reads changed paths and any available per-file patch hunks. Forgejo
// exposes metadata only; use Diff for its patch. GitLab completeness is
// unknown because instance diff limits can omit files without a marker.
func (c *Client) Files(ctx context.Context, r Ref) (Files, error) {
	s, api, _, err := c.locate(r)
	if err != nil {
		return Files{}, err
	}
	before, err := c.read(ctx, r)
	if err != nil {
		return Files{}, err
	}
	endpoint := api + "/files?per_page=100&limit=100"
	if s.Session.Kind == forgeauth.GitLab {
		endpoint = api + "/diffs?per_page=100"
	}
	items, err := forgerest.Pages(ctx, s.Session, endpoint, func(raw json.RawMessage) (ChangedFile, error) {
		var w struct {
			Filename         string
			PreviousFilename string `json:"previous_filename"`
			Status           string
			Patch            *string
			OldPath          string `json:"old_path"`
			NewPath          string `json:"new_path"`
			Diff             *string
			NewFile          bool `json:"new_file"`
			DeletedFile      bool `json:"deleted_file"`
			RenamedFile      bool `json:"renamed_file"`
			Collapsed        bool
			TooLarge         bool `json:"too_large"`
		}
		if err := json.Unmarshal(raw, &w); err != nil {
			return ChangedFile{}, err
		}
		v := ChangedFile{Path: w.Filename, OldPath: w.Filename, Status: w.Status, PatchUnavailable: w.Patch == nil}
		if w.Patch != nil {
			v.Patch = *w.Patch
		}
		if w.PreviousFilename != "" {
			v.OldPath = w.PreviousFilename
		}
		if s.Session.Kind == forgeauth.GitLab {
			v = ChangedFile{Path: w.NewPath, OldPath: w.OldPath, Status: "modified", PatchUnavailable: w.Diff == nil || w.Collapsed || w.TooLarge}
			if w.Diff != nil {
				v.Patch = *w.Diff
				if *w.Diff == "" {
					v.PatchUnavailable = true
				}
			}
			switch {
			case w.NewFile:
				v.Status = "added"
			case w.DeletedFile:
				v.Status = "deleted"
			case w.RenamedFile:
				v.Status = "renamed"
			}
		}
		switch v.Status {
		case "added":
			v.OldPath = ""
		case "removed", "deleted":
			v.Status = "deleted"
			v.Path = ""
		case "modified", "renamed", "copied":
		default:
			v.Status = "unknown"
		}
		return v, nil
	})
	if err != nil {
		return Files{}, err
	}
	if err = c.unchanged(ctx, r, before.change.Revision); err != nil {
		return Files{}, err
	}
	completeness := "unknown"
	if before.files != nil {
		if len(items) == *before.files {
			completeness = "complete"
		} else {
			completeness = "truncated"
		}
	}
	if s.Session.Kind == forgeauth.GitHub && len(items) >= 3000 {
		completeness = "truncated"
	}
	return Files{before.change.Revision, items, completeness}, nil
}

// Diff reads at most 16 MiB of the native unified diff. Larger responses fail
// with ErrTooLarge. Missing revision IDs stay empty; no branch is substituted.
func (c *Client) Diff(ctx context.Context, r Ref) (Diff, error) {
	s, api, _, err := c.locate(r)
	if err != nil {
		return Diff{}, err
	}
	before, err := c.Get(ctx, r)
	if err != nil {
		return Diff{}, err
	}
	h := http.Header{}
	switch s.Session.Kind {
	case forgeauth.GitHub:
		h.Set("Accept", "application/vnd.github.diff")
	case forgeauth.GitLab:
		api += "/raw_diffs"
	case forgeauth.Forgejo:
		api += ".diff?binary=true"
	}
	resp, err := forgerest.Open(ctx, s.Session, "GET", api, h)
	if err != nil {
		return Diff{}, err
	}
	defer resp.Body.Close()
	const limit = 16 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return Diff{}, err
	}
	if len(data) > limit {
		return Diff{}, ErrTooLarge
	}
	if err = c.unchanged(ctx, r, before.Revision); err != nil {
		return Diff{}, err
	}
	return Diff{before.Revision, string(data), "unknown"}, nil
}

func (c *Client) unchanged(ctx context.Context, r Ref, revision Revision) error {
	after, err := c.Get(ctx, r)
	if err != nil {
		return err
	}
	if after.Revision != revision {
		return ErrHeadChanged
	}
	return nil
}
