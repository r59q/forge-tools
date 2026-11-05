package reactor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCycleHandsOffAndRecordsAcceptance(t *testing.T) {
	r, _ := newReactor(t, Config{})
	subscribe(t, r, "a", Filter{})
	subscribe(t, r, "b", Filter{})
	e := accept(t, r, webhookEvent(), "")

	b := &broker{}
	r.cycle(context.Background(), b, map[string]bool{})
	if len(b.prepared) != 2 {
		t.Errorf("prepared = %v", b.prepared)
	}
	msgs := b.sent()
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v", msgs)
	}
	for _, m := range msgs {
		var got Event
		if err := json.Unmarshal(m.Body, &got); err != nil || got.ID != e.ID || m.Event.ID != e.ID || m.Attempt != 1 || m.Replay {
			t.Errorf("message = %+v (%v)", m, err)
		}
		if got.Native == nil || got.Native.Delivery != "d1" {
			t.Errorf("message lost the native details: %+v", got)
		}
	}
	for _, d := range deliveries(t, r, DeliveryQuery{}) {
		if d.Status != StatusAccepted || d.AcceptedAt.IsZero() || len(d.Attempts) != 1 || d.Attempts[0].Error != "" {
			t.Errorf("delivery = %+v", d)
		}
	}

	// Nothing is handed off twice.
	r.cycle(context.Background(), b, map[string]bool{})
	if n := len(b.sent()); n != 2 {
		t.Errorf("messages after second cycle = %d", n)
	}
}

func TestCycleRetriesWithBackoffThenFails(t *testing.T) {
	r, clk := newReactor(t, Config{Retry: Retry{Attempts: 3, Delay: time.Second, MaxDelay: 3 * time.Second}})
	subscribe(t, r, "a", Filter{})
	accept(t, r, webhookEvent(), "")
	b := &broker{fail: errBroker}
	ctx := context.Background()
	prepared := map[string]bool{}

	if wait := r.cycle(ctx, b, prepared); wait != time.Second {
		t.Errorf("wait after failure = %v", wait)
	}
	d := deliveries(t, r, DeliveryQuery{})[0]
	if d.Status != StatusPending || len(d.Attempts) != 1 || d.Attempts[0].Error == "" || !d.NextAttemptAt.Equal(clk.Now().Add(time.Second)) {
		t.Fatalf("after first failure = %+v", d)
	}

	// Not due yet: no attempt.
	r.cycle(ctx, b, prepared)
	if d := deliveries(t, r, DeliveryQuery{})[0]; len(d.Attempts) != 1 {
		t.Fatalf("attempted before due: %+v", d)
	}

	clk.Add(time.Second)
	r.cycle(ctx, b, prepared)
	d = deliveries(t, r, DeliveryQuery{})[0]
	if len(d.Attempts) != 2 || !d.NextAttemptAt.Equal(clk.Now().Add(2*time.Second)) {
		t.Fatalf("after second failure = %+v", d)
	}

	clk.Add(2 * time.Second)
	r.cycle(ctx, b, prepared)
	d = deliveries(t, r, DeliveryQuery{})[0]
	if d.Status != StatusFailed || len(d.Attempts) != 3 || !d.NextAttemptAt.IsZero() {
		t.Fatalf("after exhausting attempts = %+v", d)
	}

	// A failed delivery stays inspectable and is not retried.
	b.setFail(nil)
	clk.Add(time.Hour)
	r.cycle(ctx, b, prepared)
	if len(b.sent()) != 0 {
		t.Error("failed delivery was handed off")
	}
	if ds := deliveries(t, r, DeliveryQuery{Status: StatusFailed}); len(ds) != 1 {
		t.Errorf("failed deliveries = %+v", ds)
	}
}

