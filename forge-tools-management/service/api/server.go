// Package api exposes management over REST, gRPC and a server-rendered web UI.
package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-issues/library/issues"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server authenticates every management RPC and web request. It does not log
// requests, responses or errors from the backend.
type Server struct {
	pb.UnimplementedManagementServiceServer
	client  *management.Client
	token   forgeauth.Credentials
	timeout time.Duration
	views   views
}

// New constructs a server with a rotating operator credential source and a
// positive request timeout. This token must differ from backend credentials.
func New(client *management.Client, token forgeauth.Credentials, timeout time.Duration) (*Server, error) {
	if client == nil || token == nil || timeout <= 0 {
		return nil, errors.New("management API: invalid configuration")
	}

	return &Server{client: client, token: token, timeout: timeout, views: parseViews()}, nil
}

// call authorizes the request and runs fn with the request timeout. Errors
// carry only a status code.
func call[T any](s *Server, ctx context.Context, fn func(context.Context) (*T, error)) (*T, error) {
	return servicekit.Call(ctx, servicekit.RPC[*management.Client]{
		Timeout: s.timeout,
		Token:   s.token,
		Client:  s.managementClient,
		Code:    code,
	}, func(ctx context.Context, _ *management.Client) (*T, error) {
		return fn(ctx)
	})
}

// managementClient returns the client every RPC uses.
func (s *Server) managementClient(context.Context) (*management.Client, error) {
	return s.client, nil
}

// code maps management errors, then context errors, to a status code.
func code(err error) codes.Code {
	switch {
	case errors.Is(err, management.ErrNotConfigured), errors.Is(err, management.ErrPipelineUnsupported):
		return codes.Unimplemented
	case errors.Is(err, management.ErrInvalid):
		return codes.InvalidArgument
	case errors.Is(err, management.ErrNotFound):
		return codes.NotFound
	case errors.Is(err, management.ErrCheckFailed):
		return codes.FailedPrecondition
	}

	return servicekit.ForgeCode(err)
}

// ListRepositories returns the configured state and complete repository list.
// Backend credential rejection is Unavailable, never Unauthenticated.
func (s *Server) ListRepositories(ctx context.Context, _ *pb.ListRepositoriesRequest) (*pb.ListRepositoriesResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ListRepositoriesResponse, error) {
		listing, err := s.client.ListRepositories(ctx)
		if err != nil {
			return nil, err
		}

		out := &pb.ListRepositoriesResponse{Configured: listing.Configured}
		for _, r := range listing.Repositories {
			out.Repositories = append(out.Repositories, &pb.Repository{
				SourceId:    r.Source.ID,
				Forge:       string(r.Source.Kind),
				Host:        r.Source.Host,
				Path:        r.Path,
				Description: r.Description,
				Visibility:  string(r.Visibility),
			})
		}

		return out, nil
	})
}

// ListIssues returns the configured state and complete issue list. A
// malformed filter is InvalidArgument; backend credential rejection is
// Unavailable, never Unauthenticated.
func (s *Server) ListIssues(ctx context.Context, r *pb.ListIssuesRequest) (*pb.ListIssuesResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ListIssuesResponse, error) {
		listing, err := s.client.ListIssues(ctx, management.IssueQuery{
			Scope:      issues.Scope(r.GetScope()),
			State:      issues.StateFilter(r.GetState()),
			Source:     r.GetSource(),
			Repository: r.GetRepository(),
		})
		if err != nil {
			return nil, err
		}

		out := &pb.ListIssuesResponse{Configured: listing.Configured}
		for _, i := range listing.Issues {
			v := &pb.Issue{
				SourceId:   i.Source.ID,
				Forge:      string(i.Source.Kind),
				Host:       i.Source.Host,
				Repository: i.Repository,
				Number:     i.Number,
				Title:      i.Title,
				State:      string(i.State),
				Author:     i.Author,
				Assignees:  i.Assignees,
				Labels:     i.Labels,
				Comments:   clamp32(i.Comments),
			}
			if !i.UpdatedAt.IsZero() {
				v.UpdatedAt = timestamppb.New(i.UpdatedAt)
			}

			out.Issues = append(out.Issues, v)
		}

		return out, nil
	})
}

