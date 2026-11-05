package reactor

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Defaults for Config fields left zero.
const (
	DefaultRetention   = 7 * 24 * time.Hour
	DefaultMaxEvents   = 1000
	DefaultDedupWindow = 72 * time.Hour
	DefaultMaxPending  = 10000
)

// maxKey caps Input keys.
const maxKey = 512

// Config configures a Reactor.
type Config struct {
	// Store keeps all state. It is required.
	Store Store
	// Now returns the current time. Nil uses time.Now.
	Now func() time.Time
	// Retention is how long accepted events and their delivery history are
	// kept, unless a delivery is still pending.
	Retention time.Duration
	// MaxEvents caps retained events; the oldest go first.
	MaxEvents int
	// DedupWindow is how long an Input key is remembered.
	DedupWindow time.Duration
	// MaxPending caps deliveries waiting for the broker. New events and
	// replays fail with ErrBusy beyond it.
	MaxPending int
	// Retry controls broker hand-off attempts.
	Retry Retry
}

// Reactor accepts events, fans them out to subscriptions and hands each
// delivery to a Broker. It is safe for concurrent use.
type Reactor struct {
	cfg  Config
	wake chan struct{}
}

// Observation is one poll reading of a branch head.
type Observation struct {
	// Source is the poll target ID.
	Source string
	// Forge is the forge kind.
	Forge forgeauth.Kind
	// Connection is the forge-connector connection ID.
	Connection string
	// Repository is the owner/name path.
	Repository string
	// Branch is the branch name.
	Branch string
	// Commit is the head commit read from the forge.
	Commit string
}

// New validates c and returns a Reactor.
func New(c Config) (*Reactor, error) {
	if c.Store == nil || c.Retention < 0 || c.MaxEvents < 0 || c.DedupWindow < 0 || c.MaxPending < 0 {
		return nil, ErrInvalid
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	c.Retention = cmp.Or(c.Retention, DefaultRetention)
	c.MaxEvents = cmp.Or(c.MaxEvents, DefaultMaxEvents)
	c.DedupWindow = cmp.Or(c.DedupWindow, DefaultDedupWindow)
	c.MaxPending = cmp.Or(c.MaxPending, DefaultMaxPending)
	retry, err := c.Retry.withDefaults()
	if err != nil {
		return nil, err
	}
	c.Retry = retry

	return &Reactor{cfg: c, wake: make(chan struct{}, 1)}, nil
}

// Accept validates and durably stores in's event with one pending delivery
// per matching subscription. It returns only after the store commits. A key
// seen within the dedup window returns the earlier event with duplicate set
// and stores nothing; the earlier event may no longer be retained, leaving
// only its ID.
func (r *Reactor) Accept(ctx context.Context, in Input) (event Event, duplicate bool, err error) {
	if err := in.Event.validate(); err != nil || len(in.Key) > maxKey {
		return Event{}, false, ErrInvalid
	}

	err = r.cfg.Store.Update(ctx, func(s *State) error {
		now := r.cfg.Now()
		r.prune(s, now)
		if seen, ok := s.Seen[in.Key]; ok && in.Key != "" {
			event, ok = s.Events[seen.EventID]
			if !ok {
				event = Event{ID: seen.EventID}
			}
			duplicate = true

			return nil
		}

		accepted, err := r.add(s, in.Event, now)
		if err != nil {
			return err
		}
		if in.Key != "" {
			s.Seen[in.Key] = Seen{EventID: accepted.ID, At: now}
		}
		event, duplicate = accepted, false

		return nil
	})
	if err != nil {
		return Event{}, false, err
	}
	if !duplicate {
		r.signal()
	}

	return event, duplicate, nil
}

// Publish accepts an event from an authorized caller. Origin is set to
// OriginPublished, and Source and Native are cleared; forge fields stay
// empty unless given. A non-empty key deduplicates retries of one publish.
func (r *Reactor) Publish(ctx context.Context, e Event, key string) (Event, bool, error) {
	e.Origin, e.Source, e.Native = OriginPublished, "", nil
	if key != "" {
		key = "published:" + key
	}

	return r.Accept(ctx, Input{Event: e, Key: key})
}

// Observe records a poll reading. The first reading of a target sets its
// baseline and emits nothing; an unchanged head emits nothing; a new head
// emits TypeBranchUpdated. The event and the new baseline commit together,
// so a failure keeps the old baseline and the change is seen again.
func (r *Reactor) Observe(ctx context.Context, o Observation) (*Event, error) {
	if o.Commit == "" || o.Branch == "" || o.Repository == "" || !validText(o.Commit, 128) || !validText(o.Branch, 255) {
		return nil, ErrInvalid
	}

	key := o.Connection + ":" + o.Repository + ":" + o.Branch
	var emitted *Event
	err := r.cfg.Store.Update(ctx, func(s *State) error {
		now := r.cfg.Now()
		r.prune(s, now)
		base, ok := s.Baselines[key]
		if ok && base.Commit == o.Commit {
			return nil
		}
		if ok {
			e, err := r.add(s, observed(o, base.Commit), now)
			if err != nil {
				return err
			}
			emitted = &e
		}
		s.Baselines[key] = Baseline{Commit: o.Commit, ObservedAt: now}

		return nil
	})
	if err != nil {
		return nil, err
	}
	if emitted != nil {
		r.signal()
	}

	return emitted, nil
}

// observed builds the event for a changed poll reading.
func observed(o Observation, before string) Event {
	return Event{
		Type:       TypeBranchUpdated,
		Origin:     OriginPoll,
		Source:     o.Source,
		Forge:      o.Forge,
		Connection: o.Connection,
		Repository: o.Repository,
		Subject:    "refs/heads/" + o.Branch,
		Data:       mustJSON(BranchUpdate{Branch: o.Branch, Before: before, After: o.Commit}),
	}
}

// add stores e as a new event with its deliveries.
func (r *Reactor) add(s *State, e Event, now time.Time) (Event, error) {
	subs := matching(s, e)
	if pending(s)+len(subs) > r.cfg.MaxPending {
		return Event{}, ErrBusy
	}

	e.ID, e.AcceptedAt = newID(), now
	s.Events[e.ID] = e
	for _, sub := range subs {
		addDelivery(s, e.ID, sub.ID, false, now)
	}

	return e, nil
}

// matching returns the subscriptions selecting e, in ID order.
func matching(s *State, e Event) []Subscription {
	var subs []Subscription
	for _, sub := range s.Subscriptions {
		if sub.Filter.Match(e) {
			subs = append(subs, sub)
		}
	}
	slices.SortFunc(subs, func(a, b Subscription) int { return cmp.Compare(a.ID, b.ID) })

	return subs
}

// addDelivery stores a new pending delivery.
func addDelivery(s *State, eventID, subscription string, replay bool, now time.Time) Delivery {
	s.Sequence++
	d := Delivery{
		ID:            newID(),
		Sequence:      s.Sequence,
		EventID:       eventID,
		Subscription:  subscription,
		Replay:        replay,
		Status:        StatusPending,
		CreatedAt:     now,
		NextAttemptAt: now,
	}
	s.Deliveries[d.ID] = d

	return d
}

// pending counts deliveries waiting for the broker.
func pending(s *State) int {
	n := 0
	for _, d := range s.Deliveries {
		if d.Status == StatusPending {
			n++
		}
	}

	return n
}

// signal wakes the dispatcher.
func (r *Reactor) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
