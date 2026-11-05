package reactor

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"time"
)

// Defaults for Retry fields left zero.
const (
	DefaultAttempts = 20
	DefaultDelay    = time.Second
	DefaultMaxDelay = 5 * time.Minute
	DefaultTimeout  = 10 * time.Second
)

const (
	// batch caps the deliveries handed off per cycle.
	batch = 100
	// idle is the longest the dispatcher sleeps without a wake-up.
	idle = 30 * time.Second
	// pruneEvery is how often an idle dispatcher prunes.
	pruneEvery = 5 * time.Minute
)

// Message is one delivery handed to a broker.
type Message struct {
	// Subscription names the receiving subscription and its queue.
	Subscription string
	// DeliveryID identifies the delivery.
	DeliveryID string
	// Attempt counts hand-off attempts, starting at 1.
	Attempt int
	// Replay reports a delivery requested by a replay.
	Replay bool
	// Event is the delivered event.
	Event Event
	// Body is Event encoded as JSON, the message consumers receive.
	Body []byte
}

// Broker hands deliveries to a message broker. Implementations must be safe
// for concurrent use.
type Broker interface {
	// Prepare makes sure the subscription's queue exists, so consumers can
	// attach before any event. It is idempotent.
	Prepare(ctx context.Context, subscription string) error
	// Publish returns nil only once the broker has taken durable
	// responsibility for m, such as by a publisher confirm.
	Publish(ctx context.Context, m Message) error
	// Remove deletes the subscription's queue and its messages. Removing a
	// missing queue succeeds.
	Remove(ctx context.Context, subscription string) error
}

// Retry bounds broker hand-off attempts. The delay doubles after each
// failed attempt, up to MaxDelay; after Attempts failures a delivery fails.
type Retry struct {
	// Attempts caps attempts per delivery.
	Attempts int
	// Delay is the wait after the first failure.
	Delay time.Duration
	// MaxDelay caps the wait.
	MaxDelay time.Duration
	// Timeout caps one attempt.
	Timeout time.Duration
}

// withDefaults fills zero fields.
func (r Retry) withDefaults() (Retry, error) {
	if r.Attempts < 0 || r.Delay < 0 || r.MaxDelay < 0 || r.Timeout < 0 {
		return Retry{}, ErrInvalid
	}
	r.Attempts = cmp.Or(r.Attempts, DefaultAttempts)
	r.Delay = cmp.Or(r.Delay, DefaultDelay)
	r.MaxDelay = max(cmp.Or(r.MaxDelay, DefaultMaxDelay), r.Delay)
	r.Timeout = cmp.Or(r.Timeout, DefaultTimeout)

	return r, nil
}

// backoff is the wait after the nth failed attempt.
func (r Retry) backoff(n int) time.Duration {
	d := r.Delay
	for i := 1; i < n && d < r.MaxDelay; i++ {
		d *= 2
	}

	return min(d, r.MaxDelay)
}

// outcome is the result of one hand-off.
type outcome struct {
	id  string
	at  time.Time
	err error
}

