package api

import (
	"context"
	"errors"
	"slices"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	"github.com/r59q/forge-tools/forge-reactor/library/sources"
	"github.com/r59q/forge-tools/forge-reactor/library/webhook"
	pb "github.com/r59q/forge-tools/forge-reactor/service/reactorpb"
)

// Source is a configured webhook source. Its secret is read for every
// delivery, so rotation needs no restart.
type Source struct {
	// Source configures verification; its Secret field is ignored.
	webhook.Source
	// Secret supplies the webhook secret.
	Secret forgeauth.Credentials
}

// errSecret means a configured secret could not be read.
var errSecret = errors.New("api: cannot read webhook secret")

// Sources are every webhook source: configured ones, which the API cannot
// change, and managed ones.
type Sources struct {
	configured map[string]Source
	managed    *sources.Manager
}

// NewSources checks configured and reserves their IDs in managed. Configured
// IDs must be unique and must not be used by a stored managed source.
func NewSources(ctx context.Context, configured []Source, managed *sources.Manager) (*Sources, error) {
	if managed == nil {
		return nil, ErrInvalid
	}

	s := &Sources{configured: map[string]Source{}, managed: managed}
	for _, c := range configured {
		if !validConfigured(c) || s.configured[c.ID].ID != "" {
			return nil, ErrInvalid
		}
		s.configured[c.ID] = c
	}

	stored, err := managed.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range stored {
		if _, taken := s.configured[m.ID]; taken {
			return nil, ErrInvalid
		}
	}
	managed.Reserved = s.ids()

	return s, nil
}

// validConfigured checks one configured source.
func validConfigured(c Source) bool {
	forge := c.Forge == forgeauth.GitHub || c.Forge == forgeauth.Forgejo

	return forge && reactor.ValidID(c.ID) && c.Secret != nil
}

// ids returns the configured IDs in order.
func (s *Sources) ids() []string {
	ids := make([]string, 0, len(s.configured))
	for id := range s.configured {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	return ids
}

// lookup returns a source with its secret. An unknown source is
// reactor.ErrNotFound; an unreadable secret is another error.
func (s *Sources) lookup(ctx context.Context, id string) (webhook.Source, error) {
	c, ok := s.configured[id]
	if !ok {
		m, err := s.managed.Get(ctx, id)
		if err != nil {
			return webhook.Source{}, err
		}

		return m.Webhook(), nil
	}

	secret, err := c.Secret.Token(ctx)
	if err != nil || secret == "" {
		return webhook.Source{}, errSecret
	}
	out := c.Source
	out.Secret = []byte(secret)

	return out, nil
}

// list returns configured sources, then managed ones, each by ID.
func (s *Sources) list(ctx context.Context) ([]*pb.WebhookSource, error) {
	managed, err := s.managed.List(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*pb.WebhookSource, 0, len(s.configured)+len(managed))
	for _, id := range s.ids() {
		c := s.configured[id]
		out = append(out, &pb.WebhookSource{
			Id:           c.ID,
			Forge:        string(c.Forge),
			Connection:   c.Connection,
			Repositories: c.Repositories,
			Url:          s.managed.URL(c.ID),
		})
	}
	for _, m := range managed {
		out = append(out, s.toManaged(m))
	}

	return out, nil
}

// toManaged describes a managed source without its secret.
func (s *Sources) toManaged(m sources.Source) *pb.WebhookSource {
	return &pb.WebhookSource{
		Id:           m.ID,
		Forge:        string(m.Forge),
		Connection:   m.Connection,
		Repositories: m.Repositories,
		Managed:      true,
		Registered:   m.Registered(),
		Url:          s.managed.URL(m.ID),
		CreatedAt:    toTime(m.CreatedAt),
	}
}
