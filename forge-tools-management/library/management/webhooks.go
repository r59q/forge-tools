package management

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Webhook errors. They never include upstream details.
var (
	// ErrExists means a webhook source with that ID exists.
	ErrExists = errors.New("management: webhook source exists")
	// ErrRegistration means forge-reactor could not register the webhook on
	// the forge, or has no public URL or connector to do so.
	ErrRegistration = errors.New("management: webhook registration failed")
)

// WebhookSource is a forge-reactor webhook source, without its secret.
type WebhookSource struct {
	ID           string
	Forge        forgeauth.Kind
	Connection   string
	Repositories []string
	// Managed is false for sources from forge-reactor's configuration file.
	Managed bool
	// Registered reports webhooks forge-reactor created on the forge.
	Registered bool
	// URL is the payload URL forges deliver to, if known.
	URL       string
	CreatedAt time.Time
}

// NewWebhook describes a webhook source to add. Forge may be left empty
// when Connection names a connection; it then comes from the connection.
type NewWebhook struct {
	ID           string
	Forge        forgeauth.Kind
	Connection   string
	Repositories []string
	// Register creates the webhook on every repository.
	Register bool
}

// WebhookSecret is a source with its secret, shown once after a change.
type WebhookSecret struct {
	Source WebhookSource
	Secret string
	// Failed are repositories whose forge webhook was not changed.
	Failed []string
}

// WebhookManager manages forge-reactor webhook sources. Implementations
// return ErrInvalid, ErrNotFound, ErrExists, ErrRegistration or another
// error for an unavailable service.
type WebhookManager interface {
	ListWebhooks(context.Context) ([]WebhookSource, error)
	CreateWebhook(context.Context, NewWebhook) (WebhookSecret, error)
	RotateWebhook(ctx context.Context, id string) (WebhookSecret, error)
	DeleteWebhook(ctx context.Context, id string) (failed []string, err error)
}

// Webhooks distinguishes a missing capability from an empty list.
type Webhooks struct {
	Configured bool
	Sources    []WebhookSource
}

// WebhooksConfigured reports whether webhook management is configured.
func (c *Client) WebhooksConfigured() bool { return c.webhooks != nil }

// ListWebhooks returns every webhook source.
func (c *Client) ListWebhooks(ctx context.Context) (Webhooks, error) {
	if err := ctx.Err(); err != nil {
		return Webhooks{}, err
	}
	if c.webhooks == nil {
		return Webhooks{Sources: []WebhookSource{}}, nil
	}

	list, err := c.webhooks.ListWebhooks(ctx)
	if err != nil {
		return Webhooks{}, webhookError(ctx, err)
	}
	if slices.ContainsFunc(list, func(s WebhookSource) bool { return s.ID == "" }) {
		return Webhooks{}, ErrUnavailable
	}

	return Webhooks{Configured: true, Sources: list}, nil
}

// AddWebhook creates a webhook source and returns its secret. A named
// connection must exist when a connector is configured, and supplies the
// forge.
func (c *Client) AddWebhook(ctx context.Context, w NewWebhook) (WebhookSecret, error) {
	if c.webhooks == nil {
		return WebhookSecret{}, ErrNotConfigured
	}
	w, err := c.webhookForge(ctx, w)
	if err != nil {
		return WebhookSecret{}, err
	}

	created, err := c.webhooks.CreateWebhook(ctx, w)
	if err != nil {
		return WebhookSecret{}, webhookError(ctx, err)
	}

	return checkSecret(created)
}

// webhookForge fills in and checks the forge of w's connection.
func (c *Client) webhookForge(ctx context.Context, w NewWebhook) (NewWebhook, error) {
	if w.Connection == "" || c.connector == nil {
		return w, nil
	}

	list, err := c.ListConnections(ctx)
	if err != nil {
		return NewWebhook{}, err
	}
	i := slices.IndexFunc(list.Connections, func(conn Connection) bool { return conn.ID == w.Connection })
	switch {
	case i < 0:
		return NewWebhook{}, ErrNotFound
	case w.Forge != "" && w.Forge != list.Connections[i].Kind:
		return NewWebhook{}, ErrInvalid
	}
	w.Forge = list.Connections[i].Kind

	return w, nil
}

// RotateWebhook replaces a managed source's secret and returns it.
func (c *Client) RotateWebhook(ctx context.Context, id string) (WebhookSecret, error) {
	if c.webhooks == nil {
		return WebhookSecret{}, ErrNotConfigured
	}

	rotated, err := c.webhooks.RotateWebhook(ctx, id)
	if err != nil {
		return WebhookSecret{}, webhookError(ctx, err)
	}

	return checkSecret(rotated)
}

// RemoveWebhook deletes a managed source and its registered webhooks. It
// returns the repositories whose forge webhook was left behind.
func (c *Client) RemoveWebhook(ctx context.Context, id string) ([]string, error) {
	if c.webhooks == nil {
		return nil, ErrNotConfigured
	}

	failed, err := c.webhooks.DeleteWebhook(ctx, id)
	if err != nil {
		return nil, webhookError(ctx, err)
	}

	return failed, nil
}

// checkSecret rejects a change response without a source or secret.
func checkSecret(s WebhookSecret) (WebhookSecret, error) {
	if s.Source.ID == "" || s.Secret == "" {
		return WebhookSecret{}, ErrUnavailable
	}

	return s, nil
}

// webhookError keeps known errors and hides the rest.
func webhookError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	for _, known := range []error{ErrInvalid, ErrNotFound, ErrExists, ErrRegistration} {
		if errors.Is(err, known) {
			return known
		}
	}

	return ErrUnavailable
}
