package reactor

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"time"
)

// Prune drops expired events, their delivery history and expired dedup
// keys. Accepting, observing and dispatching prune as they go; Prune lets
// an idle service do the same.
func (r *Reactor) Prune(ctx context.Context) error {
	return r.cfg.Store.Update(ctx, func(s *State) error {
		r.prune(s, r.cfg.Now())

		return nil
	})
}

// prune applies the retention limits. Events with pending deliveries stay
// until those deliveries finish.
func (r *Reactor) prune(s *State, now time.Time) {
	for key, seen := range s.Seen {
		if now.Sub(seen.At) > r.cfg.DedupWindow {
			delete(s.Seen, key)
		}
	}

	busy := map[string]bool{}
	for _, d := range s.Deliveries {
		if d.Status == StatusPending {
			busy[d.EventID] = true
		}
	}

	events := make([]Event, 0, len(s.Events))
	for _, e := range s.Events {
		events = append(events, e)
	}
	slices.SortFunc(events, func(a, b Event) int {
		return cmp.Or(a.AcceptedAt.Compare(b.AcceptedAt), cmp.Compare(a.ID, b.ID))
	})

	dropped := map[string]bool{}
	excess := len(events) - r.cfg.MaxEvents
	for i, e := range events {
		if busy[e.ID] || (i >= excess && now.Sub(e.AcceptedAt) <= r.cfg.Retention) {
			continue
		}
		dropped[e.ID] = true
		delete(s.Events, e.ID)
		if since := e.AcceptedAt.Add(time.Nanosecond); since.After(s.RetainedSince) {
			s.RetainedSince = since
		}
	}

	for id, d := range s.Deliveries {
		if dropped[d.EventID] {
			delete(s.Deliveries, id)
		}
	}
}

// mustJSON encodes a value that always encodes.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}

	return b
}
