package main

import (
	"context"

	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	pb "github.com/r59q/forge-tools/forge-reactor/service/reactorpb"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// eventBackendError maps reactor status codes to management errors.
func eventBackendError(err error) error {
	switch status.Code(err) {
	case codes.InvalidArgument:
		return management.ErrInvalid
	case codes.NotFound:
		return management.ErrNotFound
	default:
		return management.ErrUnavailable
	}
}

// Events lists retained events, newest first.
func (r *remoteReactor) Events(ctx context.Context, q reactor.EventQuery) ([]reactor.Event, string, error) {
	ctx, err := r.context(ctx)
	if err != nil {
		return nil, "", err
	}

	v, err := r.client.ListEvents(ctx, &pb.ListEventsRequest{
		Types:        q.Filter.Types,
		Repositories: q.Filter.Repositories,
		Sources:      q.Filter.Sources,
		PageSize:     clamp32(q.Limit),
		PageToken:    q.PageToken,
	})
	if err != nil {
		return nil, "", eventBackendError(err)
	}

	events := make([]reactor.Event, 0, len(v.GetEvents()))
	for _, e := range v.GetEvents() {
		out, err := pb.ToEvent(e)
		if err != nil {
			return nil, "", management.ErrUnavailable
		}
		events = append(events, out)
	}

	return events, v.GetNextPageToken(), nil
}

// Event reads one event and its deliveries.
func (r *remoteReactor) Event(ctx context.Context, id string) (reactor.Event, []reactor.Delivery, error) {
	ctx, err := r.context(ctx)
	if err != nil {
		return reactor.Event{}, nil, err
	}

	v, err := r.client.GetEvent(ctx, &pb.GetEventRequest{Id: id})
	if err != nil {
		return reactor.Event{}, nil, eventBackendError(err)
	}
	e, err := pb.ToEvent(v.GetEvent())
	if err != nil {
		return reactor.Event{}, nil, management.ErrUnavailable
	}

	return e, pb.ToDeliveries(v.GetDeliveries()), nil
}
