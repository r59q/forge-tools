package reactor

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Status is where a delivery stands. Statuses describe the hand-off to the
// broker only; consumers acknowledge to the broker, never to the reactor.
type Status string

const (
	// StatusPending waits for a broker hand-off attempt.
	StatusPending Status = "pending"
	// StatusAccepted means the broker confirmed it took the message. It
	// says nothing about whether a consumer has processed it.
	StatusAccepted Status = "broker_accepted"
	// StatusFailed means every hand-off attempt failed. A replay can try
	// again with a new delivery.
	StatusFailed Status = "failed"
	// StatusCancelled means the subscription was deleted first.
	StatusCancelled Status = "cancelled"
)

// Attempt is one hand-off to the broker.
type Attempt struct {
	// At is when the attempt finished.
	At time.Time `json:"at"`
	// Error is why the broker did not confirm; empty means it confirmed.
	Error string `json:"error,omitempty"`
}

// Delivery is one event for one subscription.
type Delivery struct {
	// ID identifies this delivery; a replay creates a new one.
	ID string `json:"id"`
	// EventID is the delivered event.
	EventID string `json:"eventId"`
	// Subscription is the receiving subscription.
	Subscription string `json:"subscription"`
	// Sequence orders deliveries by creation within one store.
	Sequence uint64 `json:"sequence"`
	// Replay reports a delivery requested by a replay.
	Replay bool `json:"replay,omitempty"`
	// Status is where the delivery stands.
	Status Status `json:"status"`
	// Attempts records every hand-off attempt, oldest first.
	Attempts []Attempt `json:"attempts,omitempty"`
	// CreatedAt is when the delivery was created.
	CreatedAt time.Time `json:"createdAt"`
	// NextAttemptAt is when a pending delivery is next due.
	NextAttemptAt time.Time `json:"nextAttemptAt,omitzero"`
	// AcceptedAt is when the broker confirmed the message.
	AcceptedAt time.Time `json:"acceptedAt,omitzero"`
}

// Seen records an accepted Input key for deduplication.
type Seen struct {
	// EventID is the event the key produced.
	EventID string `json:"eventId"`
	// At is when the key was first accepted.
	At time.Time `json:"at"`
}

// Baseline is the last commit a poll target observed.
type Baseline struct {
	// Commit is the observed head.
	Commit string `json:"commit"`
	// ObservedAt is when it was first observed.
	ObservedAt time.Time `json:"observedAt"`
}

// State is everything the reactor keeps. Stores persist it as one unit, so
// an event, its deliveries and its dedup or baseline record commit together.
type State struct {
	// Events are retained events by ID.
	Events map[string]Event `json:"events"`
	// Deliveries are deliveries of retained events by ID.
	Deliveries map[string]Delivery `json:"deliveries"`
	// Subscriptions are subscriptions by ID.
	Subscriptions map[string]Subscription `json:"subscriptions"`
	// Seen are Input keys within the dedup window.
	Seen map[string]Seen `json:"seen"`
	// Baselines are poll observations by target key.
	Baselines map[string]Baseline `json:"baselines"`
	// Removals are subscription IDs whose broker queues must be removed.
	Removals []string `json:"removals,omitempty"`
	// Sequence is the last delivery sequence number used.
	Sequence uint64 `json:"sequence"`
	// RetainedSince is the earliest acceptance time history is complete
	// from; replays of older periods fail.
	RetainedSince time.Time `json:"retainedSince,omitzero"`
}

// Store keeps State. Update must commit fn's changes durably, all or
// nothing, before it returns nil.
type Store interface {
	// Read returns an isolated copy of the state.
	Read(ctx context.Context) (State, error)
	// Update applies fn to a copy and commits it only if fn succeeds.
	Update(ctx context.Context, fn func(*State) error) error
}

// JSONStore keeps the state encoded as JSON in memory and gives each new
// encoding to Save before it becomes visible. Its zero value is an empty
// process-local store; services set Save to write durable storage.
type JSONStore struct {
	// Save persists an encoding. A nil Save keeps state in memory only.
	Save func(ctx context.Context, data []byte) error

	mu   sync.Mutex
	data []byte
}

// NewJSONStore returns a store starting from data, an encoding written by
// an earlier Save; empty data starts empty.
func NewJSONStore(data []byte, save func(context.Context, []byte) error) (*JSONStore, error) {
	if _, err := decodeState(data); err != nil {
		return nil, err
	}

	return &JSONStore{Save: save, data: data}, nil
}

// Read returns an isolated copy of the current state.
func (j *JSONStore) Read(ctx context.Context) (State, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return State{}, err
	}

	return decodeState(j.data)
}

// Update commits fn's changes only if fn and Save succeed.
func (j *JSONStore) Update(ctx context.Context, fn func(*State) error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	s, err := decodeState(j.data)
	if err != nil {
		return err
	}
	if err := fn(&s); err != nil {
		return err
	}

	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if j.Save != nil {
		if err := j.Save(ctx, data); err != nil {
			return err
		}
	}
	j.data = data

	return nil
}

// decodeState decodes data, filling in empty maps.
func decodeState(data []byte) (State, error) {
	var s State
	if len(data) > 0 {
		if err := json.Unmarshal(data, &s); err != nil {
			return State{}, err
		}
	}
	if s.Events == nil {
		s.Events = map[string]Event{}
	}
	if s.Deliveries == nil {
		s.Deliveries = map[string]Delivery{}
	}
	if s.Subscriptions == nil {
		s.Subscriptions = map[string]Subscription{}
	}
	if s.Seen == nil {
		s.Seen = map[string]Seen{}
	}
	if s.Baselines == nil {
		s.Baselines = map[string]Baseline{}
	}

	return s, nil
}
