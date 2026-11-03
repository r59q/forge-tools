package issues

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// filter is a checked listing filter. state is never empty.
type filter struct {
	state  StateFilter
	labels []string
}

// forge maps one forge's REST API to the shared model. Adapters are
// internal; callers only see the shared types.
type forge interface {
	// repoPath is the API path of a repository, or false if the forge
	// cannot hold such a path.
	repoPath(path string) (string, bool)
	// listPath is the first page of a repository's issues.
	listPath(repo string, f filter) string
	// accountPath is the first page of the account's issues in scope.
	accountPath(f filter, scope Scope) string
	// issuePath is the API path of one issue.
	issuePath(repo string, number int64) string
	// commentsPath is the first page of an issue's comments, and the path
	// that adds one.
	commentsPath(issue string) (list, add string)
	// issue decodes one issue. It reports false for a pull request.
	issue(raw json.RawMessage) (Issue, bool, error)
	// comment decodes one comment. It reports false for a system note.
	comment(raw json.RawMessage) (Comment, bool, error)
	// create opens an issue in repo; update changes the issue at an issue
	// path. Both return the issue as the forge reports it.
	create(ctx context.Context, s *forgeauth.Session, repo string, n NewIssue) (Issue, error)
	update(ctx context.Context, s *forgeauth.Session, issue string, u Update) (Issue, error)
}

// forges returns the supported adapters by kind.
func forges() map[forgeauth.Kind]forge {
	return map[forgeauth.Kind]forge{
		forgeauth.GitHub:  github{},
		forgeauth.GitLab:  gitlab{},
		forgeauth.Forgejo: forgejo{},
	}
}

// Query parameters shared by several forges.
const (
	perPage    = "per_page"
	stateParam = "state"
)

// user is a forge account in GitHub and Forgejo JSON.
type user struct {
	Login   string `json:"login"`
	HTMLURL string `json:"html_url"`
}

func (u user) shared() User { return User{Login: u.Login, WebURL: u.HTMLURL} }

func users(list []user) []User {
	out := []User{}
	for _, u := range list {
		out = append(out, u.shared())
	}

	return out
}

// label is a label object; GitHub may also send a plain name.
type label struct {
	Name string `json:"name"`
}

func labelNames(raws []json.RawMessage) []string {
	out := []string{}
	for _, raw := range raws {
		var name string
		if json.Unmarshal(raw, &name) != nil {
			var l label
			_ = json.Unmarshal(raw, &l)
			name = l.Name
		}

		if name != "" {
			out = append(out, name)
		}
	}

	return out
}

// titled is an object with a title, such as a milestone.
type titled struct {
	Title string `json:"title"`
}

// comment is a comment in GitHub and Forgejo JSON.
type comment struct {
	ID        int64     `json:"id"`
	User      user      `json:"user"`
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func decodeComment(raw json.RawMessage) (Comment, bool, error) {
	var v comment
	if err := json.Unmarshal(raw, &v); err != nil {
		return Comment{}, false, err
	}

	return Comment{
		ID:        id(v.ID),
		Author:    v.User.shared(),
		Body:      v.Body,
		WebURL:    v.HTMLURL,
		CreatedAt: v.CreatedAt,
		UpdatedAt: v.UpdatedAt,
	}, true, nil
}

// ownerName escapes a two-segment path as "owner/name" for GitHub and
// Forgejo URLs.
func ownerName(path string) (string, bool) {
	owner, name, ok := strings.Cut(path, "/")
	if !ok || strings.Contains(name, "/") {
		return "", false
	}

	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), true
}

// query encodes v, adding the label filter.
func query(v url.Values, f filter) string {
	if len(f.labels) > 0 {
		v.Set("labels", strings.Join(f.labels, ","))
	}

	return v.Encode()
}

// id formats a numeric forge ID.
func id(n int64) string { return strconv.FormatInt(n, 10) }

// present reports whether a JSON field is set and not null.
func present(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

// list returns *p, or an empty slice, so it encodes as [] to clear.
func list(p *[]string) []string {
	if *p == nil {
		return []string{}
	}

	return *p
}

// issueFields is the shared part of GitHub and Forgejo update bodies.
func issueFields(u Update) map[string]any {
	body := map[string]any{}
	if u.Title != nil {
		body["title"] = *u.Title
	}
	if u.Body != nil {
		body["body"] = *u.Body
	}
	if u.State != nil {
		body["state"] = string(*u.State)
	}
	if u.Assignees != nil {
		body["assignees"] = list(u.Assignees)
	}

	return body
}

// send sends a request and decodes the issue in the response.
func send(ctx context.Context, s *forgeauth.Session, f forge, method, path string, body any) (Issue, error) {
	var raw json.RawMessage
	if err := forgerest.Send(ctx, s, method, path, body, &raw); err != nil {
		return Issue{}, err
	}

	iss, _, err := f.issue(raw)

	return iss, err
}
