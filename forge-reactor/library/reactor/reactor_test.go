package reactor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestNewRejectsInvalidConfig(t *testing.T) {
	for _, c := range []Config{
		{},
		{Store: &JSONStore{}, Retention: -1},
		{Store: &JSONStore{}, MaxPending: -1},
		{Store: &JSONStore{}, Retry: Retry{Attempts: -1}},
	} {
		if _, err := New(c); !errors.Is(err, ErrInvalid) {
			t.Errorf("New(%+v) = %v, want ErrInvalid", c, err)
		}
	}
}

func TestAcceptFansOutToMatchingSubscriptions(t *testing.T) {
	r, _ := newReactor(t, Config{})
	subscribe(t, r, "all", Filter{})
	subscribe(t, r, "typed", Filter{Types: []string{TypeBranchUpdated}})
	subscribe(t, r, "repo", Filter{Repositories: []string{"owner/repo"}, Connections: []string{"gh"}})
	subscribe(t, r, "origin", Filter{Origins: []Origin{OriginWebhook}})
	subscribe(t, r, "pulls", Filter{Types: []string{TypePullRequestOpened}})
	subscribe(t, r, "other-conn", Filter{Connections: []string{"forgejo"}})
	subscribe(t, r, "polls", Filter{Origins: []Origin{OriginPoll}})
	subscribe(t, r, "source", Filter{Sources: []string{webhookEvent().Source}})
	subscribe(t, r, "other-source", Filter{Sources: []string{"elsewhere"}})

	e := accept(t, r, webhookEvent(), "k")
	if e.ID == "" || e.AcceptedAt.IsZero() {
		t.Fatalf("event = %+v", e)
	}

	var got []string
	for _, d := range deliveries(t, r, DeliveryQuery{EventID: e.ID}) {
		if d.Status != StatusPending || d.Replay {
			t.Errorf("delivery = %+v", d)
		}
		got = append(got, d.Subscription)
	}
	slices.Sort(got)
	if want := []string{"all", "origin", "repo", "source", "typed"}; !slices.Equal(got, want) {
		t.Errorf("subscriptions = %v, want %v", got, want)
	}
}

func TestFilterEmptyEventFieldNeverMatchesList(t *testing.T) {
	f := Filter{Connections: []string{"gh"}}
	if f.Match(Event{Type: "x", Origin: OriginPublished}) {
		t.Error("event without a connection matched a connection filter")
	}
	if (Filter{Sources: []string{"gh"}}).Match(Event{Type: "x", Origin: OriginPublished}) {
		t.Error("event without a source matched a source filter")
	}
	if (Filter{Sources: []string{""}}).validate() == nil {
		t.Error("empty source accepted")
	}
	if !(Filter{}).Match(Event{Type: "x", Origin: OriginPublished}) {
		t.Error("empty filter did not match")
	}
}

func TestAcceptDeduplicatesWithinWindow(t *testing.T) {
	r, clk := newReactor(t, Config{DedupWindow: time.Hour})
	subscribe(t, r, "all", Filter{})
	first := accept(t, r, webhookEvent(), "webhook:github:d1")

	clk.Add(30 * time.Minute)
	again, dup, err := r.Accept(context.Background(), Input{Event: webhookEvent(), Key: "webhook:github:d1"})
	if err != nil || !dup || again.ID != first.ID {
		t.Fatalf("duplicate Accept() = %v, %v, %v; want ID %s", again.ID, dup, err, first.ID)
	}
	if n := len(deliveries(t, r, DeliveryQuery{})); n != 1 {
		t.Errorf("deliveries = %d, want 1", n)
	}

	// An unkeyed input is never deduplicated.
	if e := accept(t, r, webhookEvent(), ""); e.ID == first.ID {
		t.Error("unkeyed input reused an ID")
	}

	clk.Add(time.Hour)
	if e := accept(t, r, webhookEvent(), "webhook:github:d1"); e.ID == first.ID {
		t.Error("key past the dedup window was still deduplicated")
	}
}

func TestAcceptRejectsInvalidEvents(t *testing.T) {
	r, _ := newReactor(t, Config{})
	for _, mutate := range []func(*Event){
		func(e *Event) { e.Type = "Bad Type" },
		func(e *Event) { e.Origin = "elsewhere" },
		func(e *Event) { e.Data = json.RawMessage(`[1]`) },
		func(e *Event) { e.Subject = "line\nbreak" },
	} {
		e := webhookEvent()
		mutate(&e)
		if _, _, err := r.Accept(context.Background(), Input{Event: e}); !errors.Is(err, ErrInvalid) {
			t.Errorf("Accept(%+v) = %v, want ErrInvalid", e, err)
		}
	}
}

