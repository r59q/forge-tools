package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
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

// forges holds the supported adapters by kind.
var forges = map[forgeauth.Kind]forge{
	forgeauth.GitHub:  github{},
	forgeauth.GitLab:  gitlab{},
	forgeauth.Forgejo: forgejo{},
}

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
		ID: id(v.ID), Author: v.User.shared(), Body: v.Body, WebURL: v.HTMLURL,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
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
	return repo + "/issues?" + query(url.Values{"state": {string(f.state)}, "per_page": {"100"}}, f)
}

func (github) accountPath(f filter, scope Scope) string {
	v := url.Values{"filter": {"assigned"}, "state": {string(f.state)}, "per_page": {"100"}}
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
		Repository: repo, ID: id(v.ID), Number: v.Number, Title: v.Title, Body: v.Body,
		State: State(v.State), Author: v.User.shared(), Assignees: users(v.Assignees),
		Labels: labelNames(v.Labels), Milestone: v.Milestone.Title, Comments: v.Comments,
		Locked: v.Locked, WebURL: v.HTMLURL, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
		ClosedAt: v.ClosedAt,
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
		v.Set("state", "opened")
	case StateClosed:
		v.Set("state", "closed")
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
	v := url.Values{"per_page": {"100"}}
	g.state(v, f)
	return repo + "/issues?" + query(v, g.labels(f))
}

func (g gitlab) accountPath(f filter, scope Scope) string {
	v := url.Values{"scope": {"assigned_to_me"}, "per_page": {"100"}}
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
		Repository: repo, ID: id(v.ID), Number: v.IID, Title: v.Title, Body: v.Description,
		State: state, Author: v.Author.shared(), Assignees: assignees, Labels: labels,
		Milestone: v.Milestone.Title, Comments: v.UserNotesCount, Locked: v.DiscussionLocked,
		WebURL: v.WebURL, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, ClosedAt: v.ClosedAt,
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
		ID: id(v.ID), Author: v.Author.shared(), Body: v.Body,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
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
	return repo + "/issues?" + url.Values{"type": {"issues"}, "state": {string(f.state)}, "limit": {"50"}}.Encode()
}

func (forgejo) accountPath(f filter, scope Scope) string {
	v := url.Values{"type": {"issues"}, "assigned": {"true"}, "state": {string(f.state)}, "limit": {"50"}}
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
		Repository: v.Repository.FullName, ID: id(v.ID), Number: v.Number, Title: v.Title,
		Body: v.Body, State: State(v.State), Author: v.User.shared(), Assignees: users(v.Assignees),
		Labels: labelNames(v.Labels), Milestone: v.Milestone.Title, Comments: v.Comments,
		Locked: v.IsLocked, WebURL: v.HTMLURL, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
		ClosedAt: v.ClosedAt,
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

// send sends a request and decodes the issue in the response.
func send(ctx context.Context, s *forgeauth.Session, f forge, method, path string, body any) (Issue, error) {
	var raw json.RawMessage
	if err := forgerest.Send(ctx, s, method, path, body, &raw); err != nil {
		return Issue{}, err
	}
	iss, _, err := f.issue(raw)
	return iss, err
}
