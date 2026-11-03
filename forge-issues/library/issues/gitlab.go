package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// gitlab is the GitLab REST API (v4). Issue numbers are iids.
type gitlab struct{}

type gitlabUser struct {
	Username string `json:"username"`
	WebURL   string `json:"web_url"`
}

func (u gitlabUser) shared() User { return User{Login: u.Username, WebURL: u.WebURL} }

type gitlabIssue struct {
	ID               int64        `json:"id"`
	IID              int64        `json:"iid"`
	Title            string       `json:"title"`
	Description      string       `json:"description"`
	State            string       `json:"state"`
	Author           gitlabUser   `json:"author"`
	Assignees        []gitlabUser `json:"assignees"`
	Labels           []string     `json:"labels"`
	Milestone        titled       `json:"milestone"`
	UserNotesCount   int          `json:"user_notes_count"`
	DiscussionLocked bool         `json:"discussion_locked"`
	WebURL           string       `json:"web_url"`
	References       struct {
		Full string `json:"full"`
	} `json:"references"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ClosedAt  time.Time `json:"closed_at"`
}

// repoPath encodes the full path as one segment, such as
// /projects/group%2Fsub%2Fproject.
func (gitlab) repoPath(path string) (string, bool) {
	return "/projects/" + url.PathEscape(path), true
}

// state maps a StateFilter to GitLab's; all is no filter.
func (gitlab) state(v url.Values, f filter) {
	switch f.state {
	case StateOpen:
		v.Set(stateParam, "opened")
	case StateClosed:
		v.Set(stateParam, "closed")
	}
}

// labels drops None and Any from the label filter, as GitLab reads them as
// "no labels" and "any label". The client still filters for them.
func (gitlab) labels(f filter) filter {
	kept := []string{}
	for _, l := range f.labels {
		if !strings.EqualFold(l, "None") && !strings.EqualFold(l, "Any") {
			kept = append(kept, l)
		}
	}

	f.labels = kept

	return f
}

func (g gitlab) listPath(repo string, f filter) string {
	v := url.Values{perPage: {"100"}}
	g.state(v, f)

	return repo + "/issues?" + query(v, g.labels(f))
}

func (g gitlab) accountPath(f filter, scope Scope) string {
	v := url.Values{"scope": {"assigned_to_me"}, perPage: {"100"}}
	if scope == Created {
		v.Set("scope", "created_by_me")
	}

	g.state(v, f)

	return "/issues?" + query(v, g.labels(f))
}

func (gitlab) issuePath(repo string, n int64) string { return repo + "/issues/" + id(n) }

func (gitlab) commentsPath(issue string) (string, string) {
	return issue + "/notes?order_by=created_at&per_page=100&sort=asc", issue + "/notes"
}

func (gitlab) issue(raw json.RawMessage) (Issue, bool, error) {
	var v gitlabIssue
	if err := json.Unmarshal(raw, &v); err != nil {
		return Issue{}, false, err
	}

	repo := v.References.Full
	if i := strings.LastIndexByte(repo, '#'); i >= 0 {
		repo = repo[:i]
	}
	if repo == "" {
		if u, err := url.Parse(v.WebURL); err == nil {
			repo, _, _ = strings.Cut(strings.Trim(u.Path, "/"), "/-/")
		}
	}

	state := State(v.State)
	if v.State == "opened" {
		state = Open
	}

	assignees := []User{}
	for _, a := range v.Assignees {
		assignees = append(assignees, a.shared())
	}

	labels := v.Labels
	if labels == nil {
		labels = []string{}
	}

	return Issue{
		Repository: repo,
		ID:         id(v.ID),
		Number:     v.IID,
		Title:      v.Title,
		Body:       v.Description,
		State:      state,
		Author:     v.Author.shared(),
		Assignees:  assignees,
		Labels:     labels,
		Milestone:  v.Milestone.Title,
		Comments:   v.UserNotesCount,
		Locked:     v.DiscussionLocked,
		WebURL:     v.WebURL,
		CreatedAt:  v.CreatedAt,
		UpdatedAt:  v.UpdatedAt,
		ClosedAt:   v.ClosedAt,
	}, true, nil
}

// comment reports false for a system note, and for the reply to a body of
// only quick actions, which runs them and creates no note.
func (gitlab) comment(raw json.RawMessage) (Comment, bool, error) {
	var v struct {
		ID        int64           `json:"id"`
		Body      string          `json:"body"`
		Author    gitlabUser      `json:"author"`
		System    bool            `json:"system"`
		Commands  json.RawMessage `json:"commands_changes"`
		CreatedAt time.Time       `json:"created_at"`
		UpdatedAt time.Time       `json:"updated_at"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return Comment{}, false, err
	}
	if v.ID == 0 || present(v.Commands) {
		return Comment{}, false, nil
	}

	return Comment{
		ID:        id(v.ID),
		Author:    v.Author.shared(),
		Body:      v.Body,
		CreatedAt: v.CreatedAt,
		UpdatedAt: v.UpdatedAt,
	}, !v.System, nil
}

func (g gitlab) create(ctx context.Context, s *forgeauth.Session, repo string, n NewIssue) (Issue, error) {
	body := map[string]any{"title": n.Title, "description": n.Body}
	if len(n.Labels) > 0 {
		body["labels"] = strings.Join(n.Labels, ",")
	}
	if len(n.Assignees) > 0 {
		ids, err := g.userIDs(ctx, s, n.Assignees)
		if err != nil {
			return Issue{}, err
		}

		body["assignee_ids"] = ids
	}

	return send(ctx, s, g, http.MethodPost, repo+"/issues", body)
}

func (g gitlab) update(ctx context.Context, s *forgeauth.Session, issue string, u Update) (Issue, error) {
	body := map[string]any{}
	if u.Title != nil {
		body["title"] = *u.Title
	}
	if u.Body != nil {
		body["description"] = *u.Body
	}
	if u.State != nil {
		body["state_event"] = map[State]string{Open: "reopen", Closed: "close"}[*u.State]
	}
	if u.Labels != nil {
		// An empty string removes every label.
		body["labels"] = strings.Join(*u.Labels, ",")
	}
	if u.Assignees != nil {
		// The ID 0 unassigns everyone.
		ids := []int64{0}
		if len(*u.Assignees) > 0 {
			var err error
			if ids, err = g.userIDs(ctx, s, *u.Assignees); err != nil {
				return Issue{}, err
			}
		}

		body["assignee_ids"] = ids
	}

	return send(ctx, s, g, http.MethodPut, issue, body)
}

// userIDs looks up the user ID of each login. An unknown login is
// ErrInvalid.
func (gitlab) userIDs(ctx context.Context, s *forgeauth.Session, logins []string) ([]int64, error) {
	ids := make([]int64, 0, len(logins))
	for _, login := range logins {
		var found []struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		}
		if err := forgerest.Send(ctx, s, http.MethodGet, "/users?username="+url.QueryEscape(login), nil, &found); err != nil {
			return nil, err
		}

		id := int64(0)
		for _, u := range found {
			if strings.EqualFold(u.Username, login) {
				id = u.ID
				break
			}
		}

		if id == 0 {
			return nil, fmt.Errorf("%w: unknown user %q", ErrInvalid, login)
		}

		ids = append(ids, id)
	}

	return ids, nil
}
