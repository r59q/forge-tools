// Package api exposes issues over gRPC and generated REST routes.
package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-issues/library/issues"
	pb "github.com/r59q/forge-tools/forge-issues/service/issuespb"
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
	pb.UnimplementedIssuesServiceServer
	sources Sources
	token   forgeauth.Credentials
	timeout time.Duration
}

// jscpd:ignore-start - each service wraps its own library client.

// Sources returns a client for the current sources. The server calls it once
// per RPC, so sources may change while it runs. Errors map to Unavailable
// unless they wrap a context error.
type Sources func(ctx context.Context) (*issues.Client, error)

// Fixed returns Sources that always use client.
func Fixed(client *issues.Client) Sources {
	return func(context.Context) (*issues.Client, error) { return client, nil }
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
	if err := pb.RegisterIssuesServiceHandlerServer(ctx, mux, s); err != nil {
		return nil, err
	}

	return servicekit.Secure(mux), nil
}

// call runs one authorized unary RPC within the timeout.
func call[T any](s *Server, ctx context.Context, fn func(context.Context, *issues.Client) (*T, error)) (*T, error) {
	return servicekit.Call(ctx, servicekit.RPC[*issues.Client]{
		Timeout: s.timeout,
		Token:   s.token,
		Client:  s.sources,
		Code:    code,
	}, fn)
}

// rpcError maps library and forge errors to a bare status.
func rpcError(err error) error {
	return servicekit.Status(code(err))
}

// code maps context errors, then library errors, then forge errors, to a
// status code.
func code(err error) codes.Code {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return servicekit.ForgeCode(err)
	}

	switch {
	case errors.Is(err, issues.ErrInvalid), errors.Is(err, issues.ErrUnknownSource):
		return codes.InvalidArgument
	case errors.Is(err, issues.ErrAmbiguous):
		return codes.FailedPrecondition
	}

	return servicekit.ForgeCode(err)
}

func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}

	return timestamppb.New(t)
}

// count converts a count to int32, clamped to its range.
func count(n int) int32 {
	return int32(max(0, min(n, math.MaxInt32)))
}

func user(u issues.User) *pb.User {
	return &pb.User{Login: u.Login, WebUrl: u.WebURL}
}

func issue(i issues.Issue) *pb.Issue {
	out := &pb.Issue{
		Source:     &pb.SourceInfo{Id: i.Source.ID, Kind: string(i.Source.Kind), Host: i.Source.Host},
		Repository: i.Repository,
		Id:         i.ID,
		Number:     i.Number,
		Title:      i.Title,
		Body:       i.Body,
		State:      string(i.State),
		Labels:     i.Labels,
		Milestone:  i.Milestone,
		Comments:   count(i.Comments),
		Locked:     i.Locked,
		WebUrl:     i.WebURL,
		CreatedAt:  timestamp(i.CreatedAt),
		UpdatedAt:  timestamp(i.UpdatedAt),
		ClosedAt:   timestamp(i.ClosedAt),
	}
	if i.Author != (issues.User{}) {
		out.Author = user(i.Author)
	}

	for _, a := range i.Assignees {
		out.Assignees = append(out.Assignees, user(a))
	}

	return out
}

func comment(c issues.Comment) *pb.Comment {
	out := &pb.Comment{
		Id:        c.ID,
		Body:      c.Body,
		WebUrl:    c.WebURL,
		CreatedAt: timestamp(c.CreatedAt),
		UpdatedAt: timestamp(c.UpdatedAt),
	}
	if c.Author != (issues.User{}) {
		out.Author = user(c.Author)
	}

	return out
}

func ref(r interface {
	GetNumber() int64
	GetRepository() string
	GetSource() string
}) issues.Ref {
	return issues.Ref{Source: r.GetSource(), Repository: r.GetRepository(), Number: r.GetNumber()}
}

// list returns l's values, or nil if l is unset. A set but empty list
// clears.
func list(l *pb.StringList) *[]string {
	if l == nil {
		return nil
	}

	values := append([]string{}, l.GetValues()...)

	return &values
}

