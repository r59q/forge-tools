package reactor

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReplayByIDKeepsEventIdentity(t *testing.T) {
	r, clk := newReactor(t, Config{})
	ctx := context.Background()
	subscribe(t, r, "a", Filter{Types: []string{TypeBranchUpdated}})
	e := accept(t, r, webhookEvent(), "")
	b := &broker{}
	r.cycle(ctx, b, map[string]bool{})

	clk.Add(time.Minute)
	created, err := r.Replay(ctx, ReplayRequest{Subscription: "a", EventIDs: []string{e.ID, e.ID}})
	if err != nil || len(created) != 1 {
		t.Fatalf("Replay() = %+v, %v", created, err)
	}
	if d := created[0]; d.EventID != e.ID || !d.Replay || d.Status != StatusPending {
		t.Errorf("replay delivery = %+v", d)
	}
	if n := len(state(t, r).Events); n != 1 {
		t.Errorf("replay created events: %d", n)
	}

	r.cycle(ctx, b, map[string]bool{})
	msgs := b.sent()
	if len(msgs) != 2 || msgs[1].Event.ID != e.ID || !msgs[1].Replay || msgs[1].DeliveryID == msgs[0].DeliveryID {
		t.Fatalf("messages = %+v", msgs)
	}
	_, ds, err := r.Event(ctx, e.ID)
	if err != nil || len(ds) != 2 || ds[0].Replay || !ds[1].Replay || ds[1].Status != StatusAccepted {
		t.Errorf("event deliveries = %+v, %v", ds, err)
	}
}

func TestReplayRejectsBadRequests(t *testing.T) {
	r, clk := newReactor(t, Config{})
	ctx := context.Background()
	subscribe(t, r, "pulls", Filter{Types: []string{TypePullRequestOpened}})
	e := accept(t, r, webhookEvent(), "")
	now := clk.Now()

	for name, tc := range map[string]struct {
		req  ReplayRequest
		want error
	}{
		"neither":         {ReplayRequest{Subscription: "pulls"}, ErrInvalid},
		"both":            {ReplayRequest{Subscription: "pulls", EventIDs: []string{e.ID}, Since: now}, ErrInvalid},
		"empty range":     {ReplayRequest{Subscription: "pulls", Since: now, Until: now}, ErrInvalid},
		"no subscription": {ReplayRequest{Subscription: "nope", EventIDs: []string{e.ID}}, ErrNotFound},
		"unknown event":   {ReplayRequest{Subscription: "pulls", EventIDs: []string{"missing"}}, ErrNotFound},
		"filter mismatch": {ReplayRequest{Subscription: "pulls", EventIDs: []string{e.ID}}, ErrInvalid},
	} {
		if _, err := r.Replay(ctx, tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: Replay() = %v, want %v", name, err, tc.want)
		}
	}
}

func TestReplayRangeAppliesFilterAndRetention(t *testing.T) {
	r, clk := newReactor(t, Config{Retention: time.Hour})
	ctx := context.Background()
	subscribe(t, r, "main", Filter{Repositories: []string{"owner/repo"}})
	start := clk.Now()

	old := accept(t, r, webhookEvent(), "")
	clk.Add(10 * time.Minute)
	other := webhookEvent()
	other.Repository = "someone/else"
	accept(t, r, other, "")
	clk.Add(10 * time.Minute)
	recent := accept(t, r, webhookEvent(), "")
	r.cycle(ctx, &broker{}, map[string]bool{})

	created, err := r.Replay(ctx, ReplayRequest{Subscription: "main", Since: start})
	if err != nil || len(created) != 2 || created[0].EventID != old.ID || created[1].EventID != recent.ID {
		t.Fatalf("Replay(range) = %+v, %v", created, err)
	}
	created, err = r.Replay(ctx, ReplayRequest{Subscription: "main", Since: start, Until: start.Add(time.Minute)})
	if err != nil || len(created) != 1 || created[0].EventID != old.ID {
		t.Fatalf("Replay(bounded range) = %+v, %v", created, err)
	}
	r.cycle(ctx, &broker{}, map[string]bool{})

	// Once the oldest event expires, periods reaching back to it fail
	// explicitly instead of returning a partial history.
	clk.Add(45 * time.Minute)
	if err := r.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Replay(ctx, ReplayRequest{Subscription: "main", Since: start}); !errors.Is(err, ErrOutOfRange) {
		t.Errorf("Replay(pruned range) = %v, want ErrOutOfRange", err)
	}
	if _, err := r.Replay(ctx, ReplayRequest{Subscription: "main", EventIDs: []string{old.ID}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Replay(pruned ID) = %v, want ErrNotFound", err)
	}
	if created, err := r.Replay(ctx, ReplayRequest{Subscription: "main", Since: start.Add(5 * time.Minute)}); err != nil || len(created) != 1 {
		t.Errorf("Replay(retained range) = %+v, %v", created, err)
	}
}

func TestPruneKeepsPendingAndCapsEvents(t *testing.T) {
	r, clk := newReactor(t, Config{Retention: time.Hour, MaxEvents: 2})
	ctx := context.Background()
	subscribe(t, r, "a", Filter{})
	first := accept(t, r, webhookEvent(), "")
	clk.Add(time.Minute)

	// The pending event outlives its retention until it is handed off.
	clk.Add(2 * time.Hour)
	if err := r.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Event(ctx, first.ID); err != nil {
		t.Fatalf("pending event pruned: %v", err)
	}
	r.cycle(ctx, &broker{}, map[string]bool{})
	if err := r.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Event(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired event kept: %v", err)
	}
	if n := len(state(t, r).Deliveries); n != 0 {
		t.Errorf("deliveries of pruned event kept: %d", n)
	}

	// MaxEvents drops the oldest handed-off events first.
	if err := r.DeleteSubscription(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 3 {
		clk.Add(time.Second)
		ids = append(ids, accept(t, r, webhookEvent(), "").ID)
	}
	if err := r.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	s := state(t, r)
	if _, ok := s.Events[ids[0]]; ok || len(s.Events) != 2 {
		t.Errorf("events after cap = %d, oldest kept %v", len(s.Events), ok)
	}
}

func TestListingPagesNewestFirst(t *testing.T) {
	r, clk := newReactor(t, Config{})
	ctx := context.Background()
	var ids []string
	for range 5 {
		ids = append(ids, accept(t, r, webhookEvent(), "").ID)
		clk.Add(time.Second)
	}
	pull := webhookEvent()
	pull.Type = TypePullRequestOpened
	accept(t, r, pull, "")

	var got []string
	token := ""
	for {
		events, next, err := r.Events(ctx, EventQuery{Filter: Filter{Types: []string{TypeBranchUpdated}}, Limit: 2, PageToken: token})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			got = append(got, e.ID)
		}
		if next == "" {
			break
		}
		token = next
	}
	if len(got) != 5 {
		t.Fatalf("listed %d events", len(got))
	}
	for i := range got {
		if got[i] != ids[len(ids)-1-i] {
			t.Fatalf("order = %v, want reverse of %v", got, ids)
		}
	}

	if _, _, err := r.Events(ctx, EventQuery{PageToken: "garbage"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad token = %v", err)
	}
	if _, _, err := r.Events(ctx, EventQuery{Limit: MaxPageSize + 1}); !errors.Is(err, ErrInvalid) {
		t.Errorf("oversized page = %v", err)
	}
	if _, _, err := r.Event(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing event = %v", err)
	}
}
