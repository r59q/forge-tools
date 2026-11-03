package changes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// Client manages proposals using caller-owned sessions. It is safe for
// concurrent use provided those sessions are not mutated.
type Client struct {
	sources []Source
	byID    map[string]Source
}

// New validates the sources. No credentials are read or stored by this tool.
func New(sources ...Source) (*Client, error) {
	c := &Client{byID: make(map[string]Source)}
	for _, s := range sources {
		if strings.TrimSpace(s.ID) == "" || s.Session == nil || s.Session.Client == nil {
			return nil, ErrInvalid
		}
		if _, ok := c.byID[s.ID]; ok {
			return nil, ErrInvalid
		}

		switch s.Session.Kind {
		case forgeauth.GitHub, forgeauth.GitLab, forgeauth.Forgejo:
		default:
			return nil, ErrUnsupported
		}

		c.byID[s.ID] = s
		c.sources = append(c.sources, s)
	}

	return c, nil
}

// Sources returns a copy of the configured sources, in order.
func (c *Client) Sources() []Source { return append([]Source{}, c.sources...) }

// Capabilities returns the adapter's capabilities for a source.
func (c *Client) Capabilities(id string) (Capabilities, error) {
	s, ok := c.byID[id]
	if !ok {
		return Capabilities{}, ErrUnknownSource
	}

	m := []string{"merge", "squash", "rebase"}
	switch s.Session.Kind {
	case forgeauth.GitLab:
		m = []string{"default", "squash"}
	case forgeauth.Forgejo:
		m = append(m, "rebase-merge", "fast-forward-only")
	}

	return Capabilities{DraftTransition: true, MergeMethods: m, ExpectedHead: true}, nil
}

func (c *Client) repository(r RepositoryRef) (Source, string, string, error) {
	if r.Source == "" {
		return Source{}, "", "", fmt.Errorf("%w: source required", ErrInvalid)
	}

	s, ok := c.byID[r.Source]
	if !ok {
		return Source{}, "", "", ErrUnknownSource
	}

	p, ok := forgerest.CleanPath(r.Repository)
	if !ok {
		return Source{}, "", "", fmt.Errorf("%w: repository path", ErrInvalid)
	}
	if s.Session.Kind == forgeauth.GitLab {
		return s, "/projects/" + url.PathEscape(p), p, nil
	}

	parts := strings.Split(p, "/")
	if len(parts) != 2 {
		return Source{}, "", "", fmt.Errorf("%w: owner/name required", ErrInvalid)
	}

	return s, "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]), p, nil
}

func proposalPath(s Source, repo string) string {
	if s.Session.Kind == forgeauth.GitLab {
		return repo + "/merge_requests"
	}

	return repo + "/pulls"
}

func (c *Client) locate(r Ref) (Source, string, string, error) {
	if r.Number <= 0 {
		return Source{}, "", "", fmt.Errorf("%w: positive number required", ErrInvalid)
	}

	s, repo, path, err := c.repository(RepositoryRef{r.Source, r.Repository})
	if err != nil {
		return Source{}, "", "", err
	}

	return s, proposalPath(s, repo) + "/" + strconv.FormatInt(r.Number, 10), path, nil
}

// List reads every page for the required repository and applies all filters.
// Closed excludes merged, including on APIs that group both states together.
func (c *Client) List(ctx context.Context, o ListOptions) ([]ChangeRequest, error) {
	path, ok := forgerest.CleanPath(o.Repository)
	if !ok {
		return nil, fmt.Errorf("%w: repository required", ErrInvalid)
	}
	if o.State == "" {
		o.State = string(Open)
	}
	if !listState(o.State) {
		return nil, ErrInvalid
	}

	sources, err := c.listSources(o.Source)
	if err != nil {
		return nil, err
	}

	var out []ChangeRequest
	found := false
	for _, src := range sources {
		if src.Session.Kind != forgeauth.GitLab && strings.Count(path, "/") != 1 {
			continue
		}

		items, err := c.listSource(ctx, src, path, o.State)
		if o.Source == "" && errors.Is(err, forgehttp.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}

		found = true
		for _, i := range items {
			if o.matches(i) {
				out = append(out, i)
			}
		}
	}

	if !found && len(sources) > 0 {
		return nil, forgehttp.ErrNotFound
	}

	return out, nil
}

// listState reports whether List accepts state.
func listState(state string) bool {
	switch State(state) {
	case "all", Open, Closed, Merged:
		return true
	}

	return false
}

// listSources returns the sources List reads: all of them, or the one
// named.
func (c *Client) listSources(id string) ([]Source, error) {
	if id == "" {
		return c.sources, nil
	}

	s, ok := c.byID[id]
	if !ok {
		return nil, ErrUnknownSource
	}

	return []Source{s}, nil
}

// listSource reads every proposal of path on src in state.
func (c *Client) listSource(ctx context.Context, src Source, path, state string) ([]ChangeRequest, error) {
	s, repo, _, err := c.repository(RepositoryRef{src.ID, path})
	if err != nil {
		return nil, err
	}

	endpoint := proposalPath(s, repo) + "?" + listQuery(s.Session.Kind, state).Encode()
	items, err := forgerest.Pages(ctx, s.Session, endpoint, func(raw json.RawMessage) (ChangeRequest, error) {
		v, err := decode(s, path, raw)
		return v.change, err
	})
	if err != nil {
		return nil, fmt.Errorf("changes: source %s: %w", s.ID, err)
	}

	return items, nil
}

