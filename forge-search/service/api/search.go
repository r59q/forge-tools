package api

import (
	"context"
	"math"
	"os"

	"github.com/r59q/forge-tools/forge-search/library/checkout"
	"github.com/r59q/forge-tools/forge-search/library/search"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
)

// Result limits of a CodeQuery.
const (
	DefaultResults = 100
	MaxResults     = 500
)

// SearchIndex searches the code of an index.
func (s *Server) SearchIndex(ctx context.Context, r *pb.SearchIndexRequest) (*pb.SearchIndexResponse, error) {
	return call(s, ctx, s.config.Timeout, func(ctx context.Context, engine *search.Engine) (*pb.SearchIndexResponse, error) {
		query, prefix, err := codeQuery(r.GetQuery())
		if err != nil {
			return nil, err
		}

		info, err := engine.Index(ctx, r.GetIndex())
		if err != nil {
			return nil, err
		}
		results, err := s.searchIndex(ctx, r.GetIndex(), query, prefix)
		if err != nil {
			return nil, err
		}

		return &pb.SearchIndexResponse{
			Matches:  matches(results.Matches),
			State:    searchState(results.State),
			Revision: info.Marker,
		}, nil
	})
}

// SearchCheckout reads and searches the code of a checkout.
func (s *Server) SearchCheckout(ctx context.Context, r *pb.SearchCheckoutRequest) (*pb.SearchCheckoutResponse, error) {
	return call(s, ctx, s.config.Timeout, func(ctx context.Context, _ *search.Engine) (*pb.SearchCheckoutResponse, error) {
		query, prefix, err := codeQuery(r.GetQuery())
		if err != nil {
			return nil, err
		}

		c, root, err := s.openCheckout(ctx, r.GetCheckoutId())
		if err != nil {
			return nil, err
		}
		defer func() { _ = root.Close() }()

		results, err := searchCheckout(ctx, root, query, prefix)
		if err != nil {
			return nil, err
		}

		return &pb.SearchCheckoutResponse{
			Matches:  matches(results.Matches),
			State:    searchState(results.State),
			Revision: c.Revision,
			Files:    fileState(results.Files),
		}, nil
	})
}

// searchIndex searches the code of an index, failing if ctx ends first.
func (s *Server) searchIndex(ctx context.Context, index string, query search.Query, prefix string) (search.Results, error) {
	results, err := s.config.Engine.Search(ctx, index, query, search.Filter{Kind: search.KindCode, IDPrefix: prefix})
	if err != nil {
		return search.Results{}, err
	}
	return results, canceled(ctx, results.State)
}

// searchCheckout searches the code of a checkout directory, failing if ctx
// ends first.
func searchCheckout(ctx context.Context, root *os.Root, query search.Query, prefix string) (checkout.Results, error) {
	results, err := checkout.Search(ctx, root, query, checkout.Options{Prefix: prefix})
	if err != nil {
		return checkout.Results{}, err
	}
	return results, canceled(ctx, results.State)
}

// codeQuery returns the library query and path prefix of a CodeQuery.
// The library validates the text and prefix.
func codeQuery(q *pb.CodeQuery) (search.Query, string, error) {
	limit := int(q.GetMaxResults())
	switch {
	case limit == 0:
		limit = DefaultResults
	case limit < 0 || limit > MaxResults:
		return search.Query{}, "", errBadRequest
	}
	return search.Query{Text: q.GetText(), Limit: limit}, q.GetPathPrefix(), nil
}

// canceled returns the context error of a search the context ended, whose
// matches are incomplete.
func canceled(ctx context.Context, state search.State) error {
	if !state.Canceled {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return context.Canceled
}

func matches(found []search.Match) []*pb.CodeMatch {
	out := make([]*pb.CodeMatch, len(found))
	for i, m := range found {
		out[i] = &pb.CodeMatch{
			Path:    m.Document.ID,
			Version: m.Document.Version,
			Range: &pb.Range{
				StartLine:   count(m.Range.StartLine),
				StartColumn: count(m.Range.StartColumn),
				EndLine:     count(m.Range.EndLine),
				EndColumn:   count(m.Range.EndColumn),
			},
			Preview:       m.Preview,
			PreviewColumn: count(m.PreviewColumn),
		}
	}
	return out
}

func searchState(state search.State) *pb.SearchState {
	return &pb.SearchState{Truncated: state.Truncated, Skipped: count(state.Skipped)}
}

func fileState(state checkout.State) *pb.FileState {
	return &pb.FileState{
		Truncated:   state.Truncated,
		TooLarge:    count(state.TooLarge),
		Unsupported: count(state.Unsupported),
	}
}

// count converts a count, line or column, clamped to [0, math.MaxInt32].
func count(n int) int32 {
	return int32(max(0, min(n, math.MaxInt32)))
}
