package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// Client reads and writes issues on one or more sources. It is safe for
// concurrent use. Calls that read several sources query them in parallel
// and return results in source order.
type Client struct {
	sources []*source
	byID    map[string]*source
}

// source is a Source with its forge adapter.
type source struct {
	Source
	forge forge
}

// New returns a Client for the given sources. Each needs a unique, non-empty
// ID and an open session of a supported kind. A Client without sources lists
// nothing and finds nothing.
func New(sources ...Source) (*Client, error) {
	c := &Client{byID: map[string]*source{}}
	for _, s := range sources {
		if strings.TrimSpace(s.ID) == "" {
			return nil, fmt.Errorf("%w: source with empty ID", ErrInvalid)
		}
		if _, dup := c.byID[s.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate source %q", ErrInvalid, s.ID)
		}
		if s.Session == nil || s.Session.Client == nil {
			return nil, fmt.Errorf("%w: source %q has no open session", ErrInvalid, s.ID)
		}
		f, ok := forges[s.Session.Kind]
		if !ok {
			return nil, fmt.Errorf("%w: source %q has unsupported kind %q", ErrInvalid, s.ID, s.Session.Kind)
		}
		src := &source{Source: s, forge: f}
		c.sources = append(c.sources, src)
		c.byID[s.ID] = src
	}
	return c, nil
}

// Sources returns the configured sources in order.
func (c *Client) Sources() []Source {
	out := make([]Source, len(c.sources))
	for i, s := range c.sources {
		out[i] = s.Source
	}
	return out
}

// List returns issues, merged in source order. With opts.Repository set it
// lists that repository on opts.Source, or on every source that has it;
// if none has it, the error wraps forgehttp.ErrNotFound. A 410 Gone, as
// GitHub sends when issues are disabled, counts as not found. Without it,
// each source lists its account's issues selected by opts.Scope.
func (c *Client) List(ctx context.Context, opts ListOptions) ([]Issue, error) {
	f, err := checkList(opts)
	if err != nil {
		return nil, err
	}
	sources, err := c.pick(opts.Source)
	if err != nil {
		return nil, err
	}
	path := ""
	if opts.Repository != "" {
		path, _ = forgerest.CleanPath(opts.Repository)
	}
	type result struct {
		issues []Issue
		found  bool
	}
	results, err := each(sources, func(s *source) (result, error) {
		first := s.forge.accountPath(f, opts.Scope)
		if path != "" {
			repo, ok := s.forge.repoPath(path)
			if !ok {
				return result{}, nil // this forge cannot hold such a path
			}
			first = s.forge.listPath(repo, f)
		}
		list, err := s.issues(ctx, first, path)
		if err = gone(err); err != nil {
			if path != "" && opts.Source == "" && errors.Is(err, forgehttp.ErrNotFound) {
				return result{}, nil
			}
			return result{}, err
		}
		return result{issues: withLabels(list, f.labels), found: true}, nil
	})
	if err != nil {
		return nil, err
	}
	var out []Issue
	found := false
	for _, r := range results {
		out = append(out, r.issues...)
		found = found || r.found
	}
	if path != "" && !found {
		if len(sources) == 1 {
			return nil, notFound(sources[0], path)
		}
		return nil, fmt.Errorf("issues: %q: %w", path, forgehttp.ErrNotFound)
	}
	return out, nil
}

// Get returns the issue ref names. If ref.Source is empty, the issue must
// exist on exactly one source; otherwise the error wraps ErrAmbiguous. A
// missing issue, or a number that is a pull or merge request, gives an
// error wrapping forgehttp.ErrNotFound, as does a deleted GitHub issue
// (410 Gone).
func (c *Client) Get(ctx context.Context, ref Ref) (Issue, error) {
	_, iss, _, err := c.get(ctx, ref)
	return iss, err
}

// Create opens an issue in the repository ref names and returns it.
// ref.Source and n.Title are required.
func (c *Client) Create(ctx context.Context, ref RepositoryRef, n NewIssue) (Issue, error) {
	if strings.TrimSpace(n.Title) == "" {
		return Issue{}, fmt.Errorf("%w: empty title", ErrInvalid)
	}
	if err := checkNames("label", n.Labels); err != nil {
		return Issue{}, err
	}
	if err := checkNames("assignee", n.Assignees); err != nil {
		return Issue{}, err
	}
	if ref.Source == "" {
		return Issue{}, fmt.Errorf("%w: source required", ErrInvalid)
	}
	path, ok := forgerest.CleanPath(ref.Repository)
	if !ok {
		return Issue{}, invalidPath(ref.Repository)
	}
	s, err := c.one(ref.Source)
	if err != nil {
		return Issue{}, err
	}
	repo, ok := s.forge.repoPath(path)
	if !ok {
		return Issue{}, notFound(s, path)
	}
	iss, err := s.forge.create(ctx, s.Session, repo, n)
	if err != nil {
		return Issue{}, fmt.Errorf("issues: source %s: %w", s.ID, err)
	}
	return s.stamp(iss, path), nil
}

