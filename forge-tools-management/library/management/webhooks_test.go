package management

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// fakeWebhooks records the last request and answers with fixed results.
type fakeWebhooks struct {
	created NewWebhook
	list    []WebhookSource
	secret  string
	err     error
}

func (f *fakeWebhooks) ListWebhooks(context.Context) ([]WebhookSource, error) { return f.list, f.err }

func (f *fakeWebhooks) CreateWebhook(_ context.Context, w NewWebhook) (WebhookSecret, error) {
	f.created = w

	return WebhookSecret{Source: WebhookSource{ID: w.ID, Forge: w.Forge}, Secret: f.secret}, f.err
}

func (f *fakeWebhooks) RotateWebhook(_ context.Context, id string) (WebhookSecret, error) {
	return WebhookSecret{Source: WebhookSource{ID: id}, Secret: f.secret, Failed: []string{"a/b"}}, f.err
}

func (f *fakeWebhooks) DeleteWebhook(context.Context, string) ([]string, error) {
	return []string{"a/b"}, f.err
}

func TestWebhooksUnconfigured(t *testing.T) {
	ctx := context.Background()
	c := New(Config{})
	if got, err := c.ListWebhooks(ctx); err != nil || got.Configured || got.Sources == nil || c.WebhooksConfigured() {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if _, err := c.AddWebhook(ctx, NewWebhook{ID: "x"}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("add = %v", err)
	}
	if _, err := c.RotateWebhook(ctx, "x"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("rotate = %v", err)
	}
	if _, err := c.RemoveWebhook(ctx, "x"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("remove = %v", err)
	}
}

func TestWebhooksConfigured(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWebhooks{secret: "s3cret", list: []WebhookSource{{ID: "gh", Forge: forgeauth.GitHub}}}
	c := New(Config{Webhooks: fake})

	list, err := c.ListWebhooks(ctx)
	if err != nil || !list.Configured || len(list.Sources) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	created, err := c.AddWebhook(ctx, NewWebhook{ID: "app", Forge: forgeauth.Forgejo})
	if err != nil || created.Secret != "s3cret" || fake.created.Forge != forgeauth.Forgejo {
		t.Fatalf("add = %+v, %v", created, err)
	}
	rotated, err := c.RotateWebhook(ctx, "app")
	if err != nil || rotated.Secret != "s3cret" || len(rotated.Failed) != 1 {
		t.Fatalf("rotate = %+v, %v", rotated, err)
	}
	if failed, err := c.RemoveWebhook(ctx, "app"); err != nil || len(failed) != 1 {
		t.Fatalf("remove = %v, %v", failed, err)
	}

	fake.secret = ""
	if _, err := c.AddWebhook(ctx, NewWebhook{ID: "app"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no secret = %v", err)
	}
	fake.list = []WebhookSource{{}}
	if _, err := c.ListWebhooks(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no id = %v", err)
	}
}

func TestWebhookForgeFromConnection(t *testing.T) {
	ctx := context.Background()
	m, host, _ := connector(t)
	fake := &fakeWebhooks{secret: "s"}
	c := New(Config{Connector: m, Webhooks: fake})
	conn, err := c.AddConnection(ctx, NewConnection{Kind: forgeauth.GitLab, Host: host, Credential: goodToken})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.AddWebhook(ctx, NewWebhook{ID: "x", Connection: conn.ID}); err != nil || fake.created.Forge != forgeauth.GitLab {
		t.Fatalf("forge = %q, %v", fake.created.Forge, err)
	}
	if _, err := c.AddWebhook(ctx, NewWebhook{ID: "x", Connection: conn.ID, Forge: forgeauth.GitHub}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatch = %v", err)
	}
	if _, err := c.AddWebhook(ctx, NewWebhook{ID: "x", Connection: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
}

func TestWebhookErrors(t *testing.T) {
	ctx := context.Background()
	for _, known := range []error{ErrInvalid, ErrNotFound, ErrExists, ErrRegistration} {
		c := New(Config{Webhooks: &fakeWebhooks{err: known}})
		if _, err := c.RotateWebhook(ctx, "x"); !errors.Is(err, known) {
			t.Errorf("%v became %v", known, err)
		}
	}

	c := New(Config{Webhooks: &fakeWebhooks{err: errors.New("upstream-secret")}})
	if _, err := c.RemoveWebhook(ctx, "x"); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("upstream = %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.ListWebhooks(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled = %v", err)
	}
}
