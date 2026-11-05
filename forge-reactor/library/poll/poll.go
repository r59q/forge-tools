// Package poll watches configured Forgejo branches and reports new heads to
// a reactor. A poll sees observations, not pushes: several pushes between
// two polls appear as one change, and a push that is undone before the next
// poll is not seen at all.
package poll

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

// DefaultInterval is the poll interval when Poller.Interval is zero.
const DefaultInterval = time.Minute

// ErrUnsupported means a connection is not a Forgejo connection.
var ErrUnsupported = errors.New("poll: only Forgejo connections can be polled")

// Target is a set of branches in one repository.
type Target struct {
	// ID names the target; events carry it as their source.
	ID string
	// Connection is the forge-connector connection to read with.
	Connection string
	// Repository is the owner/name path.
	Repository string
	// Branches are the branch names to watch.
	Branches []string
}

// Sessions opens a forge session for a connection, such as through
// forge-connector, so the poller never holds forge credentials.
type Sessions func(ctx context.Context, connection string) (*forgeauth.Session, error)

// Poller reads branch heads and passes each reading to a reactor.
type Poller struct {
	// Reactor receives observations.
	Reactor *reactor.Reactor
	// Sessions opens sessions.
	Sessions Sessions
	// Targets are the branches to watch.
	Targets []Target
	// Interval is the wait between rounds; zero uses DefaultInterval.
	Interval time.Duration
	// OnError, if set, sees every failed read or observation.
	OnError func(target Target, branch string, err error)
}

// Validate checks the targets.
func (p *Poller) Validate() error {
	if p.Reactor == nil || p.Sessions == nil || p.Interval < 0 {
		return reactor.ErrInvalid
	}
	seen := map[string]bool{}
	for _, t := range p.Targets {
		owner, name, ok := strings.Cut(t.Repository, "/")
		if !reactor.ValidID(t.ID) || seen[t.ID] || t.Connection == "" || !ok || owner == "" || name == "" ||
			strings.Contains(name, "/") || len(t.Branches) == 0 {
			return fmt.Errorf("%w: poll target %q", reactor.ErrInvalid, t.ID)
		}
		seen[t.ID] = true
		for _, b := range t.Branches {
			if b == "" || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") || strings.Contains(b, "..") {
				return fmt.Errorf("%w: poll target %q branch %q", reactor.ErrInvalid, t.ID, b)
			}
		}
	}

	return nil
}

// Run polls at once and then every interval until ctx ends, and returns
// ctx's error. Failed reads are reported to OnError and retried next round.
func (p *Poller) Run(ctx context.Context) error {
	if err := p.Validate(); err != nil {
		return err
	}
	interval := p.Interval
	if interval == 0 {
		interval = DefaultInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		_ = p.Once(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Once reads every branch once and reports each reading. A failed read
// reports nothing, so it never counts as a change or moves a baseline.
func (p *Poller) Once(ctx context.Context) error {
	var errs []error
	for _, t := range p.Targets {
		session, err := p.Sessions(ctx, t.Connection)
		if err == nil && session.Kind != forgeauth.Forgejo {
			err = ErrUnsupported
		}
		for _, branch := range t.Branches {
			failed := err
			if failed == nil {
				failed = p.observe(ctx, session, t, branch)
			}
			if failed == nil {
				continue
			}
			errs = append(errs, fmt.Errorf("poll %s %s: %w", t.ID, branch, failed))
			if p.OnError != nil {
				p.OnError(t, branch, failed)
			}
		}
	}

	return errors.Join(errs...)
}

// observe reads one branch and reports the reading.
func (p *Poller) observe(ctx context.Context, s *forgeauth.Session, t Target, branch string) error {
	commit, err := Head(ctx, s, t.Repository, branch)
	if err != nil {
		return err
	}
	_, err = p.Reactor.Observe(ctx, reactor.Observation{
		Source:     t.ID,
		Forge:      forgeauth.Forgejo,
		Connection: t.Connection,
		Repository: t.Repository,
		Branch:     branch,
		Commit:     commit,
	})

	return err
}

// Head reads a Forgejo branch's head commit.
func Head(ctx context.Context, s *forgeauth.Session, repository, branch string) (string, error) {
	owner, name, _ := strings.Cut(repository, "/")
	parts := strings.Split(branch, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}

	var body struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/branches/" + strings.Join(parts, "/")
	if err := forgerest.Send(ctx, s, "GET", path, nil, &body); err != nil {
		return "", err
	}
	if body.Commit.ID == "" {
		return "", fmt.Errorf("poll: branch %q has no head commit", branch)
	}

	return body.Commit.ID, nil
}
