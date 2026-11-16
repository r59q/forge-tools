package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-search/library/checkout"
	"github.com/r59q/forge-tools/forge-search/library/issue"
	"github.com/r59q/forge-tools/forge-search/library/search"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// KindRecord is the kind of the document that records the last completed
// indexing of an index: its ID is the checkout ID and its version the time,
// as RFC 3339. It has no content, and code searches never match it.
const KindRecord search.Kind = "checkout-index"

// IndexCheckout syncs a checkout into an index, creating the index first.
// Names IndexRepository and IndexIssues use, starting with "repo-" or
// "issues-", are refused.
func (s *Server) IndexCheckout(ctx context.Context, r *pb.IndexCheckoutRequest) (*pb.IndexCheckoutResponse, error) {
	return call(s, ctx, s.config.IndexTimeout, func(ctx context.Context, engine *search.Engine) (*pb.IndexCheckoutResponse, error) {
		if reserved(r.GetIndex()) {
			return nil, errBadRequest
		}

		_, err := engine.Index(ctx, r.GetIndex())
		exists := err == nil
		if err != nil && !errors.Is(err, search.ErrNotFound) {
			return nil, err
		}

		release, err := s.reserve(r.GetIndex())
		if err != nil {
			return nil, err
		}
		defer release()

		c, root, err := s.openCheckout(ctx, r.GetCheckoutId())
		if err != nil {
			return nil, err
		}
		defer func() { _ = root.Close() }()

		if !exists {
			if err := s.create(ctx, r.GetIndex()); err != nil {
				return nil, err
			}
		}
		return s.sync(ctx, root, c, r.GetIndex())
	})
}

// create creates an index unless MaxIndexes exist.
func (s *Server) create(ctx context.Context, index string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	indexes, err := s.config.Engine.Indexes(ctx)
	if err != nil {
		return err
	}
	if len(indexes) >= s.config.MaxIndexes {
		return errTooMany
	}

	err = s.config.Engine.CreateIndex(ctx, index)
	if errors.Is(err, search.ErrExists) {
		return nil
	}
	return err
}

// sync brings the code of an index to a checkout and records it.
func (s *Server) sync(ctx context.Context, root *os.Root, c Checkout, index string) (*pb.IndexCheckoutResponse, error) {
	engine := s.config.Engine

	result, err := checkout.Sync(ctx, root, engine, index, c.Revision, checkout.Options{})
	if err != nil {
		return nil, err
	}
	if err := s.record(ctx, index, c.ID); err != nil {
		return nil, err
	}

	status, err := s.status(ctx, index)
	if err != nil {
		return nil, err
	}

	return &pb.IndexCheckoutResponse{
		Index:     status,
		Added:     count(result.Added),
		Replaced:  count(result.Replaced),
		Removed:   count(result.Removed),
		Unchanged: count(result.Unchanged),
		Files:     fileState(result.Files),
	}, nil
}

// record replaces the record of an index with one for checkout, now.
func (s *Server) record(ctx context.Context, index, checkoutID string) error {
	engine := s.config.Engine

	records, err := engine.Documents(ctx, index, search.Filter{Kind: KindRecord})
	if err != nil {
		return err
	}

	ref := search.Ref{Kind: KindRecord, ID: checkoutID, Version: s.config.Now().UTC().Format(time.RFC3339Nano)}
	if err := engine.Put(ctx, index, search.Document{Ref: ref, Content: []byte{}}); err != nil {
		return err
	}

	for _, old := range records {
		if old.ID == checkoutID {
			continue
		}
		if err := engine.Remove(ctx, index, old); err != nil && !errors.Is(err, search.ErrNotFound) {
			return err
		}
	}
	return nil
}

