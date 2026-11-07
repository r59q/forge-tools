package reactorpb

import (
	"encoding/json"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// FromEvent converts a library event. Its data and payload must be JSON
// objects; an error means stored state is corrupt.
func FromEvent(e reactor.Event) (*Event, error) {
	data, err := StructFromJSON(e.Data)
	if err != nil {
		return nil, err
	}

	out := &Event{
		Id:         e.ID,
		Type:       e.Type,
		Origin:     string(e.Origin),
		Source:     e.Source,
		Forge:      string(e.Forge),
		Connection: e.Connection,
		Repository: e.Repository,
		Subject:    e.Subject,
		OccurredAt: timestamp(e.OccurredAt),
		AcceptedAt: timestamp(e.AcceptedAt),
		Data:       data,
	}
	if e.Native != nil {
		payload, err := StructFromJSON(e.Native.Payload)
		if err != nil {
			return nil, err
		}
		out.Native = &Native{Event: e.Native.Event, Delivery: e.Native.Delivery, Payload: payload}
	}

	return out, nil
}

// ToEvent converts an event to the library type.
func ToEvent(e *Event) (reactor.Event, error) {
	data, err := JSONFromStruct(e.GetData())
	if err != nil {
		return reactor.Event{}, err
	}

	out := reactor.Event{
		ID:         e.GetId(),
		Type:       e.GetType(),
		Origin:     reactor.Origin(e.GetOrigin()),
		Source:     e.GetSource(),
		Forge:      forgeauth.Kind(e.GetForge()),
		Connection: e.GetConnection(),
		Repository: e.GetRepository(),
		Subject:    e.GetSubject(),
		OccurredAt: when(e.GetOccurredAt()),
		AcceptedAt: when(e.GetAcceptedAt()),
		Data:       data,
	}
	if n := e.GetNative(); n != nil {
		payload, err := JSONFromStruct(n.GetPayload())
		if err != nil {
			return reactor.Event{}, err
		}
		out.Native = &reactor.Native{Event: n.GetEvent(), Delivery: n.GetDelivery(), Payload: payload}
	}

	return out, nil
}

// FromDeliveries converts library deliveries.
func FromDeliveries(ds []reactor.Delivery) []*Delivery {
	out := make([]*Delivery, 0, len(ds))
	for _, d := range ds {
		pd := &Delivery{
			Id:            d.ID,
			EventId:       d.EventID,
			Subscription:  d.Subscription,
			Replay:        d.Replay,
			Status:        string(d.Status),
			CreatedAt:     timestamp(d.CreatedAt),
			NextAttemptAt: timestamp(d.NextAttemptAt),
			AcceptedAt:    timestamp(d.AcceptedAt),
		}
		for _, a := range d.Attempts {
			pd.Attempts = append(pd.Attempts, &Attempt{At: timestamp(a.At), Error: a.Error})
		}
		out = append(out, pd)
	}

	return out
}

// ToDeliveries converts deliveries to the library type.
func ToDeliveries(ds []*Delivery) []reactor.Delivery {
	out := make([]reactor.Delivery, 0, len(ds))
	for _, d := range ds {
		rd := reactor.Delivery{
			ID:            d.GetId(),
			EventID:       d.GetEventId(),
			Subscription:  d.GetSubscription(),
			Replay:        d.GetReplay(),
			Status:        reactor.Status(d.GetStatus()),
			CreatedAt:     when(d.GetCreatedAt()),
			NextAttemptAt: when(d.GetNextAttemptAt()),
			AcceptedAt:    when(d.GetAcceptedAt()),
		}
		for _, a := range d.GetAttempts() {
			rd.Attempts = append(rd.Attempts, reactor.Attempt{At: when(a.GetAt()), Error: a.GetError()})
		}
		out = append(out, rd)
	}

	return out
}

// StructFromJSON converts a JSON object; empty data is nil.
func StructFromJSON(data json.RawMessage) (*structpb.Struct, error) {
	if len(data) == 0 {
		return nil, nil
	}
	s := &structpb.Struct{}
	if err := protojson.Unmarshal(data, s); err != nil {
		return nil, err
	}

	return s, nil
}

// JSONFromStruct encodes a Struct as a JSON object; nil is empty.
func JSONFromStruct(s *structpb.Struct) (json.RawMessage, error) {
	if s == nil {
		return nil, nil
	}
	b, err := protojson.Marshal(s)
	if err != nil {
		return nil, reactor.ErrInvalid
	}

	return b, nil
}

// timestamp converts a time; the zero time is nil.
func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}

// when converts a timestamp; nil is the zero time.
func when(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}

	return t.AsTime()
}