func TestCycleRecoversAfterOutage(t *testing.T) {
	r, clk := newReactor(t, Config{Retry: Retry{Delay: time.Second}})
	subscribe(t, r, "a", Filter{})
	first := accept(t, r, webhookEvent(), "")
	second := accept(t, r, webhookEvent(), "")
	b := &broker{fail: errBroker}
	ctx := context.Background()
	prepared := map[string]bool{}

	// The first failure stops the cycle, so the later delivery waits.
	r.cycle(ctx, b, prepared)
	for _, d := range deliveries(t, r, DeliveryQuery{}) {
		want := 0
		if d.EventID == first.ID {
			want = 1
		}
		if len(d.Attempts) != want {
			t.Errorf("delivery %s attempts = %d, want %d", d.EventID, len(d.Attempts), want)
		}
	}

	b.setFail(nil)
	clk.Add(time.Second)
	r.cycle(ctx, b, prepared)
	msgs := b.sent()
	if len(msgs) != 2 || msgs[0].Event.ID != first.ID || msgs[0].Attempt != 2 || msgs[1].Event.ID != second.ID || msgs[1].Attempt != 1 {
		t.Fatalf("messages after recovery = %+v", msgs)
	}
	if ds := deliveries(t, r, DeliveryQuery{Status: StatusAccepted}); len(ds) != 2 {
		t.Errorf("accepted = %d", len(ds))
	}
	if len(b.prepared) != 1 {
		t.Errorf("prepared = %v", b.prepared)
	}
}

func TestCycleKeepsDeletedSubscriptionBlockedUntilRemoved(t *testing.T) {
	r, _ := newReactor(t, Config{})
	subscribe(t, r, "a", Filter{})
	accept(t, r, webhookEvent(), "")
	ctx := context.Background()
	if err := r.DeleteSubscription(ctx, "a"); err != nil {
		t.Fatal(err)
	}

	b := &broker{removeErr: errBroker}
	if wait := r.cycle(ctx, b, map[string]bool{}); wait != r.cfg.Retry.Delay {
		t.Errorf("wait with a blocked removal = %v", wait)
	}
	if len(b.sent()) != 0 || len(state(t, r).Removals) != 1 {
		t.Fatal("published to a deleted subscription or forgot the removal")
	}
	b.removeErr = nil
	r.cycle(ctx, b, map[string]bool{})
	if len(b.removed) != 1 || len(state(t, r).Removals) != 0 {
		t.Errorf("removed %v, removals %v", b.removed, state(t, r).Removals)
	}
}

func TestCycleKeepsCancellationRecordedDuringHandOff(t *testing.T) {
	r, _ := newReactor(t, Config{})
	subscribe(t, r, "a", Filter{})
	e := accept(t, r, webhookEvent(), "")
	d := deliveries(t, r, DeliveryQuery{})[0]
	if err := r.DeleteSubscription(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.record(context.Background(), []outcome{{id: d.ID, at: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if got := deliveries(t, r, DeliveryQuery{EventID: e.ID})[0]; got.Status != StatusCancelled {
		t.Errorf("status = %s, want cancelled", got.Status)
	}
}

func TestBackoffDoublesUpToMax(t *testing.T) {
	r := Retry{Delay: time.Second, MaxDelay: 5 * time.Second}
	for n, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 4: 5 * time.Second, 50: 5 * time.Second} {
		if got := r.backoff(n); got != want {
			t.Errorf("backoff(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestRunDeliversUntilCancelled(t *testing.T) {
	r, err := New(Config{Store: &JSONStore{}})
	if err != nil {
		t.Fatal(err)
	}
	subscribe(t, r, "a", Filter{})
	ctx, cancel := context.WithCancel(context.Background())
	b := &broker{}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, b) }()

	e := accept(t, r, webhookEvent(), "")
	deadline := time.Now().Add(5 * time.Second)
	for len(b.sent()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if msgs := b.sent(); len(msgs) != 1 || msgs[0].Event.ID != e.ID {
		t.Fatalf("messages = %+v", msgs)
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run() = %v", err)
	}
}
