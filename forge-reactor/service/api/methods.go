package api

import (
	"context"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	pb "github.com/r59q/forge-tools/forge-reactor/service/reactorpb"
)

// PublishEvent stores a published event and returns it once it is durable.
func (s *Server) PublishEvent(ctx context.Context, req *pb.PublishEventRequest) (*pb.PublishEventResponse, error) {
	return call(s, ctx, func(ctx context.Context, r *reactor.Reactor) (*pb.PublishEventResponse, error) {
		forge := forgeauth.Kind(req.GetForge())
		if forge != "" && forge != forgeauth.GitHub && forge != forgeauth.Forgejo {
			return nil, reactor.ErrInvalid
		}
		data, err := pb.JSONFromStruct(req.GetData())
		if err != nil {
			return nil, err
		}

		e, duplicate, err := r.Publish(ctx, reactor.Event{
			Type:       req.GetType(),
			Forge:      forge,
			Connection: req.GetConnection(),
			Repository: req.GetRepository(),
			Subject:    req.GetSubject(),
			OccurredAt: fromTime(req.GetOccurredAt()),
			Data:       data,
		}, req.GetIdempotencyKey())
		if err != nil {
			return nil, err
		}
		out, err := pb.FromEvent(e)
		if err != nil {
			return nil, err
		}

		return &pb.PublishEventResponse{Event: out, Duplicate: duplicate}, nil
	})
}

// GetEvent reads a retained event and its deliveries.
func (s *Server) GetEvent(ctx context.Context, req *pb.GetEventRequest) (*pb.GetEventResponse, error) {
	return call(s, ctx, func(ctx context.Context, r *reactor.Reactor) (*pb.GetEventResponse, error) {
		e, deliveries, err := r.Event(ctx, req.GetId())
		if err != nil {
			return nil, err
		}
		out, err := pb.FromEvent(e)
		if err != nil {
			return nil, err
		}

		return &pb.GetEventResponse{Event: out, Deliveries: pb.FromDeliveries(deliveries)}, nil
	})
}

// ListEvents lists retained events, newest first.
func (s *Server) ListEvents(ctx context.Context, req *pb.ListEventsRequest) (*pb.ListEventsResponse, error) {
	return call(s, ctx, func(ctx context.Context, r *reactor.Reactor) (*pb.ListEventsResponse, error) {
		events, next, err := r.Events(ctx, reactor.EventQuery{
			Filter: fromFilter(&pb.Filter{
				Types:        req.GetTypes(),
				Connections:  req.GetConnections(),
				Repositories: req.GetRepositories(),
				Origins:      req.GetOrigins(),
				Sources:      req.GetSources(),
			}),
			Limit:     int(req.GetPageSize()),
			PageToken: req.GetPageToken(),
		})
		if err != nil {
			return nil, err
		}

		out := &pb.ListEventsResponse{NextPageToken: next}
		for _, e := range events {
			pe, err := pb.FromEvent(e)
			if err != nil {
				return nil, err
			}
			out.Events = append(out.Events, pe)
		}

		return out, nil
	})
}

// CreateSubscription creates a durable subscription.
func (s *Server) CreateSubscription(ctx context.Context, req *pb.CreateSubscriptionRequest) (*pb.CreateSubscriptionResponse, error) {
	return call(s, ctx, func(ctx context.Context, r *reactor.Reactor) (*pb.CreateSubscriptionResponse, error) {
		in := req.GetSubscription()
		sub, err := r.CreateSubscription(ctx, reactor.Subscription{
			ID:          in.GetId(),
			Description: in.GetDescription(),
			Filter:      fromFilter(in.GetFilter()),
		})
		if err != nil {
			return nil, err
		}

		return &pb.CreateSubscriptionResponse{Subscription: toSubscription(sub)}, nil
	})
}

// GetSubscription reads a subscription.
func (s *Server) GetSubscription(ctx context.Context, req *pb.GetSubscriptionRequest) (*pb.GetSubscriptionResponse, error) {
	return call(s, ctx, func(ctx context.Context, r *reactor.Reactor) (*pb.GetSubscriptionResponse, error) {
		sub, err := r.Subscription(ctx, req.GetId())
		if err != nil {
			return nil, err
		}

		return &pb.GetSubscriptionResponse{Subscription: toSubscription(sub)}, nil
	})
}

// ListSubscriptions lists every subscription by ID.
func (s *Server) ListSubscriptions(ctx context.Context, _ *pb.ListSubscriptionsRequest) (*pb.ListSubscriptionsResponse, error) {
	return call(s, ctx, func(ctx context.Context, r *reactor.Reactor) (*pb.ListSubscriptionsResponse, error) {
		subs, err := r.Subscriptions(ctx)
		if err != nil {
			return nil, err
		}

		out := &pb.ListSubscriptionsResponse{}
		for _, sub := range subs {
			out.Subscriptions = append(out.Subscriptions, toSubscription(sub))
		}

		return out, nil
	})
}

// DeleteSubscription deletes a subscription and, through the dispatcher,
// its queue.
func (s *Server) DeleteSubscription(ctx context.Context, req *pb.DeleteSubscriptionRequest) (*pb.DeleteSubscriptionResponse, error) {
	return call(s, ctx, func(ctx context.Context, r *reactor.Reactor) (*pb.DeleteSubscriptionResponse, error) {
		if err := r.DeleteSubscription(ctx, req.GetId()); err != nil {
			return nil, err
		}

		return &pb.DeleteSubscriptionResponse{}, nil
	})
}

// ListDeliveries lists delivery history, newest first.
func (s *Server) ListDeliveries(ctx context.Context, req *pb.ListDeliveriesRequest) (*pb.ListDeliveriesResponse, error) {
	return call(s, ctx, func(ctx context.Context, r *reactor.Reactor) (*pb.ListDeliveriesResponse, error) {
		deliveries, next, err := r.Deliveries(ctx, reactor.DeliveryQuery{
			Subscription: req.GetSubscription(),
			EventID:      req.GetEventId(),
			Status:       reactor.Status(req.GetStatus()),
			Limit:        int(req.GetPageSize()),
			PageToken:    req.GetPageToken(),
		})
		if err != nil {
			return nil, err
		}

		return &pb.ListDeliveriesResponse{Deliveries: pb.FromDeliveries(deliveries), NextPageToken: next}, nil
	})
}

// ReplayEvents delivers retained events to one subscription again.
func (s *Server) ReplayEvents(ctx context.Context, req *pb.ReplayEventsRequest) (*pb.ReplayEventsResponse, error) {
	return call(s, ctx, func(ctx context.Context, r *reactor.Reactor) (*pb.ReplayEventsResponse, error) {
		deliveries, err := r.Replay(ctx, reactor.ReplayRequest{
			Subscription: req.GetSubscription(),
			EventIDs:     req.GetEventIds(),
			Since:        fromTime(req.GetSince()),
			Until:        fromTime(req.GetUntil()),
		})
		if err != nil {
			return nil, err
		}

		return &pb.ReplayEventsResponse{Deliveries: pb.FromDeliveries(deliveries)}, nil
	})
}
