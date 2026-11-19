package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/r59q/forge-tools/forge-search/library/search"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
)

// Uploaded documents have their own kind so a path cannot overwrite a
// checkout file, issue, definition or synchronization record.
const (
	uploadedKind     search.Kind = "document"
	maxDocuments                 = 256
	maxDocumentTotal             = 4 << 20
)

// CreateIndex creates an empty named index within the shared index quota.
func (s *Server) CreateIndex(ctx context.Context, r *pb.CreateIndexRequest) (*pb.CreateIndexResponse, error) {
	return call(s, ctx, s.config.Timeout, func(ctx context.Context, _ *search.Engine) (*pb.CreateIndexResponse, error) {
		if reserved(r.GetIndex()) {
			return nil, errBadRequest
		}
		release, err := s.reserve(r.GetIndex())
		if err != nil {
			return nil, err
		}
		defer release()
		return &pb.CreateIndexResponse{}, s.create(ctx, r.GetIndex())
	})
}

// PutDocument adds or replaces one uploaded document by path.
func (s *Server) PutDocument(ctx context.Context, r *pb.PutDocumentRequest) (*pb.PutDocumentResponse, error) {
	return call(s, ctx, s.config.Timeout, func(ctx context.Context, engine *search.Engine) (*pb.PutDocumentResponse, error) {
		if reserved(r.GetIndex()) {
			return nil, errBadRequest
		}
		err := engine.Put(ctx, r.GetIndex(), uploaded(r.GetDocument()))
		return &pb.PutDocumentResponse{}, err
	})
}

// RemoveDocument deletes one uploaded document by path.
func (s *Server) RemoveDocument(ctx context.Context, r *pb.RemoveDocumentRequest) (*pb.RemoveDocumentResponse, error) {
	return call(s, ctx, s.config.Timeout, func(ctx context.Context, engine *search.Engine) (*pb.RemoveDocumentResponse, error) {
		if reserved(r.GetIndex()) {
			return nil, errBadRequest
		}
		err := engine.Remove(ctx, r.GetIndex(), search.Ref{Kind: uploadedKind, ID: r.GetPath()})
		return &pb.RemoveDocumentResponse{}, err
	})
}

// SearchDocuments searches either uploaded documents in an index or a request's documents.
func (s *Server) SearchDocuments(ctx context.Context, r *pb.SearchDocumentsRequest) (*pb.SearchDocumentsResponse, error) {
	return call(s, ctx, s.config.Timeout, func(ctx context.Context, engine *search.Engine) (*pb.SearchDocumentsResponse, error) {
		limit, err := resultLimit(r.GetMaxResults())
		if err != nil {
			return nil, err
		}
		query := search.Query{Text: r.GetText(), Limit: limit}
		var results search.Results
		switch {
		case r.GetIndex() == "":
			results, err = scanDocuments(ctx, query, r.GetDocuments())
		case len(r.GetDocuments()) != 0:
			return nil, errBadRequest
		default:
			results, err = engine.Search(ctx, r.GetIndex(), query, search.Filter{Kind: uploadedKind})
		}
		if err != nil {
			return nil, err
		}
		return &pb.SearchDocumentsResponse{
			Matches: matches(results.Matches), State: searchState(results.State),
		}, canceled(ctx, results.State)
	})
}

func scanDocuments(ctx context.Context, query search.Query, input []*pb.Document) (search.Results, error) {
	if len(input) > maxDocuments {
		return search.Results{}, search.ErrTooLarge
	}
	documents := make([]search.Document, 0, len(input))
	total := 0
	for _, d := range input {
		total += len(d.GetContent())
		if total > maxDocumentTotal {
			return search.Results{}, search.ErrTooLarge
		}
		documents = append(documents, uploaded(d))
	}
	return search.Unindexed(ctx, query, documents, search.Limits{})
}

func uploaded(d *pb.Document) search.Document {
	content := d.GetContent()
	version := sha256.Sum256(content)
	return search.Document{
		Ref:     search.Ref{Kind: uploadedKind, ID: d.GetPath(), Version: hex.EncodeToString(version[:])},
		Content: content,
	}
}