// Update changes the issue ref names and returns it. ref.Source is
// required, and at least one field of u must be set.
func (c *Client) Update(ctx context.Context, ref Ref, u Update) (Issue, error) {
	if u == (Update{}) {
		return Issue{}, fmt.Errorf("%w: empty update", ErrInvalid)
	}
	if u.Title != nil && strings.TrimSpace(*u.Title) == "" {
		return Issue{}, fmt.Errorf("%w: empty title", ErrInvalid)
	}
	if u.State != nil && *u.State != Open && *u.State != Closed {
		return Issue{}, fmt.Errorf("%w: state %q, want open or closed", ErrInvalid, *u.State)
	}
	if u.Labels != nil {
		if err := checkNames("label", *u.Labels); err != nil {
			return Issue{}, err
		}
	}
	if u.Assignees != nil {
		if err := checkNames("assignee", *u.Assignees); err != nil {
			return Issue{}, err
		}
	}
	if ref.Source == "" {
		return Issue{}, fmt.Errorf("%w: source required", ErrInvalid)
	}
	// Read the issue first, so a pull request is never changed.
	s, cur, api, err := c.get(ctx, ref)
	if err != nil {
		return Issue{}, err
	}
	iss, err := s.forge.update(ctx, s.Session, api, u)
	if err != nil {
		return Issue{}, fmt.Errorf("issues: source %s: %w", s.ID, err)
	}
	return s.stamp(iss, cur.Repository), nil
}

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

// get returns ref's issue, the source it is on and its API path. The path
// is built from where the forge says the issue is, so a write after a
// transfer or rename goes to the issue's new place.
func (c *Client) get(ctx context.Context, ref Ref) (*source, Issue, string, error) {
	path, ok := forgerest.CleanPath(ref.Repository)
	if !ok {
		return nil, Issue{}, "", invalidPath(ref.Repository)
	}
	if ref.Number <= 0 {
		return nil, Issue{}, "", fmt.Errorf("%w: issue number %d", ErrInvalid, ref.Number)
	}
	sources, err := c.pick(ref.Source)
	if err != nil {
		return nil, Issue{}, "", err
	}
	results, err := each(sources, func(s *source) ([]Issue, error) {
		repo, ok := s.forge.repoPath(path)
		if !ok {
			return nil, nil // this forge cannot hold such a path
		}
		var raw json.RawMessage
		err := gone(forgerest.Send(ctx, s.Session, "GET", s.forge.issuePath(repo, ref.Number), nil, &raw))
		if errors.Is(err, forgehttp.ErrNotFound) && ref.Source == "" {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		iss, isIssue, err := s.forge.issue(raw)
		if err != nil || !isIssue {
			return nil, err
		}
		return []Issue{s.stamp(iss, path)}, nil
	})
	if err != nil {
		return nil, Issue{}, "", err
	}
	var found []Issue
	for _, r := range results {
		found = append(found, r...)
	}
	switch {
	case len(found) == 1:
		s, iss := c.byID[found[0].Source.ID], found[0]
		return s, iss, s.apiPath(iss, path, ref.Number), nil
	case len(found) > 1:
		ids := make([]string, len(found))
		for i, iss := range found {
			ids[i] = iss.Source.ID
		}
		return nil, Issue{}, "", fmt.Errorf("%w: %s#%d on %s", ErrAmbiguous, path, ref.Number, strings.Join(ids, ", "))
	case len(sources) == 1:
		return nil, Issue{}, "", fmt.Errorf("issues: source %s: %s#%d: %w", sources[0].ID, path, ref.Number, forgehttp.ErrNotFound)
	}
	return nil, Issue{}, "", fmt.Errorf("issues: %s#%d: %w", path, ref.Number, forgehttp.ErrNotFound)
}

// pick returns the source with the given ID, or all sources if id is empty.
func (c *Client) pick(id string) ([]*source, error) {
	if id == "" {
		return c.sources, nil
	}
	s, err := c.one(id)
	if err != nil {
		return nil, err
	}
	return []*source{s}, nil
}

// one returns the source with the given ID.
func (c *Client) one(id string) (*source, error) {
	s, ok := c.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSource, id)
	}
	return s, nil
}