// Run hands pending deliveries to b until ctx ends, then returns ctx's
// error. Deliveries survive restarts because they are read from the store
// each cycle. Within one subscription deliveries are handed off in creation
// order, but a retry can overtake a later delivery; there is no global
// order.
func (r *Reactor) Run(ctx context.Context, b Broker) error {
	prepared := map[string]bool{}
	lastPrune := time.Time{}
	for {
		if now := r.cfg.Now(); now.Sub(lastPrune) >= pruneEvery {
			if r.Prune(ctx) == nil {
				lastPrune = now
			}
		}

		wait := r.cycle(ctx, b, prepared)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()

			return ctx.Err()
		case <-r.wake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// cycle runs one dispatch round and returns how long to wait.
func (r *Reactor) cycle(ctx context.Context, b Broker, prepared map[string]bool) time.Duration {
	s, err := r.cfg.Store.Read(ctx)
	if err != nil {
		return r.cfg.Retry.Delay
	}

	blocked := r.removeQueues(ctx, b, s.Removals, prepared)
	for id := range s.Subscriptions {
		if !prepared[id] && !blocked[id] && r.attempt(ctx, func(ctx context.Context) error { return b.Prepare(ctx, id) }) == nil {
			prepared[id] = true
		}
	}

	due := dueDeliveries(s, r.cfg.Now(), blocked)
	var outcomes []outcome
	for _, d := range due {
		m := Message{Subscription: d.Subscription, DeliveryID: d.ID, Attempt: len(d.Attempts) + 1, Replay: d.Replay, Event: s.Events[d.EventID]}
		m.Body, err = json.Marshal(m.Event)
		if err == nil {
			err = r.attempt(ctx, func(ctx context.Context) error { return b.Publish(ctx, m) })
		}
		outcomes = append(outcomes, outcome{id: d.ID, at: r.cfg.Now(), err: err})
		if err != nil {
			break
		}
	}
	if len(outcomes) == 0 {
		if len(blocked) > 0 {
			return r.cfg.Retry.Delay
		}

		return untilDue(s, r.cfg.Now())
	}

	next, err := r.record(ctx, outcomes)
	switch {
	case err != nil || outcomes[len(outcomes)-1].err != nil:
		return r.cfg.Retry.Delay
	case len(due) == batch:
		return 0
	default:
		return next
	}
}

// attempt runs fn with the attempt timeout.
func (r *Reactor) attempt(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.Retry.Timeout)
	defer cancel()

	return fn(ctx)
}

// removeQueues removes deleted subscriptions' queues and returns the IDs
// whose removal failed, which must not be prepared or published to.
func (r *Reactor) removeQueues(ctx context.Context, b Broker, ids []string, prepared map[string]bool) map[string]bool {
	blocked := map[string]bool{}
	for _, id := range ids {
		err := r.attempt(ctx, func(ctx context.Context) error { return b.Remove(ctx, id) })
		if err == nil {
			delete(prepared, id)
			err = r.cfg.Store.Update(ctx, func(s *State) error {
				s.Removals = slices.DeleteFunc(s.Removals, func(x string) bool { return x == id })

				return nil
			})
		}
		if err != nil {
			blocked[id] = true
		}
	}

	return blocked
}

// dueDeliveries returns up to batch pending deliveries due at now, oldest
// first, skipping blocked subscriptions.
func dueDeliveries(s State, now time.Time, blocked map[string]bool) []Delivery {
	var due []Delivery
	for _, d := range s.Deliveries {
		if d.Status == StatusPending && !d.NextAttemptAt.After(now) && !blocked[d.Subscription] {
			due = append(due, d)
		}
	}
	slices.SortFunc(due, func(a, b Delivery) int {
		return cmp.Or(cmp.Compare(a.Sequence, b.Sequence), cmp.Compare(a.ID, b.ID))
	})

	return due[:min(len(due), batch)]
}

// record stores outcomes and returns the wait until the next due delivery.
// A delivery cancelled meanwhile stays cancelled.
func (r *Reactor) record(ctx context.Context, outcomes []outcome) (time.Duration, error) {
	var next time.Duration
	err := r.cfg.Store.Update(ctx, func(s *State) error {
		for _, o := range outcomes {
			d, ok := s.Deliveries[o.id]
			if !ok || d.Status != StatusPending {
				continue
			}
			r.apply(&d, o)
			s.Deliveries[d.ID] = d
		}
		next = untilDue(*s, r.cfg.Now())

		return nil
	})

	return next, err
}

// apply records one outcome on d.
func (r *Reactor) apply(d *Delivery, o outcome) {
	if o.err == nil {
		d.Attempts = append(d.Attempts, Attempt{At: o.at})
		d.Status, d.AcceptedAt, d.NextAttemptAt = StatusAccepted, o.at, time.Time{}

		return
	}

	d.Attempts = append(d.Attempts, Attempt{At: o.at, Error: o.err.Error()})
	if len(d.Attempts) >= r.cfg.Retry.Attempts {
		d.Status, d.NextAttemptAt = StatusFailed, time.Time{}

		return
	}
	d.NextAttemptAt = o.at.Add(r.cfg.Retry.backoff(len(d.Attempts)))
}

// untilDue is the wait until the earliest pending delivery, capped at idle.
func untilDue(s State, now time.Time) time.Duration {
	wait := idle
	for _, d := range s.Deliveries {
		if d.Status == StatusPending {
			wait = min(wait, max(d.NextAttemptAt.Sub(now), 0))
		}
	}

	return wait
}