// ListIndexes returns the status of every index.
func (s *Server) ListIndexes(ctx context.Context, _ *pb.ListIndexesRequest) (*pb.ListIndexesResponse, error) {
	return call(s, ctx, s.config.Timeout, func(ctx context.Context, engine *search.Engine) (*pb.ListIndexesResponse, error) {
		infos, err := engine.Indexes(ctx)
		if err != nil {
			return nil, err
		}

		indexes := make([]*pb.Index, 0, len(infos))
		for _, info := range infos {
			status, err := s.status(ctx, info.Name)
			if errors.Is(err, search.ErrNotFound) {
				continue // Deleted since it was listed.
			}
			if err != nil {
				return nil, err
			}
			indexes = append(indexes, status)
		}
		return &pb.ListIndexesResponse{Indexes: indexes}, nil
	})
}

// GetIndex returns the status of an index.
func (s *Server) GetIndex(ctx context.Context, r *pb.GetIndexRequest) (*pb.GetIndexResponse, error) {
	return call(s, ctx, s.config.Timeout, func(ctx context.Context, _ *search.Engine) (*pb.GetIndexResponse, error) {
		status, err := s.status(ctx, r.GetIndex())
		if err != nil {
			return nil, err
		}
		return &pb.GetIndexResponse{Index: status}, nil
	})
}

// DeleteIndex removes an index that is not being indexed.
func (s *Server) DeleteIndex(ctx context.Context, r *pb.DeleteIndexRequest) (*pb.DeleteIndexResponse, error) {
	return call(s, ctx, s.config.Timeout, func(ctx context.Context, engine *search.Engine) (*pb.DeleteIndexResponse, error) {
		if _, err := engine.Index(ctx, r.GetIndex()); err != nil {
			return nil, err
		}

		release, err := s.reserve(r.GetIndex())
		if err != nil {
			return nil, err
		}
		defer release()

		if err := engine.DeleteIndex(ctx, r.GetIndex()); err != nil {
			return nil, err
		}
		if s.config.Checkpoint != nil {
			if err := s.config.Checkpoint(ctx); err != nil {
				return nil, err
			}
		}
		return &pb.DeleteIndexResponse{}, nil
	})
}

// status returns the status of an index. The checkout and time are those of
// the record, shown only while the revision is set.
func (s *Server) status(ctx context.Context, index string) (*pb.Index, error) {
	engine := s.config.Engine

	info, err := engine.Index(ctx, index)
	if err != nil {
		return nil, err
	}
	own, owned, err := s.ownDocuments(ctx, index)
	if err != nil {
		return nil, err
	}
	records, targets := own[KindRecord], own[KindTarget]

	status := &pb.Index{
		Name:      info.Name,
		Documents: int64(info.Documents - owned),
		Bytes:     info.Bytes,
		Revision:  info.Marker,
	}
	if repos := own[KindIssueRepository]; len(repos) > 0 {
		return s.issueStatus(ctx, status, repos[0].ID)
	}
	if len(targets) > 0 {
		t, err := parseTarget(targets[0].ID)
		if err != nil {
			return nil, fmt.Errorf("index target: %w", err)
		}
		status.Target = t.proto()
	}
	if info.Marker == "" || len(records) == 0 {
		return status, nil
	}

	updated, err := time.Parse(time.RFC3339Nano, records[0].Version)
	if err != nil {
		return nil, fmt.Errorf("index record: %w", err)
	}
	status.CheckoutId = records[0].ID
	status.UpdatedAt = timestamppb.New(updated)
	return status, nil
}

// ownDocuments returns the documents of an index that the server and the
// issue sync write about it, by kind, and how many there are. Index
// statuses do not count them.
func (s *Server) ownDocuments(ctx context.Context, index string) (map[search.Kind][]search.Ref, int, error) {
	own := map[search.Kind][]search.Ref{}
	total := 0
	for _, kind := range []search.Kind{KindRecord, KindTarget, KindIssueRepository, issue.KindSync} {
		refs, err := s.config.Engine.Documents(ctx, index, search.Filter{Kind: kind})
		if err != nil {
			return nil, 0, err
		}
		own[kind] = refs
		total += len(refs)
	}
	return own, total, nil
}

// reserved reports whether an index name belongs to IndexRepository or
// IndexIssues.
func reserved(index string) bool {
	return strings.HasPrefix(index, repositoryPrefix) || strings.HasPrefix(index, issuesPrefix)
}