// ListSources lists source IDs and kinds, without URLs or credentials.
func (s *Server) ListSources(ctx context.Context, _ *pb.ListSourcesRequest) (*pb.ListSourcesResponse, error) {
	return call(s, ctx, func(_ context.Context, client *issues.Client) (*pb.ListSourcesResponse, error) {
		result := &pb.ListSourcesResponse{}
		for _, src := range client.Sources() {
			result.Sources = append(result.Sources, &pb.Source{Id: src.ID, Kind: string(src.Session.Kind)})
		}

		return result, nil
	})
}

// ListIssues lists issues across sources.
func (s *Server) ListIssues(ctx context.Context, r *pb.ListIssuesRequest) (*pb.ListIssuesResponse, error) {
	return call(s, ctx, func(ctx context.Context, client *issues.Client) (*pb.ListIssuesResponse, error) {
		values, err := client.List(ctx, issues.ListOptions{
			Source:     r.GetSource(),
			Repository: r.GetRepository(),
			State:      issues.StateFilter(r.GetState()),
			Labels:     r.GetLabels(),
			Scope:      issues.Scope(r.GetScope()),
		})
		result := &pb.ListIssuesResponse{}
		for _, v := range values {
			result.Issues = append(result.Issues, issue(v))
		}

		return result, err
	})
}

// GetIssue reads one issue.
func (s *Server) GetIssue(ctx context.Context, r *pb.GetIssueRequest) (*pb.GetIssueResponse, error) {
	return call(s, ctx, func(ctx context.Context, client *issues.Client) (*pb.GetIssueResponse, error) {
		v, err := client.Get(ctx, ref(r))
		return &pb.GetIssueResponse{Issue: issue(v)}, err
	})
}

// CreateIssue opens an issue.
func (s *Server) CreateIssue(ctx context.Context, r *pb.CreateIssueRequest) (*pb.CreateIssueResponse, error) {
	return call(s, ctx, func(ctx context.Context, client *issues.Client) (*pb.CreateIssueResponse, error) {
		v, err := client.Create(ctx, issues.RepositoryRef{Source: r.GetSource(), Repository: r.GetRepository()}, issues.NewIssue{
			Title:     r.GetTitle(),
			Body:      r.GetBody(),
			Labels:    r.GetLabels(),
			Assignees: r.GetAssignees(),
		})
		return &pb.CreateIssueResponse{Issue: issue(v)}, err
	})
}

// UpdateIssue changes an issue. Unset fields are left unchanged.
func (s *Server) UpdateIssue(ctx context.Context, r *pb.UpdateIssueRequest) (*pb.UpdateIssueResponse, error) {
	return call(s, ctx, func(ctx context.Context, client *issues.Client) (*pb.UpdateIssueResponse, error) {
		u := issues.Update{
			Title:     r.Title,
			Body:      r.Body,
			Labels:    list(r.Labels),
			Assignees: list(r.Assignees),
		}
		if r.State != nil {
			state := issues.State(*r.State)
			u.State = &state
		}

		v, err := client.Update(ctx, ref(r), u)

		return &pb.UpdateIssueResponse{Issue: issue(v)}, err
	})
}

// ListComments lists an issue's comments, oldest first.
func (s *Server) ListComments(ctx context.Context, r *pb.ListCommentsRequest) (*pb.ListCommentsResponse, error) {
	return call(s, ctx, func(ctx context.Context, client *issues.Client) (*pb.ListCommentsResponse, error) {
		values, err := client.Comments(ctx, ref(r))
		result := &pb.ListCommentsResponse{}
		for _, v := range values {
			result.Comments = append(result.Comments, comment(v))
		}

		return result, err
	})
}

// AddComment adds a comment to an issue.
func (s *Server) AddComment(ctx context.Context, r *pb.AddCommentRequest) (*pb.AddCommentResponse, error) {
	return call(s, ctx, func(ctx context.Context, client *issues.Client) (*pb.AddCommentResponse, error) {
		v, err := client.AddComment(ctx, ref(r), r.GetBody())
		return &pb.AddCommentResponse{Comment: comment(v)}, err
	})
}
