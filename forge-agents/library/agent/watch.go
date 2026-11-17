package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/r59q/forge-tools/forge-agents/library/transcript"
)

// MaxPage is the most events one transcript page holds.
const MaxPage = 1000

// Transcript returns up to limit events after sequence number after.
func (m *Manager) Transcript(ctx context.Context, id string, after int64, limit int) ([]transcript.Event, error) {
	if after < 0 || limit < 0 {
		return nil, fmt.Errorf("%w: after and limit must not be negative", ErrInvalid)
	}
	if limit == 0 || limit > MaxPage {
		limit = MaxPage
	}
	return m.cfg.Store.Events(ctx, id, after, limit)
}

// Output returns the raw client output of a turn.
func (m *Manager) Output(ctx context.Context, id string, turn int) (Output, error) {
	if _, err := m.cfg.Store.Get(ctx, id); err != nil {
		return Output{}, err
	}
	o, err := m.cfg.Store.Output(ctx, id, turn)
	if errors.Is(err, ErrNotFound) {
		return Output{}, fmt.Errorf("%w: no output kept for turn %d", ErrNotFound, turn)
	}
	return o, err
}

// Watch sends the events after sequence number after, then each new
// event as it is recorded. It returns nil once the agent is stopped or
// failed and every event was sent, or when the agent is deleted; it
// returns ctx's error when ctx ends first.
func (m *Manager) Watch(ctx context.Context, id string, after int64, send func(transcript.Event) error) error {
	if _, err := m.cfg.Store.Get(ctx, id); err != nil {
		return err
	}
	for {
		changed := m.changed(id)
		events, err := m.Transcript(ctx, id, after, MaxPage)
		if errors.Is(err, ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		for _, e := range events {
			if err := send(e); err != nil {
				return err
			}
			after = e.Seq
		}
		if len(events) == MaxPage {
			continue
		}
		a, err := m.cfg.Store.Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if a.State.Terminal() {
			// Events recorded before the state changed may be newer than
			// the page just sent.
			if more, err := m.Transcript(ctx, id, after, 1); err == nil && len(more) == 0 {
				return nil
			}
			continue
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
