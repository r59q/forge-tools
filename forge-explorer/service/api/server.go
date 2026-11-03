// Package api exposes explorer library operations through generated REST and gRPC routes.
package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-explorer/library/explorer"
	pb "github.com/r59q/forge-tools/forge-explorer/service/explorerpb"
	"google.golang.org/grpc/codes"
)

// Server serves a fixed set of roots. The owner closes them after serving stops.
type Server struct {
	pb.UnimplementedExplorerServiceServer
	roots   map[string]*explorer.Worktree
	token   forgeauth.Credentials
	timeout time.Duration
}

// New validates public source uniqueness and authentication configuration.
func New(roots []*explorer.Worktree, token forgeauth.Credentials, timeout time.Duration) (*Server, error) {
	if token == nil || timeout <= 0 || len(roots) == 0 || len(roots) > 32 {
		return nil, explorer.ErrInvalid
	}
	s := &Server{roots: make(map[string]*explorer.Worktree), token: token, timeout: timeout}
	for _, root := range roots {
		if root == nil || s.roots[root.Source().ID] != nil {
			return nil, explorer.ErrInvalid
		}
		s.roots[root.Source().ID] = root
	}
	return s, nil
}

// REST registers generated routes with strict JSON, bearer authentication and bounded bodies.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	mux := servicekit.NewMux(true)
	if err := pb.RegisterExplorerServiceHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}
	return servicekit.Secure(mux), nil
}

func call[T any](s *Server, ctx context.Context, fn func(context.Context, *Server) (*T, error)) (*T, error) {
	return servicekit.Call(ctx, servicekit.RPC[*Server]{
		Timeout: s.timeout, Token: s.token,
		Client: func(context.Context) (*Server, error) { return s, nil }, Code: code,
	}, fn)
}

func (s *Server) root(id, version string) (*explorer.Worktree, error) {
	root := s.roots[id]
	if root == nil {
		return nil, explorer.ErrNotFound
	}
	if version == "" {
		return nil, explorer.ErrInvalid
	}
	if root.Source().Version != version {
		return nil, explorer.ErrChanged
	}
	return root, nil
}

func code(err error) codes.Code {
	switch {
	case errors.Is(err, explorer.ErrInvalid):
		return codes.InvalidArgument
	case errors.Is(err, explorer.ErrNotFound):
		return codes.NotFound
	case errors.Is(err, explorer.ErrForbidden):
		return codes.PermissionDenied
	case errors.Is(err, explorer.ErrUnsupported):
		return codes.Unimplemented
	case errors.Is(err, explorer.ErrTooLarge):
		return codes.ResourceExhausted
	case errors.Is(err, explorer.ErrChanged):
		return codes.FailedPrecondition
	default:
		return servicekit.ForgeCode(err)
	}
}

// ListSources returns capabilities and limits in stable ID order.
func (s *Server) ListSources(ctx context.Context, _ *pb.ListSourcesRequest) (*pb.ListSourcesResponse, error) {
	return call(s, ctx, func(_ context.Context, s *Server) (*pb.ListSourcesResponse, error) {
		out := &pb.ListSourcesResponse{}
		ids := make([]string, 0, len(s.roots))
		for id := range s.roots {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			out.Sources = append(out.Sources, source(s.roots[id].Source()))
		}
		return out, nil
	})
}

// Browse returns bounded directory children.
func (s *Server) Browse(ctx context.Context, r *pb.BrowseRequest) (*pb.BrowseResponse, error) {
	return call(s, ctx, func(ctx context.Context, s *Server) (*pb.BrowseResponse, error) {
		w, err := s.root(r.GetSource(), r.GetSourceVersion())
		if err != nil {
			return nil, err
		}
		d, err := w.Browse(ctx, r.GetPath())
		if err != nil {
			return nil, err
		}
		out := &pb.Directory{Source: d.Source, Version: d.Version, State: state(d.State)}
		for _, e := range d.Entries {
			out.Entries = append(out.Entries, &pb.Entry{Path: e.Path, Kind: e.Kind})
		}
		return &pb.BrowseResponse{Directory: out}, nil
	})
}

// Read returns exact observed text and its content identity.
func (s *Server) Read(ctx context.Context, r *pb.ReadRequest) (*pb.ReadResponse, error) {
	return call(s, ctx, func(ctx context.Context, s *Server) (*pb.ReadResponse, error) {
		w, err := s.root(r.GetSource(), r.GetSourceVersion())
		if err != nil {
			return nil, err
		}
		f, err := w.Read(ctx, r.GetPath(), r.GetExpectedVersion())
		if err != nil {
			return nil, err
		}
		return &pb.ReadResponse{File: &pb.File{Location: location(f.Location), Text: f.Text}}, nil
	})
}

// Search returns bounded, versioned results without invoking repository tools.
func (s *Server) Search(ctx context.Context, r *pb.SearchRequest) (*pb.SearchResponse, error) {
	return call(s, ctx, func(ctx context.Context, s *Server) (*pb.SearchResponse, error) {
		w, err := s.root(r.GetSource(), r.GetSourceVersion())
		if err != nil {
			return nil, err
		}
		result, err := w.Search(ctx, explorer.Query{Text: r.GetText(), PathGlob: r.GetPathGlob(), Files: r.GetFiles(), Limit: int(r.GetLimit())})
		if err != nil {
			return nil, err
		}
		out := &pb.Results{State: state(result.State)}
		for _, m := range result.Matches {
			out.Matches = append(out.Matches, &pb.Match{Location: location(m.Location), Preview: m.Preview})
		}
		return &pb.SearchResponse{Results: out}, nil
	})
}
