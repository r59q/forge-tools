package issues

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// github is the GitHub REST API.
type github struct{}

type githubIssue struct {
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
	Locked     bool              `json:"locked"`
	HTMLURL    string            `json:"html_url"`
	RepoURL    string            `json:"repository_url"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest json.RawMessage `json:"pull_request"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	ClosedAt    time.Time       `json:"closed_at"`
}

func (github) repoPath(path string) (string, bool) { return ownerName(path) }

func (github) listPath(repo string, f filter) string {
	return repo + "/issues?" + query(url.Values{stateParam: {string(f.state)}, perPage: {"100"}}, f)
}

func (github) accountPath(f filter, scope Scope) string {
	v := url.Values{"filter": {"assigned"}, stateParam: {string(f.state)}, perPage: {"100"}}
	if scope == Created {
		v.Set("filter", "created")
	}

	return "/issues?" + query(v, f)
}

func (github) issuePath(repo string, n int64) string { return repo + "/issues/" + id(n) }

func (github) commentsPath(issue string) (string, string) {
	return issue + "/comments?per_page=100", issue + "/comments"
}

func (github) issue(raw json.RawMessage) (Issue, bool, error) {
	var v githubIssue
	if err := json.Unmarshal(raw, &v); err != nil {
		return Issue{}, false, err
	}

	repo := v.Repository.FullName
	if repo == "" {
		if i := strings.LastIndex(v.RepoURL, "/repos/"); i >= 0 {
			repo, _ = url.PathUnescape(v.RepoURL[i+len("/repos/"):])
		}
	}

	return Issue{
		Repository: repo,
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
		Locked:     v.Locked,
		WebURL:     v.HTMLURL,
		CreatedAt:  v.CreatedAt,
		UpdatedAt:  v.UpdatedAt,
		ClosedAt:   v.ClosedAt,
	}, !present(v.PullRequest), nil
}

func (github) comment(raw json.RawMessage) (Comment, bool, error) { return decodeComment(raw) }

func (g github) create(ctx context.Context, s *forgeauth.Session, repo string, n NewIssue) (Issue, error) {
	body := map[string]any{"title": n.Title, "body": n.Body}
	if len(n.Labels) > 0 {
		body["labels"] = n.Labels
	}
	if len(n.Assignees) > 0 {
		body["assignees"] = n.Assignees
	}

	return send(ctx, s, g, http.MethodPost, repo+"/issues", body)
}

func (g github) update(ctx context.Context, s *forgeauth.Session, issue string, u Update) (Issue, error) {
	body := issueFields(u)
	if u.Labels != nil {
		body["labels"] = list(u.Labels)
	}

	return send(ctx, s, g, http.MethodPatch, issue, body)
}
