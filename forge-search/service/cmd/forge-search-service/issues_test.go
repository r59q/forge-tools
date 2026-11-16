package main

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-issues/service/issuespb"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// fakeIssues lists one issue to callers with the issues token.
type fakeIssues struct {
	issuespb.UnimplementedIssuesServiceServer
}

func (fakeIssues) ListIssues(ctx context.Context, r *issuespb.ListIssuesRequest) (*issuespb.ListIssuesResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer issues" {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	return &issuespb.ListIssuesResponse{Issues: []*issuespb.Issue{{
		Source:     &issuespb.SourceInfo{Id: r.GetSource(), Kind: "github", Host: "github.com"},
		Repository: r.GetRepository(), Id: "I1", Number: 1, Title: "Greet fails", Body: "Greet panics.", State: "open",
		WebUrl: "https://github.com/octo/demo/issues/1",
	}}}, nil
}

// withIssues serves a fake forge-issues and adds its settings to e.
func withIssues(t *testing.T, e installation) installation {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	issuespb.RegisterIssuesServiceServer(g, fakeIssues{})
	go func() { _ = g.Serve(listener) }()
	t.Cleanup(g.Stop)

	t.Setenv("FORGE_SEARCH_ISSUES_TOKEN", "issues")
	e.args = append(e.args, "--issues-addr="+listener.Addr().String(), "--issues-insecure=true")
	return e
}

func TestRunIssues(t *testing.T) {
	grpcAddr, _, _ := withIssues(t, newInstallation(t)).start(t)
	c, ctx := client(t, grpcAddr)

	repo := &pb.IssueRepository{Source: "github", Repository: "octo/demo"}
	indexed, err := c.IndexIssues(ctx, &pb.IndexIssuesRequest{Repository: repo})
	if err != nil || indexed.GetAdded() != 1 {
		t.Fatalf("index issues: %v %v", indexed, err)
	}
	searched, err := c.SearchIndexedIssues(ctx, &pb.SearchIndexedIssuesRequest{Repository: repo, Query: &pb.IssueQuery{Text: "Greet"}})
	if err != nil || len(searched.GetMatches()) != 2 || searched.GetMatches()[0].GetNumber() != 1 {
		t.Fatalf("search issues: %v %v", searched, err)
	}
}

func TestRunWithoutIssues(t *testing.T) {
	grpcAddr, _, _ := newInstallation(t).start(t)
	c, ctx := client(t, grpcAddr)

	repo := &pb.IssueRepository{Source: "github", Repository: "octo/demo"}
	if _, err := c.IndexIssues(ctx, &pb.IndexIssuesRequest{Repository: repo}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("index issues: %v", err)
	}
	if _, err := c.IndexCheckout(ctx, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID}); err != nil {
		t.Fatalf("index checkout: %v", err)
	}
}

func TestRunRequiresIssuesToken(t *testing.T) {
	e := withIssues(t, newInstallation(t))
	t.Setenv("FORGE_SEARCH_ISSUES_TOKEN", "")
	s, err := parse(e.args, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), s, nil); err == nil || !strings.Contains(err.Error(), "token required") {
		t.Fatalf("got %v", err)
	}
}
