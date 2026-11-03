package code

import (
	"context"
	"fmt"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Read defaults and caps.
const (
	defaultRefLimit   = 10000
	defaultPageSize   = 50
	maxPageSize       = 100
	defaultMaxEntries = 100000
)

// Client reads and writes repository content on one or more sources. It
// is safe for concurrent use. Each call has its own call budget and memo;
// nothing is cached between calls.
type Client struct {
	limits  Limits
	sources []*source
	byID    map[string]*source
}

// source is a Source with its forge adapter.
type source struct {
	Source
	forge backend
}

// New returns a Client for the given sources with the default Limits.
// Each source needs a unique, non-empty ID and an open session of a
// supported kind.
func New(sources ...Source) (*Client, error) {
	return NewWithLimits(Limits{}, sources...)
}

// NewWithLimits is like New with custom limits. Zero fields of l take the
// defaults; negative ones are ErrInvalid.
func NewWithLimits(l Limits, sources ...Source) (*Client, error) {
	return newClient(l, func(k forgeauth.Kind) (backend, bool) {
		switch k {
		case forgeauth.GitHub:
			return githubBackend{}, true
		case forgeauth.GitLab:
			return gitlabBackend{}, true
		case forgeauth.Forgejo:
			return forgejoBackend{}, true
		}

		return nil, false
	}, sources)
}

// newClient builds a Client, picking each source's adapter with lookup.
func newClient(l Limits, lookup func(forgeauth.Kind) (backend, bool), sources []Source) (*Client, error) {
	l, err := l.withDefaults()
	if err != nil {
		return nil, err
	}

	c := &Client{limits: l, byID: map[string]*source{}}
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

		b, ok := lookup(s.Session.Kind)
		if !ok {
			return nil, fmt.Errorf("%w: source %q has unsupported kind %q", ErrInvalid, s.ID, s.Session.Kind)
		}

		src := &source{Source: s, forge: b}
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

// Capabilities reports what the source supports.
func (c *Client) Capabilities(source string) (Capabilities, error) {
	s, err := c.one(source)
	if err != nil {
		return Capabilities{}, err
	}

	caps := s.forge.capabilities()
	caps.Writes = s.Git != nil

	return caps, nil
}

// start checks r and returns its source and a new op.
func (c *Client) start(ctx context.Context, r RepositoryRef) (*source, *op, error) {
	repo, err := cleanRepo(r.Repository)
	if err != nil {
		return nil, nil, err
	}

	s, err := c.one(r.Source)
	if err != nil {
		return nil, nil, err
	}

	return s, newOp(ctx, s.Session, repo, c.limits), nil
}

// one returns the source with the given ID.
func (c *Client) one(id string) (*source, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: source required", ErrInvalid)
	}

	s, ok := c.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSource, id)
	}

	return s, nil
}

// wrap prefixes err with the source ID.
func (s *source) wrap(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("code: source %s: %w", s.ID, err)
}

// cmp returns v, or def if v is zero.
func cmp(v, def int) int {
	if v == 0 {
		return def
	}

	return v
}
