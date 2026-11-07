package management

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

// newReactor returns an in-memory reactor holding events from two sources.
func newReactor(t *testing.T) *reactor.Reactor {
	t.Helper()
	r, err := reactor.New(reactor.Config{Store: &reactor.JSONStore{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSubscription(context.Background(), reactor.Subscription{ID: "all"}); err != nil {
		t.Fatal(err)
	}

	for i, src := range []string{"gh", "gh", "fj"} {
		e := reactor.Event{
			Type: reactor.TypeBranchUpdated, Origin: reactor.OriginWebhook, Source: src, Forge: forgeauth.GitHub,
			Repository: "team/app", Subject: "refs/heads/main", Data: json.RawMessage(`{"branch":"main"}`),
		}
		if _, _, err := r.Accept(context.Background(), reactor.Input{Event: e, Key: src + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
	}

	return r
}

func TestEventsUnconfigured(t *testing.T) {
	ctx := context.Background()
	c := New(Config{})
	if page, err := c.ListEvents(ctx, EventQuery{}); err != nil || page.Configured || page.Events == nil || c.EventsConfigured() {
		t.Fatalf("list = %+v, %v", page, err)
	}
	if _, _, err := c.GetEvent(ctx, "x"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("get = %v", err)
	}
}

func TestListEvents(t *testing.T) {
	ctx := context.Background()
	c := New(Config{Events: newReactor(t)})

	all, err := c.ListEvents(ctx, EventQuery{})
	if err != nil || !all.Configured || len(all.Events) != 3 {
		t.Fatalf("all = %+v, %v", all, err)
	}
	gh, err := c.ListEvents(ctx, EventQuery{Source: " gh ", Repository: "Team/App"})
	if err != nil || len(gh.Events) != 2 {
		t.Fatalf("gh = %+v, %v", gh, err)
	}
	if none, err := c.ListEvents(ctx, EventQuery{Type: "forge.pull_request.opened"}); err != nil || len(none.Events) != 0 {
		t.Fatalf("none = %+v, %v", none, err)
	}
	if _, err := c.ListEvents(ctx, EventQuery{Type: "Not A Type"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad type = %v", err)
	}
	if _, err := c.ListEvents(ctx, EventQuery{PageToken: "garbage"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad token = %v", err)
	}

	latest, ok, err := c.LatestEvent(ctx, "fj")
	if err != nil || !ok || latest.Source != "fj" {
		t.Fatalf("latest = %+v, %v, %v", latest, ok, err)
	}
	if _, ok, err := c.LatestEvent(ctx, "missing"); err != nil || ok {
		t.Fatalf("latest missing = %v, %v", ok, err)
	}
}

func TestGetEvent(t *testing.T) {
	ctx := context.Background()
	c := New(Config{Events: newReactor(t)})
	page, _ := c.ListEvents(ctx, EventQuery{})

	e, deliveries, err := c.GetEvent(ctx, page.Events[0].ID)
	if err != nil || e.ID != page.Events[0].ID || len(deliveries) != 1 || deliveries[0].Subscription != "all" {
		t.Fatalf("get = %+v, %+v, %v", e, deliveries, err)
	}
	if _, _, err := c.GetEvent(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	if _, _, err := c.GetEvent(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty = %v", err)
	}
}

// lyingEvents returns events that do not match the query.
type lyingEvents struct{}

func (lyingEvents) Events(context.Context, reactor.EventQuery) ([]reactor.Event, string, error) {
	return []reactor.Event{{ID: "e", Source: "other"}}, "", nil
}

func (lyingEvents) Event(context.Context, string) (reactor.Event, []reactor.Delivery, error) {
	return reactor.Event{ID: "other"}, nil, nil
}

func TestEventsRejectMismatches(t *testing.T) {
	ctx := context.Background()
	c := New(Config{Events: lyingEvents{}})
	if _, err := c.ListEvents(ctx, EventQuery{Source: "gh"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("list = %v", err)
	}
	if _, _, err := c.GetEvent(ctx, "e"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("get = %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.ListEvents(canceled, EventQuery{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled = %v", err)
	}
}
