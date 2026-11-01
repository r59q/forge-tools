// Package api exposes a repositories.Client over gRPC and generated REST
// routes.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-repositories/library/repositories"
	pb "github.com/r59q/forge-tools/forge-repositories/service/repositoriespb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ErrInvalid means New was given a nil client or credential source, or a
// nonpositive timeout.
var ErrInvalid = errors.New("api: invalid configuration")

// Server authenticates every RPC, including direct grpc-gateway dispatch.
// It never logs requests, responses, credentials or upstream errors, and
// returns only a status code to callers.
type Server struct {
	pb.UnimplementedRepositoriesServer
	client  *repositories.Client
	token   forgeauth.Credentials
	timeout time.Duration
}

// New constructs a service for client. Callers must present the token from
// token as Authorization: Bearer. Each RPC runs for at most timeout.
// Runtime configuration should use forgeauth.FileToken or forgeauth.EnvToken.
func New(client *repositories.Client, token forgeauth.Credentials, timeout time.Duration) (*Server, error) {
	if client == nil || token == nil || timeout <= 0 {
		return nil, ErrInvalid
	}
	return &Server{client: client, token: token, timeout: timeout}, nil
}

// REST registers generated routes. Authorization is the sole forwarded header;
// Grpc-Metadata-* headers cannot inject credentials. Errors never echo input
// or forge responses. Responses are not cached.
func (s *Server) REST(ctx context.Context) (http.Handler, error) {
	mux := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: false}}),
		runtime.WithIncomingHeaderMatcher(func(string) (string, bool) { return "", false }),
		runtime.WithErrorHandler(func(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
			code := status.Code(err)
			if code == codes.Unknown {
				code = codes.Internal
			}
			runtime.DefaultHTTPErrorHandler(ctx, mux, marshaler, w, r, status.Error(code, code.String()))
		}),
	)
	if err := pb.RegisterRepositoriesHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		mux.ServeHTTP(w, r)
	}), nil
}

func (s *Server) authorize(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	h := http.Header{}
	for _, value := range md.Get("authorization") {
		h.Add("Authorization", value)
	}
	token, ok := forgeauth.AuthorizationToken(h, "Bearer")
	if !ok {
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	expected, err := s.token.Token(ctx)
	if err != nil || expected == "" {
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	a, b := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(expected))
	if subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	return nil
}

func call[T any](s *Server, ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.authorize(ctx); err != nil {
		return zero, err
	}
	result, err := fn(ctx)
	if err != nil {
		return zero, rpcError(err)
	}
	return result, nil
}

// rpcError maps library and forge errors to a bare status code. A forge
// rejecting the service's own credentials is Unavailable, not
// Unauthenticated, so callers do not mistake it for a bad service token.
func rpcError(err error) error {
	code := codes.Unavailable
	var forgeErr *forgehttp.Error
	switch {
	case errors.Is(err, context.Canceled):
		code = codes.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		code = codes.DeadlineExceeded
	case errors.Is(err, repositories.ErrInvalid), errors.Is(err, repositories.ErrUnknownSource):
		code = codes.InvalidArgument
	case errors.Is(err, repositories.ErrAmbiguous):
		code = codes.FailedPrecondition
	case errors.Is(err, forgehttp.ErrNotFound):
		code = codes.NotFound
	case errors.Is(err, forgehttp.ErrForbidden):
		code = codes.PermissionDenied
	case errors.Is(err, forgehttp.ErrRateLimited):
		code = codes.ResourceExhausted
	case errors.Is(err, forgehttp.ErrConflict):
		code = codes.FailedPrecondition
	case errors.As(err, &forgeErr) && (forgeErr.StatusCode == http.StatusBadRequest || forgeErr.StatusCode == http.StatusUnprocessableEntity):
		// The forge rejected the update, such as an unknown default branch.
		code = codes.InvalidArgument
	}
	return status.Error(code, code.String())
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
func (s *Server) ListSources(ctx context.Context, _ *emptypb.Empty) (*pb.ListSourcesResponse, error) {
	return call(s, ctx, func(context.Context) (*pb.ListSourcesResponse, error) {
		result := &pb.ListSourcesResponse{}
		for _, src := range s.client.Sources() {
			result.Sources = append(result.Sources, &pb.Source{Id: src.ID, Kind: string(src.Session.Kind)})
		}
		return result, nil
	})
}

// ListRepositories lists repositories across sources.
func (s *Server) ListRepositories(ctx context.Context, r *pb.ListRepositoriesRequest) (*pb.ListRepositoriesResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ListRepositoriesResponse, error) {
		values, err := s.client.List(ctx, repositories.ListOptions{Owner: r.GetOwner(), Source: r.GetSource()})
		result := &pb.ListRepositoriesResponse{}
		for _, v := range values {
			result.Repositories = append(result.Repositories, repository(v))
		}
		return result, err
	})
}

// GetRepository reads one repository by path.
func (s *Server) GetRepository(ctx context.Context, r *pb.RepositoryRequest) (*pb.Repository, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.Repository, error) {
		v, err := s.client.Get(ctx, ref(r))
		return repository(v), err
	})
}

// UpdateRepository changes the description or default branch.
func (s *Server) UpdateRepository(ctx context.Context, r *pb.UpdateRepositoryRequest) (*pb.Repository, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.Repository, error) {
		v, err := s.client.Update(ctx, ref(r), repositories.Update{Description: r.Description, DefaultBranch: r.DefaultBranch})
		return repository(v), err
	})
}

// ListBranches lists branches with the default branch marked.
func (s *Server) ListBranches(ctx context.Context, r *pb.RepositoryRequest) (*pb.ListBranchesResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ListBranchesResponse, error) {
		values, err := s.client.Branches(ctx, ref(r))
		result := &pb.ListBranchesResponse{}
		for _, b := range values {
			result.Branches = append(result.Branches, &pb.Branch{Name: b.Name, Sha: b.SHA, Default: b.Default, Protected: b.Protected})
		}
		return result, err
	})
}

// ListTags lists tags.
func (s *Server) ListTags(ctx context.Context, r *pb.RepositoryRequest) (*pb.ListTagsResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ListTagsResponse, error) {
		values, err := s.client.Tags(ctx, ref(r))
		result := &pb.ListTagsResponse{}
		for _, t := range values {
			result.Tags = append(result.Tags, &pb.Tag{Name: t.Name, Sha: t.SHA})
		}
		return result, err
	})
}