func TestStoreFailureStoresNothing(t *testing.T) {
	failing := true
	store := &JSONStore{Save: func(context.Context, []byte) error {
		if failing {
			return errors.New("disk full")
		}

		return nil
	}}
	r, _ := newReactor(t, Config{Store: store})

	if _, _, err := r.Accept(context.Background(), Input{Event: webhookEvent(), Key: "k"}); err == nil {
		t.Fatal("Accept() succeeded with a failing store")
	}
	if s := state(t, r); len(s.Events) != 0 || len(s.Seen) != 0 {
		t.Fatalf("failed Accept left state %+v", s)
	}

	// A retry of the same delivery is accepted, not treated as a duplicate.
	failing = false
	if _, dup, err := r.Accept(context.Background(), Input{Event: webhookEvent(), Key: "k"}); err != nil || dup {
		t.Fatalf("retry = %v, %v", dup, err)
	}
}

func TestPublishSetsOriginAndAllowsNoForge(t *testing.T) {
	r, _ := newReactor(t, Config{})
	subscribe(t, r, "published", Filter{Origins: []Origin{OriginPublished}})
	subscribe(t, r, "repo", Filter{Repositories: []string{"owner/repo"}})

	in := Event{Type: "ci.finished", Origin: OriginWebhook, Source: "spoofed", Native: &Native{Event: "push"}, Data: json.RawMessage(`{"ok":true}`)}
	e, dup, err := r.Publish(context.Background(), in, "client-1")
	if err != nil || dup {
		t.Fatal(dup, err)
	}
	if e.Origin != OriginPublished || e.Source != "" || e.Native != nil || e.Forge != "" {
		t.Errorf("published event = %+v", e)
	}
	ds := deliveries(t, r, DeliveryQuery{EventID: e.ID})
	if len(ds) != 1 || ds[0].Subscription != "published" {
		t.Errorf("deliveries = %+v", ds)
	}

	again, dup, err := r.Publish(context.Background(), in, "client-1")
	if err != nil || !dup || again.ID != e.ID {
		t.Errorf("retried publish = %v, %v, %v", again.ID, dup, err)
	}
	// Publish keys cannot collide with webhook keys.
	if _, dup, _ := r.Accept(context.Background(), Input{Event: webhookEvent(), Key: "client-1"}); dup {
		t.Error("webhook key collided with a publish key")
	}
}

func TestAcceptBusyBeyondMaxPending(t *testing.T) {
	r, _ := newReactor(t, Config{MaxPending: 2})
	subscribe(t, r, "a", Filter{})
	subscribe(t, r, "b", Filter{})
	accept(t, r, webhookEvent(), "")
	if _, _, err := r.Accept(context.Background(), Input{Event: webhookEvent()}); !errors.Is(err, ErrBusy) {
		t.Fatalf("Accept() = %v, want ErrBusy", err)
	}
	if n := len(state(t, r).Events); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
}