// ListConnections returns the configured state and every connection.
func (s *Server) ListConnections(ctx context.Context, _ *pb.ListConnectionsRequest) (*pb.ListConnectionsResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.ListConnectionsResponse, error) {
		list, err := s.client.ListConnections(ctx)
		if err != nil {
			return nil, err
		}

		out := &pb.ListConnectionsResponse{Configured: list.Configured}
		for _, c := range list.Connections {
			out.Connections = append(out.Connections, connection(c))
		}

		return out, nil
	})
}

// AddConnection checks and stores a new forge credential.
func (s *Server) AddConnection(ctx context.Context, r *pb.AddConnectionRequest) (*pb.AddConnectionResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.AddConnectionResponse, error) {
		c, err := s.client.AddConnection(ctx, management.NewConnection{
			Kind:       forgeauth.Kind(r.GetForge()),
			Host:       r.GetHost(),
			Name:       r.GetName(),
			Credential: forgeauth.StaticToken(r.GetCredential()),
		})
		return &pb.AddConnectionResponse{Connection: connection(c)}, err
	})
}

// TestConnection rechecks a stored credential with its forge.
func (s *Server) TestConnection(ctx context.Context, r *pb.TestConnectionRequest) (*pb.TestConnectionResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.TestConnectionResponse, error) {
		c, err := s.client.TestConnection(ctx, r.GetId())
		return &pb.TestConnectionResponse{Connection: connection(c)}, err
	})
}

// RotateCredential replaces a connection's credential.
func (s *Server) RotateCredential(ctx context.Context, r *pb.RotateCredentialRequest) (*pb.RotateCredentialResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.RotateCredentialResponse, error) {
		c, err := s.client.RotateCredential(ctx, r.GetId(), forgeauth.StaticToken(r.GetCredential()))
		return &pb.RotateCredentialResponse{Connection: connection(c)}, err
	})
}

// RemoveConnection deletes a connection and its credential.
func (s *Server) RemoveConnection(ctx context.Context, r *pb.RemoveConnectionRequest) (*pb.RemoveConnectionResponse, error) {
	return call(s, ctx, func(ctx context.Context) (*pb.RemoveConnectionResponse, error) {
		return &pb.RemoveConnectionResponse{}, s.client.RemoveConnection(ctx, r.GetId())
	})
}

func connection(c management.Connection) *pb.Connection {
	out := &pb.Connection{
		Id:      c.ID,
		Name:    c.Name,
		Forge:   string(c.Kind),
		BaseUrl: c.BaseURL,
		Login:   c.Login,
		Status:  string(c.Status),
	}
	if !c.CheckedAt.IsZero() {
		out.CheckedAt = timestamppb.New(c.CheckedAt)
	}

	return out
}

// contentSecurityPolicy allows only same-origin styles and form posts.
const contentSecurityPolicy = "default-src 'none'; style-src 'self'; frame-ancestors 'none'; " +
	"base-uri 'none'; form-action 'self'"

// HTTP registers the generated REST endpoints and the web UI. REST accepts
// Bearer auth; the UI uses HTTP Basic with username operator and the operator
// token as password. Authorization is the sole credential header accepted.
// Cross-origin browser requests that change state are rejected.
func (s *Server) HTTP(ctx context.Context) (http.Handler, error) {
	rest := servicekit.NewMux(true)
	if err := pb.RegisterManagementServiceHandlerServer(ctx, rest, s); err != nil {
		return nil, err
	}

	pages := s.pages()
	origins := http.NewCrossOriginProtection()
	routes := origins.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			s.web(pages, w, r)
			return
		}

		if (r.URL.Path == "/v1/repositories" || r.URL.Path == "/v1/issues") && r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)

			return
		}

		rest.ServeHTTP(w, r)
	}))

	return servicekit.Secure(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		routes.ServeHTTP(w, r)
	})), nil
}
