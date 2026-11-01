package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// Client reads repositories from one or more sources. It is safe for
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

// List returns the repositories each source's account owns or is a member
// of, merged in source order.
func (c *Client) List(ctx context.Context, opts ListOptions) ([]Repository, error) {
	sources, err := c.pick(opts.Source)
	if err != nil {
		return nil, err
	}
	owner := strings.ToLower(strings.Trim(opts.Owner, "/"))
	results, err := each(sources, func(s *source) ([]Repository, error) {
		repos, err := forgerest.Pages(ctx, s.Session, s.forge.listPath(), s.repository)
		if err != nil || owner == "" {
			return repos, err
		}
		kept := repos[:0]
		for _, r := range repos {
			if o := strings.ToLower(r.Owner); o == owner || strings.HasPrefix(o, owner+"/") {
				kept = append(kept, r)
			}
		}
		return kept, nil
	})
	if err != nil {
		return nil, err
	}
	var out []Repository
	for _, repos := range results {
		out = append(out, repos...)
	}
	return out, nil
}

// Find returns the repository at path on every source that has it, in
// source order. If no source has it, the error wraps forgehttp.ErrNotFound.
func (c *Client) Find(ctx context.Context, path string) ([]Repository, error) {
	return c.find(ctx, Ref{Path: path})
}

// Get returns the repository ref names. If ref.Source is empty, the path
// must exist on exactly one source; otherwise the error wraps ErrAmbiguous.
// A missing repository gives an error wrapping forgehttp.ErrNotFound.
func (c *Client) Get(ctx context.Context, ref Ref) (Repository, error) {
	found, err := c.find(ctx, ref)
	if err != nil {
		return Repository{}, err
	}
	if len(found) > 1 {
		ids := make([]string, len(found))
		for i, r := range found {
			ids[i] = r.Source.ID
		}
		return Repository{}, fmt.Errorf("%w: %q on %s", ErrAmbiguous, ref.Path, strings.Join(ids, ", "))
	}
	return found[0], nil
}

// Branches returns the branches of the repository ref names, with the
// default branch marked.
func (c *Client) Branches(ctx context.Context, ref Ref) ([]Branch, error) {
	repo, err := c.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	s := c.byID[repo.Source.ID]
	api, _ := s.forge.repoPath(repo.Path)
	branches, err := forgerest.Pages(ctx, s.Session, s.forge.branchesPath(api), s.forge.branch)
	if err != nil {
		return nil, err
	}
	for i := range branches {
		branches[i].Default = branches[i].Name == repo.DefaultBranch
	}
	return branches, nil
}

// Tags returns the tags of the repository ref names.
func (c *Client) Tags(ctx context.Context, ref Ref) ([]Tag, error) {
	s, api, err := c.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	return forgerest.Pages(ctx, s.Session, s.forge.tagsPath(api), s.forge.tag)
}

// Update changes the metadata of the repository ref names and returns the
// updated repository. At least one field of u must be set.
func (c *Client) Update(ctx context.Context, ref Ref, u Update) (Repository, error) {
	if u.Description == nil && u.DefaultBranch == nil {
		return Repository{}, fmt.Errorf("%w: empty update", ErrInvalid)
	}
	if u.DefaultBranch != nil && strings.TrimSpace(*u.DefaultBranch) == "" {
		return Repository{}, fmt.Errorf("%w: empty default branch", ErrInvalid)
	}
	s, api, err := c.resolve(ctx, ref)
	if err != nil {
		return Repository{}, err
	}
	body := map[string]string{}
	if u.Description != nil {
		body["description"] = *u.Description
	}
	if u.DefaultBranch != nil {
		body["default_branch"] = *u.DefaultBranch
	}
	var raw json.RawMessage
	if err := forgerest.Send(ctx, s.Session, s.forge.updateMethod(), api, body, &raw); err != nil {
		return Repository{}, err
	}
	return s.repository(raw)
}

// resolve returns the source and API path for ref. Without a source it
// finds the one source that has the path.
func (c *Client) resolve(ctx context.Context, ref Ref) (*source, string, error) {
	if ref.Source == "" {
		repo, err := c.Get(ctx, ref)
		if err != nil {
			return nil, "", err
		}
		ref = repo.Ref()
	}
	path, ok := forgerest.CleanPath(ref.Path)
	if !ok {
		return nil, "", invalidPath(ref.Path)
	}
	s, ok := c.byID[ref.Source]
	if !ok {
		return nil, "", fmt.Errorf("%w: %q", ErrUnknownSource, ref.Source)
	}
	api, ok := s.forge.repoPath(path)
	if !ok {
		return nil, "", notFound(s, path)
	}
	return s, api, nil
}

// find returns ref's repository on each source that has it.
func (c *Client) find(ctx context.Context, ref Ref) ([]Repository, error) {
	path, ok := forgerest.CleanPath(ref.Path)
	if !ok {
		return nil, invalidPath(ref.Path)
	}
	sources, err := c.pick(ref.Source)
	if err != nil {
		return nil, err
	}
	results, err := each(sources, func(s *source) ([]Repository, error) {
		api, ok := s.forge.repoPath(path)
		if !ok {
			return nil, nil // this forge cannot hold such a path
		}
		var raw json.RawMessage
		if err := forgerest.Send(ctx, s.Session, "GET", api, nil, &raw); err != nil {
			if errors.Is(err, forgehttp.ErrNotFound) && ref.Source == "" {
				return nil, nil
			}
			return nil, err
		}
		r, err := s.repository(raw)
		if err != nil {
			return nil, err
		}
		return []Repository{r}, nil
	})
	if err != nil {
		return nil, err
	}
	var found []Repository
	for _, rs := range results {
		found = append(found, rs...)
	}
	if len(found) == 0 {
		if len(sources) == 1 {
			return nil, notFound(sources[0], path)
		}
		return nil, fmt.Errorf("repositories: %q: %w", path, forgehttp.ErrNotFound)
	}
	return found, nil
}

// pick returns the source with the given ID, or all sources if id is empty.
func (c *Client) pick(id string) ([]*source, error) {
	if id == "" {
		return c.sources, nil
	}
	s, ok := c.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSource, id)
	}
	return []*source{s}, nil
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
				errs[i] = fmt.Errorf("repositories: source %s: %w", s.ID, errs[i])
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return results, nil
}

// repository decodes one repository and stamps it with s.
func (s *source) repository(raw json.RawMessage) (Repository, error) {
	r, err := s.forge.repository(raw)
	if err != nil {
		return Repository{}, err
	}
	r.Source = SourceInfo{ID: s.ID, Kind: s.Session.Kind}
	if u, err := url.Parse(r.WebURL); err == nil {
		r.Source.Host = u.Host
	}
	if i := strings.LastIndexByte(r.Path, '/'); i > 0 {
		r.Owner, r.Name = r.Path[:i], r.Path[i+1:]
	}
	return r, nil
}

// notFound is the error for a path that cannot exist on s.
func notFound(s *source, path string) error {
	return fmt.Errorf("repositories: source %s: %q: %w", s.ID, path, forgehttp.ErrNotFound)
}

// invalidPath is the error for a path that forgerest.CleanPath rejects.
func invalidPath(path string) error {
	return fmt.Errorf("%w: path %q, want owner/name", ErrInvalid, path)
}
