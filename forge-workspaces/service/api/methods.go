package api

import (
	"context"

	"github.com/r59q/forge-tools/forge-workspaces/library/workspaces"
	pb "github.com/r59q/forge-tools/forge-workspaces/service/workspacespb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func workspace(w workspaces.Workspace) *pb.Workspace {
	return &pb.Workspace{Id: w.ID, Repository: &pb.Repository{LocalPath: w.Repository.LocalPath, Source: w.Repository.Source, Path: w.Repository.Path}, Reference: w.Reference, Revision: w.Revision, State: w.State, CreatedAt: timestamppb.New(w.CreatedAt), Location: w.Location}
}

// CreateWorkspace prepares an independent checkout from a local or connected repository.
func (s *Server) CreateWorkspace(ctx context.Context, r *pb.CreateWorkspaceRequest) (*pb.Workspace, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.Workspace, error) {
		repo := r.GetRepository()
		w, err := s.manager.Create(ctx, workspaces.Input{Repository: workspaces.Repository{LocalPath: repo.GetLocalPath(), Source: repo.GetSource(), Path: repo.GetPath()}, Reference: r.GetReference()})
		if err != nil {
			return nil, err
		}
		return workspace(w), nil
	})
}

// ListWorkspaces reads all persistent metadata, including failed creations.
func (s *Server) ListWorkspaces(ctx context.Context, _ *emptypb.Empty) (*pb.ListWorkspacesResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ListWorkspacesResponse, error) {
		all, err := s.manager.List(ctx)
		if err != nil {
			return nil, err
		}
		out := &pb.ListWorkspacesResponse{}
		for _, w := range all {
			out.Workspaces = append(out.Workspaces, workspace(w))
		}
		return out, nil
	})
}

// GetWorkspace reads the stored identity, initial revision and lifecycle state.
func (s *Server) GetWorkspace(ctx context.Context, r *pb.WorkspaceRequest) (*pb.Workspace, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.Workspace, error) {
		w, err := s.manager.Get(ctx, r.GetId())
		if err != nil {
			return nil, err
		}
		return workspace(w), nil
	})
}

// RetainWorkspace marks a checkout for retention until explicit removal.
func (s *Server) RetainWorkspace(ctx context.Context, r *pb.WorkspaceRequest) (*pb.Workspace, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.Workspace, error) {
		w, err := s.manager.Retain(ctx, r.GetId())
		if err != nil {
			return nil, err
		}
		return workspace(w), nil
	})
}

// RemoveWorkspace protects uncommitted files unless discard is true.
func (s *Server) RemoveWorkspace(ctx context.Context, r *pb.RemoveWorkspaceRequest) (*emptypb.Empty, error) {
	return call(s, ctx, func(ctx context.Context) (*emptypb.Empty, error) {
		err := s.manager.Remove(ctx, r.GetId(), r.GetDiscard())
		return &emptypb.Empty{}, err
	})
}

// InspectWorkspace reports current HEAD, optional attached branch and changed paths.
func (s *Server) InspectWorkspace(ctx context.Context, r *pb.WorkspaceRequest) (*pb.Status, error) {
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
func (s *Server) ReadDiff(ctx context.Context, r *pb.WorkspaceRequest) (*pb.Diff, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.Diff, error) {
		d, err := s.manager.ReadDiff(ctx, r.GetId())
		if err != nil {
			return nil, err
		}
		return &pb.Diff{Staged: []byte(d.Staged), Unstaged: []byte(d.Unstaged)}, nil
	})
}
