package reactor

import (
	"slices"
	"strings"
	"time"
)

// maxFilterValues caps each filter list.
const maxFilterValues = 64

// Filter selects events. Each non-empty list must contain the event's value;
// an empty list matches any value. An event without a connection or
// repository never matches a non-empty list for that field.
type Filter struct {
	// Types are event types.
	Types []string `json:"types,omitempty"`
	// Connections are forge-connector connection IDs.
	Connections []string `json:"connections,omitempty"`
	// Repositories are owner/name paths, compared without case.
	Repositories []string `json:"repositories,omitempty"`
	// Origins are event origins.
	Origins []Origin `json:"origins,omitempty"`
}

// Subscription is a durable consumer of matching events. Each subscription
// has its own broker queue and its own delivery for every matching event.
type Subscription struct {
	// ID names the subscription and its queue.
	ID string `json:"id"`
	// Description is an optional note for operators.
	Description string `json:"description,omitempty"`
	// Filter selects events.
	Filter Filter `json:"filter"`
	// CreatedAt is when the subscription was created.
	CreatedAt time.Time `json:"createdAt"`
}

// Match reports whether f selects e.
func (f Filter) Match(e Event) bool {
	return (len(f.Types) == 0 || slices.Contains(f.Types, e.Type)) &&
		(len(f.Connections) == 0 || (e.Connection != "" && slices.Contains(f.Connections, e.Connection))) &&
		(len(f.Repositories) == 0 || (e.Repository != "" && slices.ContainsFunc(f.Repositories, func(r string) bool {
			return strings.EqualFold(r, e.Repository)
		}))) &&
		(len(f.Origins) == 0 || slices.Contains(f.Origins, e.Origin))
}

// validate checks f's values.
func (f Filter) validate() error {
	if len(f.Types) > maxFilterValues || len(f.Connections) > maxFilterValues ||
		len(f.Repositories) > maxFilterValues || len(f.Origins) > maxFilterValues {
		return ErrInvalid
	}
	for _, t := range f.Types {
		if !ValidType(t) {
			return ErrInvalid
		}
	}
	for _, v := range slices.Concat(f.Connections, f.Repositories) {
		if v == "" || !validText(v, 512) {
			return ErrInvalid
		}
	}
	for _, o := range f.Origins {
		if o != OriginWebhook && o != OriginPoll && o != OriginPublished {
			return ErrInvalid
		}
	}

	return nil
}
