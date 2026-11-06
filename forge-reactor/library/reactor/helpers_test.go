package reactor

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// broker records hand-offs and fails while fail is set.
type broker struct {
	mu        sync.Mutex
	fail      error
	removeErr error
	prepared  []string
	removed   []string
	messages  []Message
}

func (b *broker) Prepare(_ context.Context, sub string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return b.fail
	}
	b.prepared = append(b.prepared, sub)

	return nil
}

func (b *broker) Publish(_ context.Context, m Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return b.fail
	}
	b.messages = append(b.messages, m)

	return nil
}

func (b *broker) Remove(_ context.Context, sub string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.removeErr != nil {
		return b.removeErr
	}
	b.removed = append(b.removed, sub)

	return nil
}

func (b *broker) sent() []Message {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]Message(nil), b.messages...)
}

func (b *broker) setFail(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fail = err
}

var errBroker = errors.New("broker down")

// newReactor returns a reactor on an in-memory store and a test clock.
func newReactor(t *testing.T, c Config) (*Reactor, *clock) {
	t.Helper()
	clk := newClock()
	if c.Store == nil {
		c.Store = &JSONStore{}
	}
	c.Now = clk.Now
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}

	return r, clk
}

func subscribe(t *testing.T, r *Reactor, id string, f Filter) {
	t.Helper()
	if _, err := r.CreateSubscription(context.Background(), Subscription{ID: id, Filter: f}); err != nil {
		t.Fatal(err)
	}
}

// webhookEvent is a typical normalized webhook event.
func webhookEvent() Event {
	return Event{
		Type:       TypeBranchUpdated,
		Origin:     OriginWebhook,
		Source:     "github",
		Forge:      "github",
		Connection: "gh",
		Repository: "Owner/Repo",
		Subject:    "refs/heads/main",
		Data:       json.RawMessage(`{"branch":"main","before":"a","after":"b"}`),
		Native:     &Native{Event: "push", Delivery: "d1", Payload: json.RawMessage(`{"ref":"refs/heads/main"}`)},
	}
}

func accept(t *testing.T, r *Reactor, e Event, key string) Event {
	t.Helper()
	got, dup, err := r.Accept(context.Background(), Input{Event: e, Key: key})
	if err != nil || dup {
		t.Fatalf("Accept() = %v, %v", dup, err)
	}

	return got
}

func deliveries(t *testing.T, r *Reactor, q DeliveryQuery) []Delivery {
	t.Helper()
	q.Limit = MaxPageSize
	ds, _, err := r.Deliveries(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}

	return ds
}

func state(t *testing.T, r *Reactor) State {
	t.Helper()
	s, err := r.cfg.Store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	return s
}
