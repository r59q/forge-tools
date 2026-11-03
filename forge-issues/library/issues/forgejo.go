package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// forgejo is the Forgejo REST API (v1). Its page size parameter is limit.
type forgejo struct{}

type forgejoIssue struct {
	ID         int64             `json:"id"`
	Number     int64             `json:"number"`
	Title      string            `json:"title"`
	Body       string            `json:"body"`
	State      string            `json:"state"`
	User       user              `json:"user"`
	Assignees  []user            `json:"assignees"`
	Labels     []json.RawMessage `json:"labels"`
	Milestone  titled            `json:"milestone"`
	Comments   int               `json:"comments"`
	IsLocked   bool              `json:"is_locked"`
	HTMLURL    string            `json:"html_url"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest json.RawMessage `json:"pull_request"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	ClosedAt    time.Time       `json:"closed_at"`
}

func (forgejo) repoPath(path string) (string, bool) { return ownerName(path) }

// listPath sends no label filter: Forgejo would also require organization
// labels of the same name. The client filters instead.
func (forgejo) listPath(repo string, f filter) string {
	return repo + "/issues?" + url.Values{"type": {"issues"}, stateParam: {string(f.state)}, "limit": {"50"}}.Encode()
}

func (forgejo) accountPath(f filter, scope Scope) string {
	v := url.Values{
		"type":     {"issues"},
		"assigned": {"true"},
		stateParam: {string(f.state)},
		"limit":    {"50"},
	}
	if scope == Created {
		v.Del("assigned")
		v.Set("created", "true")
	}

	return "/repos/issues/search?" + query(v, f)
}

func (forgejo) issuePath(repo string, n int64) string { return repo + "/issues/" + id(n) }

// commentsPath has no page size: Forgejo returns every comment at once.
func (forgejo) commentsPath(issue string) (string, string) {
	return issue + "/comments", issue + "/comments"
}

func (forgejo) issue(raw json.RawMessage) (Issue, bool, error) {
	var v forgejoIssue
	if err := json.Unmarshal(raw, &v); err != nil {
		return Issue{}, false, err
	}

	return Issue{
		Repository: v.Repository.FullName,
		ID:         id(v.ID),
		Number:     v.Number,
		Title:      v.Title,
		Body:       v.Body,
		State:      State(v.State),
		Author:     v.User.shared(),
		Assignees:  users(v.Assignees),
		Labels:     labelNames(v.Labels),
		Milestone:  v.Milestone.Title,
		Comments:   v.Comments,
		Locked:     v.IsLocked,
		WebURL:     v.HTMLURL,
		CreatedAt:  v.CreatedAt,
		UpdatedAt:  v.UpdatedAt,
		ClosedAt:   v.ClosedAt,
	}, !present(v.PullRequest), nil
}

func (forgejo) comment(raw json.RawMessage) (Comment, bool, error) { return decodeComment(raw) }

// create opens the issue in one call, with labels looked up by name.
func (f forgejo) create(ctx context.Context, s *forgeauth.Session, repo string, n NewIssue) (Issue, error) {
	body := map[string]any{"title": n.Title, "body": n.Body}
	if len(n.Labels) > 0 {
		ids, err := f.labelIDs(ctx, s, repo, n.Labels)
		if err != nil {
			return Issue{}, err
		}

		body["labels"] = ids
	}

	if len(n.Assignees) > 0 {
		body["assignees"] = n.Assignees
	}

	return send(ctx, s, f, http.MethodPost, repo+"/issues", body)
}

// update checks assignees and looks up labels first, since Forgejo answers
// an unknown assignee with 500. It then changes the other fields and sets
// the labels last; if only the labels fail, the error says so.
func (f forgejo) update(ctx context.Context, s *forgeauth.Session, issue string, u Update) (Issue, error) {
	if u.Assignees != nil {
		if err := f.checkUsers(ctx, s, *u.Assignees); err != nil {
			return Issue{}, err
		}
	}

	var ids []int64
	if u.Labels != nil && len(*u.Labels) > 0 {
		var err error
		if ids, err = f.labelIDs(ctx, s, issue[:strings.LastIndex(issue, "/issues/")], *u.Labels); err != nil {
			return Issue{}, err
		}
	}

	var iss Issue
	var err error
	body := issueFields(u)
	if len(body) > 0 {
		iss, err = send(ctx, s, f, http.MethodPatch, issue, body)
	} else {
		iss, err = send(ctx, s, f, http.MethodGet, issue, nil)
	}

	if err != nil || u.Labels == nil {
		return iss, err
	}

	labels, err := f.setLabels(ctx, s, issue, ids)
	if err != nil {
		if len(body) > 0 {
			return Issue{}, fmt.Errorf("issue updated, but labels failed: %w", err)
		}

		return Issue{}, err
	}

	iss.Labels = labels

	return iss, nil
}

// checkUsers checks that each login exists. An unknown login is
// ErrInvalid.
func (forgejo) checkUsers(ctx context.Context, s *forgeauth.Session, logins []string) error {
	for _, login := range logins {
		err := forgerest.Send(ctx, s, http.MethodGet, "/users/"+url.PathEscape(login), nil, nil)
		if errors.Is(err, forgehttp.ErrNotFound) {
			return fmt.Errorf("%w: unknown user %q", ErrInvalid, login)
		}
		if err != nil {
			return err
		}
	}

	return nil
}

// labelIDs returns the IDs of the repository and organization labels named
// in names. Unknown names are left out. An owner that is not an
// organization has no organization labels.
func (forgejo) labelIDs(ctx context.Context, s *forgeauth.Session, repo string, names []string) ([]int64, error) {
	owner := strings.Split(repo, "/")[2] // repo is /repos/{owner}/{name}
	type entry struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	decode := func(raw json.RawMessage) (entry, error) {
		var e entry
		err := json.Unmarshal(raw, &e)

		return e, err
	}
	ids := []int64{}
	for _, list := range []string{repo + "/labels?limit=50", "/orgs/" + owner + "/labels?limit=50"} {
		found, err := forgerest.Pages(ctx, s, list, decode)
		if errors.Is(err, forgehttp.ErrNotFound) && strings.HasPrefix(list, "/orgs/") {
			continue
		}
		if err != nil {
			return nil, err
		}

		for _, l := range found {
			if slices.Contains(names, l.Name) {
				ids = append(ids, l.ID)
			}
		}
	}

	return ids, nil
}

// setLabels replaces an issue's labels by ID and returns the names set. No
// IDs clears them.
func (forgejo) setLabels(ctx context.Context, s *forgeauth.Session, issue string, ids []int64) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, forgerest.Send(ctx, s, http.MethodDelete, issue+"/labels", nil, nil)
	}

	var set []json.RawMessage
	if err := forgerest.Send(ctx, s, http.MethodPut, issue+"/labels", map[string]any{"labels": ids}, &set); err != nil {
		return nil, err
	}

	return labelNames(set), nil
}