func TestSubscriptionLifecycle(t *testing.T) {
	r, _ := newReactor(t, Config{})
	ctx := context.Background()
	subscribe(t, r, "ci", Filter{Types: []string{TypeBranchUpdated}})
	if _, err := r.CreateSubscription(ctx, Subscription{ID: "ci"}); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate create = %v", err)
	}
	if _, err := r.CreateSubscription(ctx, Subscription{ID: "Bad ID"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid create = %v", err)
	}
	if _, err := r.CreateSubscription(ctx, Subscription{ID: "x", Filter: Filter{Origins: []Origin{"nope"}}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid filter = %v", err)
	}
	sub, err := r.Subscription(ctx, "ci")
	if err != nil || sub.CreatedAt.IsZero() {
		t.Fatal(sub, err)
	}
	if subs, _ := r.Subscriptions(ctx); len(subs) != 1 {
		t.Errorf("subscriptions = %+v", subs)
	}

	e := accept(t, r, webhookEvent(), "")
	if err := r.DeleteSubscription(ctx, "ci"); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteSubscription(ctx, "ci"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v", err)
	}
	if _, err := r.Subscription(ctx, "ci"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted = %v", err)
	}
	ds := deliveries(t, r, DeliveryQuery{EventID: e.ID})
	if len(ds) != 1 || ds[0].Status != StatusCancelled {
		t.Errorf("deliveries after delete = %+v", ds)
	}

	// The ID is reusable only after the dispatcher removed the queue.
	if _, err := r.CreateSubscription(ctx, Subscription{ID: "ci"}); !errors.Is(err, ErrBusy) {
		t.Errorf("recreate before removal = %v, want ErrBusy", err)
	}
	b := &broker{}
	r.cycle(ctx, b, map[string]bool{})
	if !slices.Equal(b.removed, []string{"ci"}) || len(b.messages) != 0 {
		t.Fatalf("removed %v, sent %v", b.removed, b.messages)
	}
	if _, err := r.CreateSubscription(ctx, Subscription{ID: "ci"}); err != nil {
		t.Errorf("recreate after removal = %v", err)
	}
}

func TestObserveBaselineAndChanges(t *testing.T) {
	r, _ := newReactor(t, Config{})
	subscribe(t, r, "all", Filter{})
	ctx := context.Background()
	o := Observation{Source: "poll", Forge: "forgejo", Connection: "fj", Repository: "o/r", Branch: "main", Commit: "c1"}

	if e, err := r.Observe(ctx, o); err != nil || e != nil {
		t.Fatalf("first observation = %v, %v; want baseline only", e, err)
	}
	if e, err := r.Observe(ctx, o); err != nil || e != nil {
		t.Fatalf("unchanged observation = %v, %v", e, err)
	}

	o.Commit = "c2"
	e, err := r.Observe(ctx, o)
	if err != nil || e == nil {
		t.Fatalf("changed observation = %v, %v", e, err)
	}
	var data BranchUpdate
	if err := json.Unmarshal(e.Data, &data); err != nil {
		t.Fatal(err)
	}
	want := BranchUpdate{Branch: "main", Before: "c1", After: "c2"}
	if data != want || e.Type != TypeBranchUpdated || e.Origin != OriginPoll || e.Subject != "refs/heads/main" || e.Native != nil {
		t.Errorf("event = %+v, data %+v", e, data)
	}
	if n := len(deliveries(t, r, DeliveryQuery{EventID: e.ID})); n != 1 {
		t.Errorf("deliveries = %d", n)
	}
	if _, err := r.Observe(ctx, Observation{Repository: "o/r", Branch: "main"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("observation without commit = %v", err)
	}
}

func TestObserveSurvivesRestartAndStoreFailure(t *testing.T) {
	ctx := context.Background()
	var disk []byte
	failing := false
	save := func(_ context.Context, data []byte) error {
		if failing {
			return errors.New("disk full")
		}
		disk = append([]byte(nil), data...)

		return nil
	}
	open := func() *Reactor {
		store, err := NewJSONStore(disk, save)
		if err != nil {
			t.Fatal(err)
		}
		r, _ := newReactor(t, Config{Store: store})

		return r
	}
	o := Observation{Source: "poll", Forge: "forgejo", Connection: "fj", Repository: "o/r", Branch: "main", Commit: "c1"}

	r := open()
	subscribe(t, r, "all", Filter{})
	if _, err := r.Observe(ctx, o); err != nil {
		t.Fatal(err)
	}

	// A restart keeps the baseline: an unchanged head emits nothing.
	r = open()
	if e, err := r.Observe(ctx, o); err != nil || e != nil {
		t.Fatalf("after restart = %v, %v", e, err)
	}

	// A change that cannot be stored is not lost: the baseline stays.
	o.Commit = "c2"
	failing = true
	if _, err := r.Observe(ctx, o); err == nil {
		t.Fatal("Observe() succeeded with a failing store")
	}
	failing = false
	r = open()
	e, err := r.Observe(ctx, o)
	if err != nil || e == nil {
		t.Fatalf("observation after failure = %v, %v", e, err)
	}

	// The emitted event and its delivery survive another restart.
	r = open()
	if ds := deliveries(t, r, DeliveryQuery{EventID: e.ID}); len(ds) != 1 || ds[0].Status != StatusPending {
		t.Errorf("deliveries after restart = %+v", ds)
	}
}

func TestNewJSONStoreRejectsCorruptData(t *testing.T) {
	if _, err := NewJSONStore([]byte("{"), nil); err == nil {
		t.Error("NewJSONStore accepted corrupt data")
	}
}
