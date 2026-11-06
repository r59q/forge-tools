package reactor

import (
	"cmp"
	"context"
	"slices"
	"time"
)

// CreateSubscription stores a new subscription. Only events accepted after
// creation are delivered to it; replay delivers earlier retained events.
// Reusing the ID of a deleted subscription fails with ErrBusy until its
// queue has been removed.
func (r *Reactor) CreateSubscription(ctx context.Context, sub Subscription) (Subscription, error) {
	if !ValidID(sub.ID) || !validText(sub.Description, 512) || sub.Filter.validate() != nil {
		return Subscription{}, ErrInvalid
	}

	err := r.cfg.Store.Update(ctx, func(s *State) error {
		if _, ok := s.Subscriptions[sub.ID]; ok {
			return ErrExists
		}
		if slices.Contains(s.Removals, sub.ID) {
			// The old queue is still being removed; reusing the ID now
			// would delete the new subscription's queue.
			return ErrBusy
		}
		sub.CreatedAt = r.cfg.Now()
		s.Subscriptions[sub.ID] = sub

		return nil
	})
	if err != nil {
		return Subscription{}, err
	}
	r.signal()

	return sub, nil
}

// Subscription returns one subscription.
func (r *Reactor) Subscription(ctx context.Context, id string) (Subscription, error) {
	s, err := r.cfg.Store.Read(ctx)
	if err != nil {
		return Subscription{}, err
	}
	sub, ok := s.Subscriptions[id]
	if !ok {
		return Subscription{}, ErrNotFound
	}

	return sub, nil
}

// Subscriptions returns every subscription in ID order.
func (r *Reactor) Subscriptions(ctx context.Context) ([]Subscription, error) {
	s, err := r.cfg.Store.Read(ctx)
	if err != nil {
		return nil, err
	}

	subs := make([]Subscription, 0, len(s.Subscriptions))
	for _, sub := range s.Subscriptions {
		subs = append(subs, sub)
	}
	slices.SortFunc(subs, func(a, b Subscription) int { return cmp.Compare(a.ID, b.ID) })

	return subs, nil
}

// DeleteSubscription removes a subscription, cancels its pending
// deliveries and schedules removal of its broker queue, including any
// messages still queued. Its delivery history stays until it expires.
func (r *Reactor) DeleteSubscription(ctx context.Context, id string) error {
	err := r.cfg.Store.Update(ctx, func(s *State) error {
		if _, ok := s.Subscriptions[id]; !ok {
			return ErrNotFound
		}
		delete(s.Subscriptions, id)
		for did, d := range s.Deliveries {
			if d.Subscription == id && d.Status == StatusPending {
				d.Status, d.NextAttemptAt = StatusCancelled, time.Time{}
				s.Deliveries[did] = d
			}
		}
		if !slices.Contains(s.Removals, id) {
			s.Removals = append(s.Removals, id)
		}

		return nil
	})
	if err != nil {
		return err
	}
	r.signal()

	return nil
}
