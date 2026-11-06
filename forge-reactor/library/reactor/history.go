package reactor

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Page sizes for listing history.
const (
	DefaultPageSize = 50
	MaxPageSize     = 500
)

// EventQuery selects retained events, newest first.
type EventQuery struct {
	// Filter selects events, as a subscription would.
	Filter Filter
	// Limit caps the page; zero uses DefaultPageSize.
	Limit int
	// PageToken continues an earlier listing.
	PageToken string
}

// DeliveryQuery selects deliveries, newest first. Empty fields match all.
type DeliveryQuery struct {
	// Subscription is the receiving subscription.
	Subscription string
	// EventID is the delivered event.
	EventID string
	// Status is the delivery status.
	Status Status
	// Limit caps the page; zero uses DefaultPageSize.
	Limit int
	// PageToken continues an earlier listing.
	PageToken string
}

// Events lists retained events and a token for the next page, empty at the
// end.
func (r *Reactor) Events(ctx context.Context, q EventQuery) ([]Event, string, error) {
	if q.Filter.validate() != nil {
		return nil, "", ErrInvalid
	}
	s, err := r.cfg.Store.Read(ctx)
	if err != nil {
		return nil, "", err
	}

	var events []Event
	for _, e := range s.Events {
		if q.Filter.Match(e) {
			events = append(events, e)
		}
	}

	return page(events, func(e Event) (time.Time, string) { return e.AcceptedAt, e.ID }, q.PageToken, q.Limit)
}

// Event returns a retained event with its deliveries, oldest first.
func (r *Reactor) Event(ctx context.Context, id string) (Event, []Delivery, error) {
	s, err := r.cfg.Store.Read(ctx)
	if err != nil {
		return Event{}, nil, err
	}
	e, ok := s.Events[id]
	if !ok {
		return Event{}, nil, ErrNotFound
	}

	deliveries := []Delivery{}
	for _, d := range s.Deliveries {
		if d.EventID == id {
			deliveries = append(deliveries, d)
		}
	}
	slices.SortFunc(deliveries, func(a, b Delivery) int {
		return cmp.Or(cmp.Compare(a.Sequence, b.Sequence), cmp.Compare(a.ID, b.ID))
	})

	return e, deliveries, nil
}

// Deliveries lists deliveries with their attempts and a token for the next
// page, empty at the end.
func (r *Reactor) Deliveries(ctx context.Context, q DeliveryQuery) ([]Delivery, string, error) {
	s, err := r.cfg.Store.Read(ctx)
	if err != nil {
		return nil, "", err
	}

	var deliveries []Delivery
	for _, d := range s.Deliveries {
		if (q.Subscription == "" || d.Subscription == q.Subscription) &&
			(q.EventID == "" || d.EventID == q.EventID) && (q.Status == "" || d.Status == q.Status) {
			deliveries = append(deliveries, d)
		}
	}

	return page(deliveries, func(d Delivery) (time.Time, string) { return d.CreatedAt, d.ID }, q.PageToken, q.Limit)
}

// page sorts items newest first and returns those after token.
func page[T any](items []T, key func(T) (time.Time, string), token string, limit int) ([]T, string, error) {
	if limit < 0 || limit > MaxPageSize {
		return nil, "", ErrInvalid
	}
	limit = cmp.Or(limit, DefaultPageSize)

	compare := func(at time.Time, id string, bt time.Time, bid string) int {
		return cmp.Or(bt.Compare(at), cmp.Compare(bid, id))
	}
	slices.SortFunc(items, func(a, b T) int {
		at, aid := key(a)
		bt, bid := key(b)

		return compare(at, aid, bt, bid)
	})

	start := 0
	if token != "" {
		tt, tid, err := parseToken(token)
		if err != nil {
			return nil, "", err
		}
		start, _ = slices.BinarySearchFunc(items, struct{}{}, func(item T, _ struct{}) int {
			at, aid := key(item)
			if compare(at, aid, tt, tid) <= 0 {
				return -1
			}

			return 1
		})
	}

	end := min(start+limit, len(items))
	out := slices.Clone(items[start:end])
	next := ""
	if end < len(items) {
		at, id := key(items[end-1])
		next = strconv.FormatInt(at.UnixNano(), 10) + "." + id
	}

	return out, next, nil
}

// parseToken reads a page token.
func parseToken(token string) (time.Time, string, error) {
	nanos, id, ok := strings.Cut(token, ".")
	n, err := strconv.ParseInt(nanos, 10, 64)
	if !ok || err != nil || id == "" {
		return time.Time{}, "", ErrInvalid
	}

	return time.Unix(0, n), id, nil
}
