package reactor

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"
)

// MaxReplay caps the events one replay may deliver.
const MaxReplay = 1000

// ReplayRequest asks to deliver retained events to one subscription again.
// Give either EventIDs or Since, not both.
type ReplayRequest struct {
	// Subscription receives the events.
	Subscription string
	// EventIDs are specific retained events.
	EventIDs []string
	// Since selects events accepted at or after it.
	Since time.Time
	// Until, if set, selects events accepted before it.
	Until time.Time
}

// Replay creates one new delivery per selected event for the subscription,
// in acceptance order, and returns them. Events keep their IDs; no new event
// is created. Every event must match the subscription's filter. A pruned or
// unknown event ID fails with ErrNotFound, a period starting before
// retained history with ErrOutOfRange, and more than MaxReplay events with
// ErrInvalid.
func (r *Reactor) Replay(ctx context.Context, req ReplayRequest) ([]Delivery, error) {
	byID := len(req.EventIDs) > 0
	if byID == !req.Since.IsZero() || len(req.EventIDs) > MaxReplay || (!req.Until.IsZero() && !req.Until.After(req.Since)) {
		return nil, ErrInvalid
	}

	var created []Delivery
	err := r.cfg.Store.Update(ctx, func(s *State) error {
		now := r.cfg.Now()
		r.prune(s, now)
		sub, ok := s.Subscriptions[req.Subscription]
		if !ok {
			return ErrNotFound
		}

		events, err := replayed(s, sub, req)
		if err != nil {
			return err
		}
		if pending(s)+len(events) > r.cfg.MaxPending {
			return ErrBusy
		}
		for _, e := range events {
			created = append(created, addDelivery(s, e.ID, sub.ID, true, now))
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	r.signal()

	return created, nil
}

// replayed selects the events a replay delivers, oldest first.
func replayed(s *State, sub Subscription, req ReplayRequest) ([]Event, error) {
	var (
		events []Event
		err    error
	)
	if len(req.EventIDs) > 0 {
		events, err = byIDs(s, sub, req.EventIDs)
	} else {
		events, err = inPeriod(s, sub, req.Since, req.Until)
	}
	if err != nil {
		return nil, err
	}

	slices.SortFunc(events, func(a, b Event) int {
		return cmp.Or(a.AcceptedAt.Compare(b.AcceptedAt), cmp.Compare(a.ID, b.ID))
	})

	return events, nil
}

// byIDs selects named events, each of which must be retained and match.
func byIDs(s *State, sub Subscription, ids []string) ([]Event, error) {
	var events []Event
	for _, id := range ids {
		e, ok := s.Events[id]
		if !ok {
			return nil, fmt.Errorf("%w: event %q", ErrNotFound, id)
		}
		if !sub.Filter.Match(e) {
			return nil, fmt.Errorf("%w: event %q does not match the subscription", ErrInvalid, id)
		}
		if !slices.ContainsFunc(events, func(x Event) bool { return x.ID == id }) {
			events = append(events, e)
		}
	}

	return events, nil
}

// inPeriod selects matching events accepted in [since, until), which must
// lie within retained history. A zero until is open.
func inPeriod(s *State, sub Subscription, since, until time.Time) ([]Event, error) {
	if since.Before(s.RetainedSince) {
		return nil, ErrOutOfRange
	}
	var events []Event
	for _, e := range s.Events {
		if !e.AcceptedAt.Before(since) && (until.IsZero() || e.AcceptedAt.Before(until)) && sub.Filter.Match(e) {
			events = append(events, e)
		}
	}
	if len(events) > MaxReplay {
		return nil, fmt.Errorf("%w: more than %d events", ErrInvalid, MaxReplay)
	}

	return events, nil
}
