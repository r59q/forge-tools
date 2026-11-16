package api

import (
	"context"

	"github.com/r59q/forge-tools/forge-search/library/checkout"
	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-search/library/symbol"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
)

// SearchIndexSymbols searches the definitions an index holds.
func (s *Server) SearchIndexSymbols(ctx context.Context, r *pb.SearchIndexSymbolsRequest) (*pb.SearchIndexSymbolsResponse, error) {
	return call(s, ctx, s.config.Timeout, func(ctx context.Context, engine *search.Engine) (*pb.SearchIndexSymbolsResponse, error) {
		query, err := symbolQuery(r.GetQuery())
		if err != nil {
			return nil, err
		}

		info, err := engine.Index(ctx, r.GetIndex())
		if err != nil {
			return nil, err
		}
		complete, err := s.symbolsComplete(ctx, r.GetIndex(), info.Marker)
		if err != nil {
			return nil, err
		}

		results, err := symbol.SearchIndex(ctx, engine, r.GetIndex(), query, symbol.Limits{Results: MaxResults})
		if err != nil {
			return nil, err
		}
		if err := symbolsCanceled(ctx, results.State); err != nil {
			return nil, err
		}

		return &pb.SearchIndexSymbolsResponse{
			Symbols:   symbolsOf(results.Symbols),
			Truncated: results.State.Truncated,
			Revision:  info.Marker,
			Complete:  complete,
		}, nil
	})
}

// SearchCheckoutSymbols reads a checkout and searches its definitions. More
// files or content to analyze than symbol.DefaultLimits allow fail with
// RESOURCE_EXHAUSTED.
func (s *Server) SearchCheckoutSymbols(
	ctx context.Context, r *pb.SearchCheckoutSymbolsRequest,
) (*pb.SearchCheckoutSymbolsResponse, error) {
	return call(s, ctx, s.config.Timeout, func(ctx context.Context, _ *search.Engine) (*pb.SearchCheckoutSymbolsResponse, error) {
		query, err := symbolQuery(r.GetQuery())
		if err != nil {
			return nil, err
		}
		// Reject a bad query before reading the checkout.
		if _, err := symbol.Search(ctx, s.analyzer, nil, query, symbol.Limits{Results: MaxResults}); err != nil {
			return nil, err
		}

		c, root, err := s.openCheckout(ctx, r.GetCheckoutId())
		if err != nil {
			return nil, err
		}
		defer func() { _ = root.Close() }()

		tree, err := checkout.Read(ctx, root, checkout.Options{Prefix: query.Filter.PathPrefix})
		if err != nil {
			return nil, err
		}
		results, err := symbol.Search(ctx, s.analyzer, tree.Documents, query, symbol.Limits{Results: MaxResults})
		if err != nil {
			return nil, err
		}
		if err := symbolsCanceled(ctx, results.State); err != nil {
			return nil, err
		}

		return &pb.SearchCheckoutSymbolsResponse{
			Symbols:   symbolsOf(results.Symbols),
			Truncated: results.State.Truncated,
			Revision:  c.Revision,
			Files:     fileState(tree.State),
			Skipped:   symbolFileState(results.Skipped),
		}, nil
	})
}

// symbolsComplete reports whether the symbol record of an index names its
// marker.
func (s *Server) symbolsComplete(ctx context.Context, index, marker string) (bool, error) {
	if marker == "" {
		return false, nil
	}
	records, err := s.config.Engine.Documents(ctx, index, search.Filter{Kind: KindSymbolRecord, ID: marker})
	return len(records) > 0, err
}

// symbolStatus returns the symbol state of a code index from its own
// documents, and its code files in a language without symbol support.
func (s *Server) symbolStatus(ctx context.Context, index, marker string, own map[search.Kind][]search.Ref) (*pb.IndexSymbols, error) {
	code, err := s.config.Engine.Documents(ctx, index, search.Filter{Kind: search.KindCode})
	if err != nil {
		return nil, err
	}

	complete := false
	for _, record := range own[KindSymbolRecord] {
		complete = complete || (marker != "" && record.ID == marker)
	}

	skipped := symbol.Skipped{}
	for _, ref := range code {
		if _, known := symbols.DetectLanguage(ref.ID); !known {
			skipped.Unsupported++
		}
	}
	for _, ref := range own[KindSymbolSkipped] {
		switch symbol.SkipReason(ref.Version) {
		case symbol.SkipUnsupported:
			skipped.Unsupported++
		case symbol.SkipTooLarge:
			skipped.TooLarge++
		case symbol.SkipFailed:
			skipped.Failed++
		}
	}

	return &pb.IndexSymbols{
		Complete:  complete,
		Documents: int64(len(own[symbol.KindSymbol])),
		Skipped:   symbolFileState(skipped),
	}, nil
}

// symbolQuery returns the library query of a SymbolQuery. The library
// validates the text and filter.
func symbolQuery(q *pb.SymbolQuery) (symbol.Query, error) {
	limit, err := resultLimit(q.GetMaxResults())
	if err != nil {
		return symbol.Query{}, err
	}
	filter := symbol.Filter{PathPrefix: q.GetPathPrefix(), Kind: q.GetKind(), Language: q.GetLanguage()}
	return symbol.Query{Text: q.GetText(), Filter: filter, Limit: limit}, nil
}

// symbolsCanceled returns the context error of a symbol search the context
// ended.
func symbolsCanceled(ctx context.Context, state symbol.State) error {
	return canceled(ctx, search.State{Canceled: state.Canceled})
}

func symbolsOf(found []symbol.Symbol) []*pb.Symbol {
	out := make([]*pb.Symbol, len(found))
	for i, s := range found {
		out[i] = &pb.Symbol{
			Name:          s.Name,
			Kind:          s.Kind,
			Language:      s.Language,
			Path:          s.Document.ID,
			Version:       s.Document.Version,
			NameRange:     rangeOf(s.NameRange),
			Range:         rangeOf(s.Range),
			Container:     s.Container,
			Preview:       s.Preview,
			PreviewColumn: count(s.PreviewColumn),
		}
	}
	return out
}

func symbolFileState(skipped symbol.Skipped) *pb.SymbolFileState {
	return &pb.SymbolFileState{
		Unsupported: count(skipped.Unsupported),
		TooLarge:    count(skipped.TooLarge),
		Failed:      count(skipped.Failed),
	}
}
