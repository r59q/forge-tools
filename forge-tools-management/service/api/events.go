package api

import (
	"context"

	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	rpb "github.com/r59q/forge-tools/forge-reactor/service/reactorpb"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
)

// ListEvents returns one page of events, without native payloads.
func (s *Server) ListEvents(ctx context.Context, r *pb.ListEventsRequest) (*pb.ListEventsResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ListEventsResponse, error) {
		page, err := s.client.ListEvents(ctx, management.EventQuery{
			Source:     r.GetSource(),
			Type:       r.GetType(),
			Repository: r.GetRepository(),
			PageToken:  r.GetPageToken(),
		})
		if err != nil {
			return nil, err
		}

		out := &pb.ListEventsResponse{Configured: page.Configured, NextPageToken: page.NextPageToken}
		for _, e := range page.Events {
			if e.Native != nil {
				e.Native = &reactor.Native{Event: e.Native.Event, Delivery: e.Native.Delivery}
			}
			pe, err := rpb.FromEvent(e)
			if err != nil {
				return nil, management.ErrUnavailable
			}
			out.Events = append(out.Events, pe)
		}

		return out, nil
	})
}

// GetEvent returns one event and its deliveries.
func (s *Server) GetEvent(ctx context.Context, r *pb.GetEventRequest) (*pb.GetEventResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.GetEventResponse, error) {
		e, deliveries, err := s.client.GetEvent(ctx, r.GetId())
		if err != nil {
			return nil, err
		}
		pe, err := rpb.FromEvent(e)
		if err != nil {
			return nil, management.ErrUnavailable
		}

		return &pb.GetEventResponse{Event: pe, Deliveries: rpb.FromDeliveries(deliveries)}, nil
	})
}
