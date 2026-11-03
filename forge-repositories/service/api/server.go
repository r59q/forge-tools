// Package api exposes repositories over gRPC and generated REST routes.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-repositories/library/repositories"
	pb "github.com/r59q/forge-tools/forge-repositories/service/repositoriespb"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ErrInvalid means New was given nil sources or credential source, or a
// nonpositive timeout.
var ErrInvalid = errors.New("api: invalid configuration")

// Server authenticates every RPC, including direct grpc-gateway dispatch.
// It never logs requests, responses, credentials or upstream errors, and
// returns only a status code to callers.
type Server struct {
	pb.UnimplementedRepositoriesServiceServer
	sources Sources
	token   forgeauth.Credentials
	timeout time.Duration
}

// jscpd:ignore-start - each service wraps its own library client.

// Sources returns a client for the current sources. The server calls it once
// per RPC, so sources may change while it runs. Errors map to Unavailable
// unless they wrap a context error.
type Sources func(ctx context.Context) (*repositories.Client, error)

// Fixed returns Sources that always use client.
func Fixed(client *repositories.Client) Sources {
	return func(context.Context) (*repositories.Client, error) { return client, nil }
}

// New constructs a service over sources. Callers must present the token from
// token as Authorization: Bearer. Each RPC runs for at most timeout.
// Runtime configuration should use forgeauth.FileToken or forgeauth.EnvToken.
func New(sources Sources, token forgeauth.Credentials, timeout time.Duration) (*Server, error) {
	if sources == nil || token == nil || timeout <= 0 {
		return nil, ErrInvalid
	}

	return &Server{sources: sources, token: token, timeout: timeout}, nil
}

// jscpd:ignore-end

// REST registers generated routes. Authorization is the sole forwarded header;
// Grpc-Metadata-* headers cannot inject credentials. Errors never echo input
// or forge responses. Responses are not cached.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	mux := servicekit.NewMux(false)
	if err := pb.RegisterRepositoriesServiceHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}

	return servicekit.Secure(mux), nil
}

// call runs one authorized unary RPC within the timeout.
func call[T any](s *Server, ctx context.Context, fn func(context.Context, *repositories.Client) (*T, error)) (*T, error) {
	return servicekit.Call(ctx, servicekit.RPC[*repositories.Client]{
		Timeout: s.timeout,
		Token:   s.token,
		Client:  s.sources,
		Code:    code,
	}, fn)
}

// code maps context errors, then library errors, then forge errors, to a
// status code.
func code(err error) codes.Code {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return servicekit.ForgeCode(err)
	}

	switch {
	case errors.Is(err, repositories.ErrInvalid), errors.Is(err, repositories.ErrUnknownSource):
		return codes.InvalidArgument
	case errors.Is(err, repositories.ErrAmbiguous):
		return codes.FailedPrecondition
	}

	return servicekit.ForgeCode(err)
}

func repository(r repositories.Repository) *pb.Repository {
	out := &pb.Repository{
		Source:        &pb.SourceInfo{Id: r.Source.ID, Kind: string(r.Source.Kind), Host: r.Source.Host},
		Id:            r.ID,
		Path:          r.Path,
		Owner:         r.Owner,
		Name:          r.Name,
		Description:   r.Description,
		DefaultBranch: r.DefaultBranch,
		Visibility:    string(r.Visibility),
		Archived:      r.Archived,
		Fork:          r.Fork,
		WebUrl:        r.WebURL,
		CloneUrl:      r.CloneURL,
		SshUrl:        r.SSHURL,
	}
	if !r.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(r.CreatedAt)
	}
	if !r.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(r.UpdatedAt)
	}

	return out
}

func ref(r interface {
	GetPath() string
	GetSource() string
}) repositories.Ref {
	return repositories.Ref{Source: r.GetSource(), Path: r.GetPath()}
}

// ListSources lists source IDs and kinds, without URLs or credentials.
func (s *Server) ListSources(ctx context.Context, _ *pb.ListSourcesRequest) (*pb.ListSourcesResponse, error) {
	return call(s, ctx, func(_ context.Context, client *repositories.Client) (*pb.ListSourcesResponse, error) {
		result := &pb.ListSourcesResponse{}
		for _, src := range client.Sources() {
			result.Sources = append(result.Sources, &pb.Source{Id: src.ID, Kind: string(src.Session.Kind)})
		}

		return result, nil
	})
}

// ListRepositories lists repositories across sources.
func (s *Server) ListRepositories(ctx context.Context, r *pb.ListRepositoriesRequest) (*pb.ListRepositoriesResponse, error) {
	return call(s, ctx, func(ctx context.Context, client *repositories.Client) (*pb.ListRepositoriesResponse, error) {
		values, err := client.List(ctx, repositories.ListOptions{Owner: r.GetOwner(), Source: r.GetSource()})
		result := &pb.ListRepositoriesResponse{}
		for _, v := range values {
			result.Repositories = append(result.Repositories, repository(v))
		}

		return result, err
	})
}

// GetRepository reads one repository by path.
func (s *Server) GetRepository(ctx context.Context, r *pb.GetRepositoryRequest) (*pb.GetRepositoryResponse, error) {
	return call(s, ctx, func(ctx context.Context, client *repositories.Client) (*pb.GetRepositoryResponse, error) {
		v, err := client.Get(ctx, ref(r))
		return &pb.GetRepositoryResponse{Repository: repository(v)}, err
	})
}

// UpdateRepository changes the description or default branch.
func (s *Server) UpdateRepository(ctx context.Context, r *pb.UpdateRepositoryRequest) (*pb.UpdateRepositoryResponse, error) {
	return call(s, ctx, func(ctx context.Context, client *repositories.Client) (*pb.UpdateRepositoryResponse, error) {
		v, err := client.Update(ctx, ref(r), repositories.Update{Description: r.Description, DefaultBranch: r.DefaultBranch})
		return &pb.UpdateRepositoryResponse{Repository: repository(v)}, err
	})
}

// ListBranches lists branches with the default branch marked.
func (s *Server) ListBranches(ctx context.Context, r *pb.ListBranchesRequest) (*pb.ListBranchesResponse, error) {
	return call(s, ctx, func(ctx context.Context, client *repositories.Client) (*pb.ListBranchesResponse, error) {
		values, err := client.Branches(ctx, ref(r))
		result := &pb.ListBranchesResponse{}
		for _, b := range values {
			result.Branches = append(result.Branches, &pb.Branch{
				Name:      b.Name,
				Sha:       b.SHA,
				Default:   b.Default,
				Protected: b.Protected,
			})
		}

		return result, err
	})
}

// ListTags lists tags.
func (s *Server) ListTags(ctx context.Context, r *pb.ListTagsRequest) (*pb.ListTagsResponse, error) {
	return call(s, ctx, func(ctx context.Context, client *repositories.Client) (*pb.ListTagsResponse, error) {
		values, err := client.Tags(ctx, ref(r))
		result := &pb.ListTagsResponse{}
		for _, t := range values {
			result.Tags = append(result.Tags, &pb.Tag{Name: t.Name, Sha: t.SHA})
		}

		return result, err
	})
}
