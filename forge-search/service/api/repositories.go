package api

import (
	"context"
	"errors"

	"github.com/r59q/forge-tools/forge-search/library/search"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
)

// KindTarget is the kind of the document that names the repository
// reference of an index IndexRepository made: its ID is the target as JSON.
// It has no content, and code searches never match it.
const KindTarget search.Kind = "repository-target"

// IndexRepository checks a repository reference out, syncs it into its
// index and removes the checkout. Calls for one reference run one at a
// time; others fail with ABORTED before they check anything out.
func (s *Server) IndexRepository(ctx context.Context, r *pb.IndexRepositoryRequest) (*pb.IndexRepositoryResponse, error) {
	return call(s, ctx, s.config.IndexTimeout, func(ctx context.Context, engine *search.Engine) (*pb.IndexRepositoryResponse, error) {
		t, err := target(r.GetTarget())
		if err != nil {
			return nil, err
		}
		index := t.index()

		release, err := s.reserve(index)
		if err != nil {
			return nil, err
		}
		defer release()

		c, root, done, err := s.checkOut(ctx, t)
		if err != nil {
			return nil, err
		}
		defer done()

		status, err := s.status(ctx, index)
		switch {
		case errors.Is(err, search.ErrNotFound):
			err = s.create(ctx, index)
		case err == nil && status.GetRevision() == c.Revision && status.GetSymbols().GetComplete():
			return &pb.IndexRepositoryResponse{Index: status, Files: &pb.FileState{}, UpToDate: true}, nil
		}
		if err != nil {
			return nil, err
		}

		ref := search.Ref{Kind: KindTarget, ID: t.record(), Version: "1"}
		if err := engine.Put(ctx, index, search.Document{Ref: ref, Content: []byte{}}); err != nil {
			return nil, err
		}

		result, err := s.sync(ctx, root, c, index)
		if err != nil {
			return nil, err
		}
		return &pb.IndexRepositoryResponse{
			Index:     result.GetIndex(),
			Added:     result.GetAdded(),
			Replaced:  result.GetReplaced(),
			Removed:   result.GetRemoved(),
			Unchanged: result.GetUnchanged(),
			Files:     result.GetFiles(),
			Symbols:   result.GetSymbols(),
		}, nil
	})
}

// SearchRepository searches the index of a repository reference, or,
// without one, a checkout made for the call.
func (s *Server) SearchRepository(ctx context.Context, r *pb.SearchRepositoryRequest) (*pb.SearchRepositoryResponse, error) {
	return call(s, ctx, s.config.IndexTimeout, func(ctx context.Context, engine *search.Engine) (*pb.SearchRepositoryResponse, error) {
		t, err := target(r.GetTarget())
		if err != nil {
			return nil, err
		}
		query, prefix, err := codeQuery(r.GetQuery())
		if err != nil {
			return nil, err
		}

		status, err := s.status(ctx, t.index())
		switch {
		case err == nil:
			return s.searchIndexed(ctx, status, query, prefix)
		case !errors.Is(err, search.ErrNotFound):
			return nil, err
		}

		// Reject a bad query before checking anything out.
		if _, err := search.Unindexed(ctx, query, nil, search.Limits{}); err != nil {
			return nil, err
		}
		c, root, done, err := s.checkOut(ctx, t)
		if err != nil {
			return nil, err
		}
		defer done()

		results, err := searchCheckout(ctx, root, query, prefix)
		if err != nil {
			return nil, err
		}
		return &pb.SearchRepositoryResponse{
			Matches:  matches(results.Matches),
			State:    searchState(results.State),
			Revision: c.Revision,
			Files:    fileState(results.Files),
		}, nil
	})
}

// searchIndexed searches an index within Timeout.
func (s *Server) searchIndexed(
	ctx context.Context, status *pb.Index, query search.Query, prefix string,
) (*pb.SearchRepositoryResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()

	results, err := s.searchIndex(ctx, status.GetName(), query, prefix)
	if err != nil {
		return nil, err
	}
	return &pb.SearchRepositoryResponse{
		Matches:  matches(results.Matches),
		State:    searchState(results.State),
		Revision: status.GetRevision(),
		Index:    status,
	}, nil
}
