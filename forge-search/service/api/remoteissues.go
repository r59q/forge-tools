package api

import (
	"context"
	"errors"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-issues/library/issues"
	"github.com/r59q/forge-tools/forge-issues/service/issuespb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MaxIssueResponseBytes is the largest ListIssues response RemoteIssues
// receives. forge-issues returns all of a repository's issues in one
// response; a larger one fails with ErrIssuesExhausted.
const MaxIssueResponseBytes = 64 << 20

// Errors RemoteIssues returns, besides ErrNotFound for an unknown source or
// repository and context errors. Each maps to one status code.
var (
	// ErrIssuesRejected means forge-issues refused the source or
	// repository as invalid, or as matching several sources.
	ErrIssuesRejected = errors.New("api: issue source rejected")

	// ErrIssuesDenied means the forge refused access to the issues.
	ErrIssuesDenied = errors.New("api: issue access denied")

	// ErrIssuesExhausted means the listing is over MaxIssueResponseBytes,
	// or the forge rate limit was reached.
	ErrIssuesExhausted = errors.New("api: issue listing over a limit")

	errIssuesUnavailable = errors.New("api: issues service unavailable")
)

// RemoteIssues lists issues with the forge-issues gRPC API.
type RemoteIssues struct {
	client issuespb.IssuesServiceClient
	token  forgeauth.Credentials

	// maxBytes is the receive limit, MaxIssueResponseBytes but in tests.
	maxBytes int
}

// NewRemoteIssues returns issues listed through client, authenticated with
// the token from token. The token is read on every call, so it can rotate.
func NewRemoteIssues(client issuespb.IssuesServiceClient, token forgeauth.Credentials) *RemoteIssues {
	return &RemoteIssues{client: client, token: token, maxBytes: MaxIssueResponseBytes}
}

// List lists issues as forge-issues ListIssues does. Errors never hold the
// token or the response of forge-issues.
func (r *RemoteIssues) List(ctx context.Context, opts issues.ListOptions) ([]issues.Issue, error) {
	ctx, err := outgoing(ctx, r.token)
	if err != nil {
		return nil, errIssuesUnavailable
	}

	response, err := r.client.ListIssues(ctx, &issuespb.ListIssuesRequest{
		Source:     opts.Source,
		Repository: opts.Repository,
		State:      string(opts.State),
		Labels:     opts.Labels,
		Scope:      string(opts.Scope),
	}, grpc.MaxCallRecvMsgSize(r.maxBytes))
	if err != nil {
		return nil, issuesError(ctx, err)
	}

	list := make([]issues.Issue, len(response.GetIssues()))
	for i, item := range response.GetIssues() {
		list[i] = issueOf(item)
	}
	return list, nil
}

// issuesError maps an error of forge-issues to one that holds none of its
// message.
func issuesError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	switch status.Code(err) {
	case codes.NotFound:
		return ErrNotFound
	case codes.InvalidArgument, codes.FailedPrecondition:
		return ErrIssuesRejected
	case codes.PermissionDenied:
		return ErrIssuesDenied
	case codes.ResourceExhausted:
		return ErrIssuesExhausted
	}
	return errIssuesUnavailable
}

// issueOf converts the fields an issue search uses.
func issueOf(item *issuespb.Issue) issues.Issue {
	source := item.GetSource()
	return issues.Issue{
		Source:     issues.SourceInfo{ID: source.GetId(), Kind: forgeauth.Kind(source.GetKind()), Host: source.GetHost()},
		Repository: item.GetRepository(),
		ID:         item.GetId(),
		Number:     item.GetNumber(),
		Title:      item.GetTitle(),
		Body:       item.GetBody(),
		State:      issues.State(item.GetState()),
		WebURL:     item.GetWebUrl(),
	}
}
