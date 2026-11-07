package api

import (
	"time"

	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	pb "github.com/r59q/forge-tools/forge-reactor/service/reactorpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

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
	out := reactor.Filter{Types: f.GetTypes(), Connections: f.GetConnections(), Repositories: f.GetRepositories(), Sources: f.GetSources()}
	for _, o := range f.GetOrigins() {
		out.Origins = append(out.Origins, reactor.Origin(o))
	}

	return out
}

func toFilter(f reactor.Filter) *pb.Filter {
	out := &pb.Filter{Types: f.Types, Connections: f.Connections, Repositories: f.Repositories, Sources: f.Sources}
	for _, o := range f.Origins {
		out.Origins = append(out.Origins, string(o))
	}

	return out
}

func toSubscription(s reactor.Subscription) *pb.Subscription {
	return &pb.Subscription{Id: s.ID, Description: s.Description, Filter: toFilter(s.Filter), CreatedAt: toTime(s.CreatedAt)}
}
