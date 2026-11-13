package api

import (
	"context"

	"github.com/r59q/forge-tools/forge-symbols/library/navigation"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
	pb "github.com/r59q/forge-tools/forge-symbols/service/symbolspb"
)

// Analyze returns the definitions and references of each file.
func (s *Server) Analyze(ctx context.Context, r *pb.AnalyzeRequest) (*pb.AnalyzeResponse, error) {
	return call(s, ctx, func(ctx context.Context, analyzer *symbols.Analyzer) (*pb.AnalyzeResponse, error) {
		analyses, err := analyzer.AnalyzeFiles(ctx, files(r.GetFiles()))
		if err != nil {
			return nil, err
		}

		return &pb.AnalyzeResponse{Analyses: convert(analyses, analysis)}, nil
	})
}

// Outgoing returns the references inside the target definitions.
func (s *Server) Outgoing(ctx context.Context, r *pb.OutgoingRequest) (*pb.OutgoingResponse, error) {
	return navigate(s, ctx, r, navigation.Outgoing, outgoing)
}

// Incoming returns the references to the target, grouped by caller.
func (s *Server) Incoming(ctx context.Context, r *pb.IncomingRequest) (*pb.IncomingResponse, error) {
	return navigate(s, ctx, r, navigation.Incoming, incoming)
}

// Definitions returns the definitions named like the target.
func (s *Server) Definitions(ctx context.Context, r *pb.DefinitionsRequest) (*pb.DefinitionsResponse, error) {
	return navigate(s, ctx, r, navigation.Definitions, definitions)
}

// query is what every navigation request holds.
type query interface {
	GetFiles() []*pb.File
	GetTarget() *pb.Target
	GetMaxResults() int32
}

// finder is a navigation helper, such as navigation.Outgoing.
type finder[T any] func([]symbols.Analysis, navigation.Target, navigation.Limits) (T, error)

// navigate analyzes the request's files, runs find on them and converts the
// result.
func navigate[T, R any](s *Server, ctx context.Context, r query, find finder[T], out func(T) *R) (*R, error) {
	return call(s, ctx, func(ctx context.Context, analyzer *symbols.Analyzer) (*R, error) {
		limits, err := resultLimits(r.GetMaxResults())
		if err != nil {
			return nil, err
		}

		analyses, err := analyzer.AnalyzeFiles(ctx, files(r.GetFiles()))
		if err != nil {
			return nil, err
		}

		found, err := find(analyses, target(r.GetTarget()), limits)
		if err != nil {
			return nil, err
		}

		return out(found), nil
	})
}

// resultLimits checks max_results: 0 means the default, which is also the
// maximum.
func resultLimits(maxResults int32) (navigation.Limits, error) {
	if maxResults < 0 || maxResults > navigation.DefaultResults {
		return navigation.Limits{}, errBadRequest
	}

	return navigation.Limits{Results: int(maxResults)}, nil
}
