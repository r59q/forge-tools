package api

import (
	"context"
	"time"

	"github.com/r59q/forge-tools/forge-changes/library/changes"
	pb "github.com/r59q/forge-tools/forge-changes/service/changespb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
func user(u changes.User) *pb.User { return &pb.User{Login: u.Login, WebUrl: u.WebURL} }
func revision(v changes.Revision) *pb.Revision {
	return &pb.Revision{Head: v.Head, Target: v.Target, Base: v.Base}
}
func change(v changes.ChangeRequest) *pb.ChangeRequest {
	return &pb.ChangeRequest{Ref: &pb.ChangeRef{Source: v.Ref.Source, Repository: v.Ref.Repository, Number: v.Ref.Number}, Kind: string(v.Kind), Id: v.ID,
		Title: v.Title, Description: v.Description, Author: user(v.Author), SourceRepository: v.SourceRepository, SourceBranch: v.SourceBranch,
		TargetBranch: v.TargetBranch, Revision: revision(v.Revision), State: string(v.State), Draft: v.Draft, WebUrl: v.WebURL,
		CreatedAt: timestamp(v.CreatedAt), UpdatedAt: timestamp(v.UpdatedAt), ClosedAt: timestamp(v.ClosedAt), MergedAt: timestamp(v.MergedAt)}
}
func comment(v changes.Comment) *pb.Comment {
	return &pb.Comment{Id: v.ID, Author: user(v.Author), Body: v.Body, WebUrl: v.WebURL, CreatedAt: timestamp(v.CreatedAt), UpdatedAt: timestamp(v.UpdatedAt)}
}
func ref(v interface {
	GetSource() string
	GetRepository() string
	GetNumber() int64
}) changes.Ref {
	return changes.Ref{Source: v.GetSource(), Repository: v.GetRepository(), Number: v.GetNumber()}
}

// ListSources exposes connection IDs, forge kinds and adapter capabilities.
func (s *Server) ListSources(ctx context.Context, _ *emptypb.Empty) (*pb.ListSourcesResponse, error) {
	return call(s, ctx, func(_ context.Context, c *changes.Client) (*pb.ListSourcesResponse, error) {
		out := &pb.ListSourcesResponse{}
		for _, src := range c.Sources() {
			caps, err := c.Capabilities(src.ID)
			if err != nil {
				return nil, err
			}
			out.Sources = append(out.Sources, &pb.Source{Id: src.ID, Kind: string(src.Session.Kind), Capabilities: &pb.Capabilities{UpdateSourceBranch: caps.UpdateSourceBranch, DraftTransition: caps.DraftTransition, MergeMethods: caps.MergeMethods, ExpectedHead: caps.ExpectedHead}})
		}
		return out, nil
	})
}

// ListChanges reads proposals matching the repository and filters.
func (s *Server) ListChanges(ctx context.Context, r *pb.ListChangesRequest) (*pb.ListChangesResponse, error) {
	return call(s, ctx, func(ctx context.Context, c *changes.Client) (*pb.ListChangesResponse, error) {
		v, err := c.List(ctx, changes.ListOptions{Source: r.GetSource(), Repository: r.GetRepository(), Author: r.GetAuthor(), State: r.GetState(), Draft: r.Draft})
		out := &pb.ListChangesResponse{}
		for _, item := range v {
			out.Changes = append(out.Changes, change(item))
		}
		return out, err
	})
}

// GetChange reads a proposal on the required source.
func (s *Server) GetChange(ctx context.Context, r *pb.ChangeRef) (*pb.ChangeRequest, error) {
	return call(s, ctx, func(ctx context.Context, c *changes.Client) (*pb.ChangeRequest, error) {
		v, err := c.Get(ctx, ref(r))
		return change(v), err
	})
}

// CreateChange proposes source and target branches in one repository.
func (s *Server) CreateChange(ctx context.Context, r *pb.CreateChangeRequest) (*pb.ChangeRequest, error) {
	return call(s, ctx, func(ctx context.Context, c *changes.Client) (*pb.ChangeRequest, error) {
		v, err := c.Create(ctx, changes.RepositoryRef{Source: r.GetSource(), Repository: r.GetRepository()}, changes.NewChange{Title: r.GetTitle(), Description: r.GetDescription(), SourceBranch: r.GetSourceBranch(), TargetBranch: r.GetTargetBranch(), Draft: r.GetDraft()})
		return change(v), err
	})
}

// UpdateChange changes only the fields present in the request.
func (s *Server) UpdateChange(ctx context.Context, r *pb.UpdateChangeRequest) (*pb.ChangeRequest, error) {
	return call(s, ctx, func(ctx context.Context, c *changes.Client) (*pb.ChangeRequest, error) {
		u := changes.Update{Title: r.Title, Description: r.Description, SourceBranch: r.SourceBranch, TargetBranch: r.TargetBranch}
		if r.State != nil {
			state := changes.State(*r.State)
			u.State = &state
		}
		v, err := c.Update(ctx, ref(r), u)
		return change(v), err
	})
}

// SetDraft marks a proposal draft or ready.
func (s *Server) SetDraft(ctx context.Context, r *pb.SetDraftRequest) (*pb.ChangeRequest, error) {
	return call(s, ctx, func(ctx context.Context, c *changes.Client) (*pb.ChangeRequest, error) {
		v, err := c.SetDraft(ctx, ref(r), r.GetDraft())
		return change(v), err
	})
}

// ListCommits reads the proposal's commits in forge order.
func (s *Server) ListCommits(ctx context.Context, r *pb.ChangeRef) (*pb.ListCommitsResponse, error) {
	return call(s, ctx, func(ctx context.Context, c *changes.Client) (*pb.ListCommitsResponse, error) {
		v, err := c.Commits(ctx, ref(r))
		out := &pb.ListCommitsResponse{}
		for _, item := range v {
			out.Commits = append(out.Commits, &pb.Commit{Id: item.ID, Message: item.Message, AuthorName: item.AuthorName, AuthorEmail: item.AuthorEmail, CreatedAt: timestamp(item.CreatedAt)})
		}
		return out, err
	})
}

// ListFiles reads changed paths, patches and completeness information.
func (s *Server) ListFiles(ctx context.Context, r *pb.ChangeRef) (*pb.Files, error) {
	return call(s, ctx, func(ctx context.Context, c *changes.Client) (*pb.Files, error) {
		v, err := c.Files(ctx, ref(r))
		out := &pb.Files{Revision: revision(v.Revision), Completeness: v.Completeness}
		for _, item := range v.Files {
			out.Files = append(out.Files, &pb.ChangedFile{Path: item.Path, OldPath: item.OldPath, Status: item.Status, Patch: item.Patch, PatchUnavailable: item.PatchUnavailable})
		}
		return out, err
	})
}

// GetDiff reads the forge's raw unified diff with its revision.
func (s *Server) GetDiff(ctx context.Context, r *pb.ChangeRef) (*pb.Diff, error) {
	return call(s, ctx, func(ctx context.Context, c *changes.Client) (*pb.Diff, error) {
		v, err := c.Diff(ctx, ref(r))
		return &pb.Diff{Revision: revision(v.Revision), Patch: v.Patch, Completeness: v.Completeness}, err
	})
}

// ListComments reads general discussion, excluding review and system notes.
func (s *Server) ListComments(ctx context.Context, r *pb.ChangeRef) (*pb.ListCommentsResponse, error) {
	return call(s, ctx, func(ctx context.Context, c *changes.Client) (*pb.ListCommentsResponse, error) {
		v, err := c.Comments(ctx, ref(r))
		out := &pb.ListCommentsResponse{}
		for _, item := range v {
			out.Comments = append(out.Comments, comment(item))
		}
		return out, err
	})
}

// AddComment writes one general discussion comment.
func (s *Server) AddComment(ctx context.Context, r *pb.AddCommentRequest) (*pb.Comment, error) {
	return call(s, ctx, func(ctx context.Context, c *changes.Client) (*pb.Comment, error) {
		v, err := c.AddComment(ctx, ref(r), r.GetBody())
		return comment(v), err
	})
}

// GetReadiness exposes advisory forge conditions without enforcing them.
func (s *Server) GetReadiness(ctx context.Context, r *pb.ChangeRef) (*pb.Readiness, error) {
	return call(s, ctx, func(ctx context.Context, c *changes.Client) (*pb.Readiness, error) {
		v, err := c.Readiness(ctx, ref(r))
		out := &pb.Readiness{Revision: revision(v.Revision), Status: v.Status}
		for _, item := range v.Conditions {
			out.Conditions = append(out.Conditions, &pb.Condition{Name: item.Name, Status: item.Status, Detail: item.Detail})
		}
		return out, err
	})
}

// Merge asks the forge to merge, forwarding the atomic expected-head guard.
func (s *Server) Merge(ctx context.Context, r *pb.MergeRequest) (*pb.MergeResult, error) {
	return call(s, ctx, func(ctx context.Context, c *changes.Client) (*pb.MergeResult, error) {
		v, err := c.Merge(ctx, ref(r), changes.MergeOptions{Method: r.GetMethod(), ExpectedHead: r.GetExpectedHead()})
		return &pb.MergeResult{Merged: v.Merged, Commit: v.Commit}, err
	})
}