// each calls fn for every source in parallel and returns the results in
// source order. Errors are joined, each prefixed with its source ID.
func each[T any](sources []*source, fn func(*source) (T, error)) ([]T, error) {
	results := make([]T, len(sources))
	errs := make([]error, len(sources))
	var wg sync.WaitGroup
	for i, s := range sources {
		wg.Go(func() {
			results[i], errs[i] = fn(s)
			if errs[i] != nil {
				errs[i] = fmt.Errorf("issues: source %s: %w", s.ID, errs[i])
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return results, nil
}

// issues reads every page of issues from first, dropping pull requests.
// path is the listed repository, or empty for an account-wide listing.
func (s *source) issues(ctx context.Context, first, path string) ([]Issue, error) {
	type entry struct {
		issue Issue
		keep  bool
	}
	entries, err := forgerest.Pages(ctx, s.Session, first, func(raw json.RawMessage) (entry, error) {
		iss, keep, err := s.forge.issue(raw)
		return entry{iss, keep}, err
	})
	if err != nil {
		return nil, err
	}
	out := []Issue{}
	for _, e := range entries {
		if e.keep {
			out = append(out, s.stamp(e.issue, path))
		}
	}
	return out, nil
}

// apiPath is the API path of iss: its reported repository and number, or
// path and n if the forge reported none or one that is not valid.
func (s *source) apiPath(iss Issue, path string, n int64) string {
	repo, _ := s.forge.repoPath(path)
	if p, ok := forgerest.CleanPath(iss.Repository); ok {
		if r, ok := s.forge.repoPath(p); ok {
			repo = r
		}
	}
	if iss.Number > 0 {
		n = iss.Number
	}
	return s.forge.issuePath(repo, n)
}

// stamp sets the source of iss, and its repository to path if the forge
// did not report one.
func (s *source) stamp(iss Issue, path string) Issue {
	iss.Source = SourceInfo{ID: s.ID, Kind: s.Session.Kind}
	if u, err := url.Parse(iss.WebURL); err == nil {
		iss.Source.Host = u.Host
	}
	if iss.Repository == "" {
		iss.Repository = path
	}
	return iss
}

// withURL fills a comment's missing web URL from its issue, as GitLab
// notes have none.
func withURL(cm Comment, iss Issue) Comment {
	if cm.WebURL == "" && cm.ID != "" && iss.WebURL != "" {
		cm.WebURL = iss.WebURL + "#note_" + cm.ID
	}
	return cm
}

// withLabels keeps the issues that have every label, ignoring case. Forges
// filter too, but not always for all labels, such as Forgejo's search.
func withLabels(list []Issue, labels []string) []Issue {
	if len(labels) == 0 {
		return list
	}
	kept := list[:0]
	for _, iss := range list {
		if hasAll(iss.Labels, labels) {
			kept = append(kept, iss)
		}
	}
	return kept
}

// hasAll reports whether have contains every name in want, ignoring case.
func hasAll(have, want []string) bool {
	for _, w := range want {
		ok := false
		for _, h := range have {
			if strings.EqualFold(h, w) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// checkList checks opts and returns its forge filter.
func checkList(opts ListOptions) (filter, error) {
	f := filter{state: opts.State, labels: opts.Labels}
	switch f.state {
	case "":
		f.state = StateOpen
	case StateOpen, StateClosed, StateAll:
	default:
		return filter{}, fmt.Errorf("%w: state %q, want open, closed or all", ErrInvalid, opts.State)
	}
	switch opts.Scope {
	case "", Assigned, Created:
	default:
		return filter{}, fmt.Errorf("%w: scope %q, want assigned or created", ErrInvalid, opts.Scope)
	}
	if opts.Repository != "" {
		if opts.Scope != "" {
			return filter{}, fmt.Errorf("%w: scope applies only without a repository", ErrInvalid)
		}
		if _, ok := forgerest.CleanPath(opts.Repository); !ok {
			return filter{}, invalidPath(opts.Repository)
		}
	}
	return f, checkNames("label", opts.Labels)
}

// checkNames checks label names or logins. Forges join them with commas,
// so a comma would split one name in two.
func checkNames(what string, names []string) error {
	for _, n := range names {
		if strings.TrimSpace(n) == "" || strings.Contains(n, ",") {
			return fmt.Errorf("%w: %s %q", ErrInvalid, what, n)
		}
	}
	return nil
}

// gone wraps a 410 Gone, which GitHub sends for a deleted issue or when
// issues are disabled, with forgehttp.ErrNotFound.
func gone(err error) error {
	var fe *forgehttp.Error
	if errors.As(err, &fe) && fe.StatusCode == http.StatusGone {
		return fmt.Errorf("%w: %w", forgehttp.ErrNotFound, err)
	}
	return err
}

// notFound is the error for a repository path that cannot exist on s.
func notFound(s *source, path string) error {
	return fmt.Errorf("issues: source %s: %q: %w", s.ID, path, forgehttp.ErrNotFound)
}

// invalidPath is the error for a path that forgerest.CleanPath rejects.
func invalidPath(path string) error {
	return fmt.Errorf("%w: repository %q, want owner/name", ErrInvalid, path)
}
