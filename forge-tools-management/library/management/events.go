package management

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

// EventPageSize is the number of events ListEvents returns per page.
const EventPageSize = 50

// EventReader reads forge-reactor's event history. *reactor.Reactor
// implements it, as do adapters for the reactor API.
type EventReader interface {
	Events(context.Context, reactor.EventQuery) ([]reactor.Event, string, error)
	Event(ctx context.Context, id string) (reactor.Event, []reactor.Delivery, error)
}

// EventQuery selects events, newest first. Empty fields match all.
type EventQuery struct {
	Source     string
	Type       string
	Repository string
	PageToken  string
}

// EventPage distinguishes a missing capability from an empty page.
type EventPage struct {
	Configured bool
	Events     []reactor.Event
	// NextPageToken continues the listing; empty at the end.
	NextPageToken string
}

// EventsConfigured reports whether event history is configured.
func (c *Client) EventsConfigured() bool { return c.events != nil }

// ListEvents reads one page of events. Every event must match the query.
func (c *Client) ListEvents(ctx context.Context, q EventQuery) (EventPage, error) {
	return c.listEvents(ctx, q, EventPageSize)
}

// LatestEvent returns a source's newest retained event, if any.
func (c *Client) LatestEvent(ctx context.Context, source string) (reactor.Event, bool, error) {
	if source == "" {
		return reactor.Event{}, false, ErrInvalid
	}

	page, err := c.listEvents(ctx, EventQuery{Source: source}, 1)
	if err != nil || len(page.Events) == 0 {
		return reactor.Event{}, false, err
	}

	return page.Events[0], true, nil
}

// GetEvent reads one event and its deliveries to subscriptions.
func (c *Client) GetEvent(ctx context.Context, id string) (reactor.Event, []reactor.Delivery, error) {
	if err := ctx.Err(); err != nil {
		return reactor.Event{}, nil, err
	}
	if c.events == nil {
		return reactor.Event{}, nil, ErrNotConfigured
	}
	if id == "" {
		return reactor.Event{}, nil, ErrInvalid
	}

	e, deliveries, err := c.events.Event(ctx, id)
	if err != nil {
		return reactor.Event{}, nil, eventError(ctx, err)
	}
	if e.ID != id || slices.ContainsFunc(deliveries, func(d reactor.Delivery) bool { return d.EventID != id }) {
		return reactor.Event{}, nil, ErrUnavailable
	}

	return e, deliveries, nil
}

func (c *Client) listEvents(ctx context.Context, q EventQuery, limit int) (EventPage, error) {
	if err := ctx.Err(); err != nil {
		return EventPage{}, err
	}
	if c.events == nil {
		return EventPage{Events: []reactor.Event{}}, nil
	}

	filter := q.filter()
	events, next, err := c.events.Events(ctx, reactor.EventQuery{Filter: filter, Limit: limit, PageToken: q.PageToken})
	if err != nil {
		return EventPage{}, eventError(ctx, err)
	}
	if len(events) > limit || slices.ContainsFunc(events, func(e reactor.Event) bool { return e.ID == "" || !filter.Match(e) }) {
		return EventPage{}, ErrUnavailable
	}

	return EventPage{Configured: true, Events: events, NextPageToken: next}, nil
}

// filter turns the query into a reactor filter.
func (q EventQuery) filter() reactor.Filter {
	var f reactor.Filter
	if s := strings.TrimSpace(q.Source); s != "" {
		f.Sources = []string{s}
	}
	if t := strings.TrimSpace(q.Type); t != "" {
		f.Types = []string{t}
	}
	if r := strings.TrimSpace(q.Repository); r != "" {
		f.Repositories = []string{r}
	}

	return f
}

// eventError keeps invalid queries and missing events, and hides the rest.
func eventError(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, reactor.ErrInvalid), errors.Is(err, ErrInvalid):
		return ErrInvalid
	case errors.Is(err, reactor.ErrNotFound), errors.Is(err, ErrNotFound):
		return ErrNotFound
	default:
		return ErrUnavailable
	}
}