// listQuery is the query that lists proposals in state on a forge of kind.
func listQuery(kind forgeauth.Kind, state string) url.Values {
	q := url.Values{"per_page": {"100"}, "limit": {"100"}, "state": {state}}
	if kind == forgeauth.GitLab {
		if state == string(Open) {
			q.Set("state", "opened")
		}

		q.Set("scope", "all")
	} else if state == string(Merged) {
		q.Set("state", string(Closed))
	}

	return q
}

// matches reports whether a listed proposal passes the filters of o.
func (o ListOptions) matches(c ChangeRequest) bool {
	if o.State != "all" && string(c.State) != o.State {
		return false
	}
	if o.Author != "" && !strings.EqualFold(o.Author, c.Author.Login) {
		return false
	}

	return o.Draft == nil || *o.Draft == c.Draft
}

// Get reads a proposal on the required source.
func (c *Client) Get(ctx context.Context, r Ref) (ChangeRequest, error) {
	v, err := c.read(ctx, r)
	return v.change, err
}

func (c *Client) read(ctx context.Context, r Ref) (proposal, error) {
	s, api, path, err := c.locate(r)
	if err != nil {
		return proposal{}, err
	}

	var raw json.RawMessage
	if err = forgerest.Send(ctx, s.Session, "GET", api, nil, &raw); err != nil {
		return proposal{}, err
	}

	return decode(s, path, raw)
}

// Create opens a proposal between branches of the target repository.
func (c *Client) Create(ctx context.Context, r RepositoryRef, n NewChange) (ChangeRequest, error) {
	if strings.TrimSpace(n.Title) == "" || !branch(n.SourceBranch) || !branch(n.TargetBranch) {
		return ChangeRequest{}, ErrInvalid
	}

	s, repo, path, err := c.repository(r)
	if err != nil {
		return ChangeRequest{}, err
	}

	b := map[string]any{
		"title": n.Title,
		"body":  n.Description,
		"head":  n.SourceBranch,
		"base":  n.TargetBranch,
	}
	switch s.Session.Kind {
	case forgeauth.GitHub:
		b["draft"] = n.Draft
	case forgeauth.GitLab:
		b = map[string]any{
			"title":         draftTitle(n.Title, n.Draft),
			"description":   n.Description,
			"source_branch": n.SourceBranch,
			"target_branch": n.TargetBranch,
		}
	case forgeauth.Forgejo:
		b["title"] = draftTitle(n.Title, n.Draft)
	}

	return sendProposal(ctx, s, path, "POST", proposalPath(s, repo), b)
}

// Update changes metadata, closes or reopens a proposal. Unsupported source
// branch changes are rejected before any write. Merged proposals stay merged.
func (c *Client) Update(ctx context.Context, r Ref, u Update) (ChangeRequest, error) {
	if u == (Update{}) {
		return ChangeRequest{}, ErrInvalid
	}
	if u.Title != nil && strings.TrimSpace(*u.Title) == "" {
		return ChangeRequest{}, ErrInvalid
	}
	if u.TargetBranch != nil && !branch(*u.TargetBranch) {
		return ChangeRequest{}, ErrInvalid
	}
	if u.State != nil && *u.State != Open && *u.State != Closed {
		return ChangeRequest{}, ErrInvalid
	}

	s, api, path, err := c.locate(r)
	if err != nil {
		return ChangeRequest{}, err
	}
	if u.SourceBranch != nil {
		return ChangeRequest{}, fmt.Errorf("%w: changing source branch", ErrUnsupported)
	}
	if u.State != nil {
		cur, err := c.Get(ctx, r)
		if err != nil {
			return ChangeRequest{}, err
		}
		if cur.State == Merged {
			return ChangeRequest{}, fmt.Errorf("%w: merged proposal", forgehttp.ErrConflict)
		}
	}

	b := map[string]any{}
	if u.Title != nil {
		b["title"] = *u.Title
	}

	method, desc, target, state := "PATCH", "body", "base", "state"
	if s.Session.Kind == forgeauth.GitLab {
		method, desc, target, state = "PUT", "description", "target_branch", "state_event"
	}
	if u.Description != nil {
		b[desc] = *u.Description
	}
	if u.TargetBranch != nil {
		b[target] = *u.TargetBranch
	}
	if u.State != nil {
		v := string(*u.State)
		if s.Session.Kind == forgeauth.GitLab {
			if *u.State == Open {
				v = "reopen"
			} else {
				v = "close"
			}
		}

		b[state] = v
	}

	return sendProposal(ctx, s, path, method, api, b)
}

func sendProposal(ctx context.Context, s Source, path, method, api string, body any) (ChangeRequest, error) {
	var raw json.RawMessage
	if err := forgerest.Send(ctx, s.Session, method, api, body, &raw); err != nil {
		return ChangeRequest{}, err
	}

	v, err := decode(s, path, raw)

	return v.change, err
}

func branch(v string) bool {
	return strings.TrimSpace(v) != "" && !strings.ContainsAny(v, "\x00\r\n:")
}
