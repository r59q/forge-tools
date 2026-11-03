package api

import (
	"context"

	"github.com/r59q/forge-tools/forge-code/library/code"
	pb "github.com/r59q/forge-tools/forge-code/service/codepb"
)

// ListSources lists source IDs and kinds, without URLs or credentials.
func (s *Server) ListSources(ctx context.Context, _ *pb.ListSourcesRequest) (*pb.ListSourcesResponse, error) {
	return call(s, ctx, "", func(_ context.Context, client *code.Client) (*pb.ListSourcesResponse, error) {
		result := &pb.ListSourcesResponse{}
		for _, src := range client.Sources() {
			result.Sources = append(result.Sources, &pb.Source{Id: src.ID, Kind: string(src.Session.Kind)})
		}

		return result, nil
	})
}

// GetCapabilities says what a source supports.
func (s *Server) GetCapabilities(ctx context.Context, r *pb.GetCapabilitiesRequest) (*pb.GetCapabilitiesResponse, error) {
	return call(s, ctx, "", func(_ context.Context, client *code.Client) (*pb.GetCapabilitiesResponse, error) {
		c, err := client.Capabilities(r.GetSource())
		out := &pb.Capabilities{TreeIds: c.TreeIDs, MergeBase: c.MergeBase, Writes: c.Writes}

		return &pb.GetCapabilitiesResponse{Capabilities: out}, err
	})
}

// Resolve returns the commit a revision names.
func (s *Server) Resolve(ctx context.Context, r *pb.ResolveRequest) (*pb.ResolveResponse, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.ResolveResponse, error) {
		v, err := client.Resolve(ctx, repo(r), r.GetRevision())
		return &pb.ResolveResponse{Commit: string(v.Commit), Ref: v.Ref}, err
	})
}

// ListRefs lists branches and tags.
func (s *Server) ListRefs(ctx context.Context, r *pb.ListRefsRequest) (*pb.ListRefsResponse, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.ListRefsResponse, error) {
		v, err := client.ListRefs(ctx, repo(r), code.RefsOptions{Prefix: r.GetPrefix(), Limit: int(r.GetLimit())})
		out := &pb.ListRefsResponse{DefaultBranch: v.Default, Truncated: v.Truncated}
		for _, ref := range v.Refs {
			out.Refs = append(out.Refs, &pb.Ref{Name: ref.Name, Target: string(ref.Target), Commit: string(ref.Commit)})
		}

		return out, err
	})
}

// GetCommit reads the commit a revision names.
func (s *Server) GetCommit(ctx context.Context, r *pb.GetCommitRequest) (*pb.GetCommitResponse, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.GetCommitResponse, error) {
		v, err := client.Commit(ctx, repo(r), r.GetRevision())
		return &pb.GetCommitResponse{Commit: commit(v)}, err
	})
}

// ListHistory returns one page of history.
func (s *Server) ListHistory(ctx context.Context, r *pb.ListHistoryRequest) (*pb.ListHistoryResponse, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.ListHistoryResponse, error) {
		v, err := client.History(ctx, repo(r), code.HistoryOptions{
			Revision: r.GetRevision(),
			Path:     r.GetPath(),
			PageSize: int(r.GetPageSize()),
			Cursor:   r.GetCursor(),
		})
		out := &pb.ListHistoryResponse{Tip: string(v.Tip), Next: v.Next}
		for _, c := range v.Commits {
			out.Commits = append(out.Commits, commit(c))
		}

		return out, err
	})
}

// GetTree lists a directory.
func (s *Server) GetTree(ctx context.Context, r *pb.GetTreeRequest) (*pb.GetTreeResponse, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.GetTreeResponse, error) {
		v, err := client.Tree(ctx, repo(r), r.GetRevision(), code.TreeOptions{
			Path:       r.GetPath(),
			Recursive:  r.GetRecursive(),
			MaxEntries: int(r.GetMaxEntries()),
		})
		out := &pb.Tree{
			Commit:    string(v.Commit),
			Id:        string(v.ID),
			Path:      v.Path,
			Truncated: v.Truncated,
		}
		for _, e := range v.Entries {
			out.Entries = append(out.Entries, entry(e))
		}

		return &pb.GetTreeResponse{Tree: out}, err
	})
}

// Stat describes one path.
func (s *Server) Stat(ctx context.Context, r *pb.StatRequest) (*pb.StatResponse, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.StatResponse, error) {
		v, err := client.Stat(ctx, repo(r), r.GetRevision(), r.GetPath())
		return &pb.StatResponse{Commit: string(v.Commit), Entry: entry(v.Entry)}, err
	})
}

// GetFile reads a file of at most MaxFileBytes.
func (s *Server) GetFile(ctx context.Context, r *pb.GetFileRequest) (*pb.GetFileResponse, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.GetFileResponse, error) {
		rd, err := client.ReadFile(ctx, repo(r), r.GetRevision(), r.GetPath())
		if err != nil {
			return nil, err
		}

		file, err := readAll(rd)

		return &pb.GetFileResponse{File: file}, err
	})
}

// GetBlob reads a blob of at most MaxFileBytes.
func (s *Server) GetBlob(ctx context.Context, r *pb.GetBlobRequest) (*pb.GetBlobResponse, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.GetBlobResponse, error) {
		rd, err := client.ReadBlob(ctx, repo(r), code.OID(r.GetId()))
		if err != nil {
			return nil, err
		}

		file, err := readAll(rd)

		return &pb.GetBlobResponse{File: file}, err
	})
}

// GetMergeBase returns the best common ancestor of two revisions.
func (s *Server) GetMergeBase(ctx context.Context, r *pb.GetMergeBaseRequest) (*pb.GetMergeBaseResponse, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.GetMergeBaseResponse, error) {
		id, err := client.MergeBase(ctx, repo(r), r.GetA(), r.GetB())
		return &pb.GetMergeBaseResponse{Commit: string(id)}, err
	})
}

// Compare lists the changes between two revisions.
func (s *Server) Compare(ctx context.Context, r *pb.CompareRequest) (*pb.CompareResponse, error) {
	return call(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.CompareResponse, error) {
		v, err := client.Compare(ctx, repo(r), r.GetBase(), r.GetHead(), compareOptions(r))
		out := &pb.Diff{
			Base:      string(v.Base),
			Head:      string(v.Head),
			MergeBase: string(v.MergeBase),
			Truncated: v.Truncated,
		}
		for _, c := range v.Changes {
			out.Changes = append(out.Changes, change(c))
		}

		return &pb.CompareResponse{Diff: out}, err
	})
}

// CreateCommit makes a commit and moves a ref to it.
func (s *Server) CreateCommit(ctx context.Context, r *pb.CreateCommitRequest) (*pb.CreateCommitResponse, error) {
	return write(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.CreateCommitResponse, error) {
		n, err := newCommit(r)
		if err != nil {
			return nil, err
		}

		v, err := client.CreateCommit(ctx, repo(r), n)

		return &pb.CreateCommitResponse{Commit: commit(v)}, err
	})
}

// UpdateRefs creates, moves and deletes refs atomically.
func (s *Server) UpdateRefs(ctx context.Context, r *pb.UpdateRefsRequest) (*pb.UpdateRefsResponse, error) {
	return write(s, ctx, r.GetSource(), func(ctx context.Context, client *code.Client) (*pb.UpdateRefsResponse, error) {
		return &pb.UpdateRefsResponse{}, client.UpdateRefs(ctx, repo(r), refUpdates(r)...)
	})
}
