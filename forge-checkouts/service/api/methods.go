package api

import (
	"context"

	"github.com/r59q/forge-tools/forge-checkouts/library/checkouts"
	pb "github.com/r59q/forge-tools/forge-checkouts/service/checkoutspb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func checkout(w checkouts.Checkout) *pb.Checkout {
	return &pb.Checkout{Id: w.ID, Repository: &pb.Repository{LocalPath: w.Repository.LocalPath, Source: w.Repository.Source, Path: w.Repository.Path}, Reference: w.Reference, Revision: w.Revision, State: w.State, CreatedAt: timestamppb.New(w.CreatedAt), Location: w.Location}
}

// CreateCheckout prepares an independent checkout from a local or connected repository.
func (s *Server) CreateCheckout(ctx context.Context, r *pb.CreateCheckoutRequest) (*pb.Checkout, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.Checkout, error) {
		repo := r.GetRepository()
		w, err := s.manager.Create(ctx, checkouts.Input{Repository: checkouts.Repository{LocalPath: repo.GetLocalPath(), Source: repo.GetSource(), Path: repo.GetPath()}, Reference: r.GetReference()})
		if err != nil {
			return nil, err
		}
		return checkout(w), nil
	})
}

// ListCheckouts reads all persistent metadata, including failed creations.
func (s *Server) ListCheckouts(ctx context.Context, _ *emptypb.Empty) (*pb.ListCheckoutsResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ListCheckoutsResponse, error) {
		all, err := s.manager.List(ctx)
		if err != nil {
			return nil, err
		}
		out := &pb.ListCheckoutsResponse{}
		for _, w := range all {
			out.Checkouts = append(out.Checkouts, checkout(w))
		}
		return out, nil
	})
}

// GetCheckout reads the stored identity, initial revision and lifecycle state.
func (s *Server) GetCheckout(ctx context.Context, r *pb.CheckoutRequest) (*pb.Checkout, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.Checkout, error) {
		w, err := s.manager.Get(ctx, r.GetId())
		if err != nil {
			return nil, err
		}
		return checkout(w), nil
	})
}

// RetainCheckout marks a checkout for retention until explicit removal.
func (s *Server) RetainCheckout(ctx context.Context, r *pb.CheckoutRequest) (*pb.Checkout, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.Checkout, error) {
		w, err := s.manager.Retain(ctx, r.GetId())
		if err != nil {
			return nil, err
		}
		return checkout(w), nil
	})
}

// RemoveCheckout protects uncommitted files unless discard is true.
func (s *Server) RemoveCheckout(ctx context.Context, r *pb.RemoveCheckoutRequest) (*emptypb.Empty, error) {
	return call(s, ctx, func(ctx context.Context) (*emptypb.Empty, error) {
		err := s.manager.Remove(ctx, r.GetId(), r.GetDiscard())
		return &emptypb.Empty{}, err
	})
}

// InspectCheckout reports current HEAD, optional attached branch and changed paths.
func (s *Server) InspectCheckout(ctx context.Context, r *pb.CheckoutRequest) (*pb.Status, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.Status, error) {
		st, err := s.manager.Inspect(ctx, r.GetId())
		if err != nil {
			return nil, err
		}
		out := &pb.Status{Revision: st.Revision, Branch: st.Branch}
		for _, f := range st.Files {
			out.Files = append(out.Files, &pb.File{Path: f.Path, OldPath: f.OldPath, Index: f.Index, Worktree: f.Worktree})
		}
		return out, nil
	})
}

// ReadDiff reads staged and unstaged binary-capable patches.
func (s *Server) ReadDiff(ctx context.Context, r *pb.CheckoutRequest) (*pb.Diff, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.Diff, error) {
		d, err := s.manager.ReadDiff(ctx, r.GetId())
		if err != nil {
			return nil, err
		}
		return &pb.Diff{Staged: []byte(d.Staged), Unstaged: []byte(d.Unstaged)}, nil
	})
}
