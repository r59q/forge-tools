package api

import (
	"encoding/json"
	"time"

	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	pb "github.com/r59q/forge-tools/forge-reactor/service/reactorpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// toEvent converts an event. Its data and payload are JSON objects, so a
// conversion failure means stored state is corrupt.
func toEvent(e reactor.Event) (*pb.Event, error) {
	data, err := toStruct(e.Data)
	if err != nil {
		return nil, err
	}

	out := &pb.Event{
		Id:         e.ID,
		Type:       e.Type,
		Origin:     string(e.Origin),
		Source:     e.Source,
		Forge:      string(e.Forge),
		Connection: e.Connection,
		Repository: e.Repository,
		Subject:    e.Subject,
		OccurredAt: toTime(e.OccurredAt),
		AcceptedAt: toTime(e.AcceptedAt),
		Data:       data,
	}
	if e.Native != nil {
		payload, err := toStruct(e.Native.Payload)
		if err != nil {
			return nil, err
		}
		out.Native = &pb.Native{Event: e.Native.Event, Delivery: e.Native.Delivery, Payload: payload}
	}

	return out, nil
}

// toStruct converts a JSON object; empty data is nil.
func toStruct(data json.RawMessage) (*structpb.Struct, error) {
	if len(data) == 0 {
		return nil, nil
	}
	s := &structpb.Struct{}
	if err := protojson.Unmarshal(data, s); err != nil {
		return nil, err
	}

	return s, nil
}

// fromStruct encodes a Struct as a JSON object; nil is empty.
func fromStruct(s *structpb.Struct) (json.RawMessage, error) {
	if s == nil {
		return nil, nil
	}
	b, err := protojson.Marshal(s)
	if err != nil {
		return nil, reactor.ErrInvalid
	}

	return b, nil
}

func toTime(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}

func fromTime(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}

	return t.AsTime()
}

func fromFilter(f *pb.Filter) reactor.Filter {
	out := reactor.Filter{Types: f.GetTypes(), Connections: f.GetConnections(), Repositories: f.GetRepositories()}
	for _, o := range f.GetOrigins() {
		out.Origins = append(out.Origins, reactor.Origin(o))
	}

	return out
}

func toFilter(f reactor.Filter) *pb.Filter {
	out := &pb.Filter{Types: f.Types, Connections: f.Connections, Repositories: f.Repositories}
	for _, o := range f.Origins {
		out.Origins = append(out.Origins, string(o))
	}

	return out
}

func toSubscription(s reactor.Subscription) *pb.Subscription {
	return &pb.Subscription{Id: s.ID, Description: s.Description, Filter: toFilter(s.Filter), CreatedAt: toTime(s.CreatedAt)}
}

func toDeliveries(ds []reactor.Delivery) []*pb.Delivery {
	out := make([]*pb.Delivery, 0, len(ds))
	for _, d := range ds {
		pd := &pb.Delivery{
			Id:            d.ID,
			EventId:       d.EventID,
			Subscription:  d.Subscription,
			Replay:        d.Replay,
			Status:        string(d.Status),
			CreatedAt:     toTime(d.CreatedAt),
			NextAttemptAt: toTime(d.NextAttemptAt),
			AcceptedAt:    toTime(d.AcceptedAt),
		}
		for _, a := range d.Attempts {
			pd.Attempts = append(pd.Attempts, &pb.Attempt{At: toTime(a.At), Error: a.Error})
		}
		out = append(out, pd)
	}

	return out
}
