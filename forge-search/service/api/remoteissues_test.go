package api

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-issues/library/issues"
	"github.com/r59q/forge-tools/forge-issues/service/issuespb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// fakeIssueService is a forge-issues server. Its repository names the
// response: "octo/demo" lists one issue, "octo/huge" one with a large body,
// and others fail with the code of that name.
type fakeIssueService struct {
	issuespb.UnimplementedIssuesServiceServer
	authorization chan string
	asked         chan *issuespb.ListIssuesRequest
}

func (f *fakeIssueService) ListIssues(ctx context.Context, r *issuespb.ListIssuesRequest) (*issuespb.ListIssuesResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	f.authorization <- strings.Join(md.Get("authorization"), ",")
	f.asked <- r

	item := &issuespb.Issue{
		Source:     &issuespb.SourceInfo{Id: "github", Kind: "github", Host: "github.com"},
		Repository: r.GetRepository(), Id: "I1", Number: 7, Title: "Crash", Body: "It crashes.",
		State: "open", WebUrl: "https://github.com/octo/demo/issues/7",
	}
	switch r.GetRepository() {
	case "octo/demo":
	case "octo/huge":
		item.Body = strings.Repeat("x", 4<<10)
	default:
		var c codes.Code
		if err := c.UnmarshalJSON([]byte(`"` + strings.TrimPrefix(r.GetRepository(), "octo/") + `"`)); err != nil {
			return nil, status.Error(codes.Internal, "bad test repository")
		}
		return nil, status.Error(c, "issue text /secret")
	}
	return &issuespb.ListIssuesResponse{Issues: []*issuespb.Issue{item}}, nil
}

func serveIssues(t *testing.T, service *fakeIssueService) (*grpc.ClientConn, *grpc.Server) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	issuespb.RegisterIssuesServiceServer(g, service)
	go func() { _ = g.Serve(listener) }()
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, g
}

func TestRemoteIssues(t *testing.T) {
	service := &fakeIssueService{authorization: make(chan string, 20), asked: make(chan *issuespb.ListIssuesRequest, 20)}
	conn, g := serveIssues(t, service)
	ctx := context.Background()
	remote := NewRemoteIssues(issuespb.NewIssuesServiceClient(conn), forgeauth.StaticToken("issues-token"))

	options := issues.ListOptions{Source: "github", Repository: "octo/demo", State: issues.StateAll}
	list, err := remote.List(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	want := issues.Issue{
		Source:     issues.SourceInfo{ID: "github", Kind: "github", Host: "github.com"},
		Repository: "octo/demo", ID: "I1", Number: 7, Title: "Crash", Body: "It crashes.",
		State: issues.Open, WebURL: "https://github.com/octo/demo/issues/7",
	}
	if len(list) != 1 || !equalIssue(list[0], want) {
		t.Fatalf("list = %+v", list)
	}
	if got := <-service.authorization; got != "Bearer issues-token" {
		t.Fatalf("authorization %q", got)
	}
	if r := <-service.asked; r.GetSource() != "github" || r.GetRepository() != "octo/demo" || r.GetState() != "all" {
		t.Fatalf("request %v", r)
	}

	remote.maxBytes = 1 << 10
	if _, err := remote.List(ctx, issues.ListOptions{Source: "github", Repository: "octo/huge"}); !errors.Is(err, ErrIssuesExhausted) {
		t.Fatalf("over the receive limit: %v", err)
	}

	noToken := NewRemoteIssues(issuespb.NewIssuesServiceClient(conn), forgeauth.EnvToken("FORGE_SEARCH_TEST_UNSET_TOKEN"))
	if _, err := noToken.List(ctx, options); !errors.Is(err, errIssuesUnavailable) {
		t.Fatalf("no token: %v", err)
	}

	g.Stop()
	if _, err := remote.List(ctx, options); !errors.Is(err, errIssuesUnavailable) {
		t.Fatalf("stopped: %v", err)
	}
}

func TestRemoteIssueErrors(t *testing.T) {
	service := &fakeIssueService{authorization: make(chan string, 20), asked: make(chan *issuespb.ListIssuesRequest, 20)}
	conn, _ := serveIssues(t, service)
	remote := NewRemoteIssues(issuespb.NewIssuesServiceClient(conn), forgeauth.StaticToken("issues-token"))

	for name, want := range map[string]error{
		"NOT_FOUND":           ErrNotFound,
		"INVALID_ARGUMENT":    ErrIssuesRejected,
		"FAILED_PRECONDITION": ErrIssuesRejected,
		"PERMISSION_DENIED":   ErrIssuesDenied,
		"RESOURCE_EXHAUSTED":  ErrIssuesExhausted,
		"UNAVAILABLE":         errIssuesUnavailable,
		"INTERNAL":            errIssuesUnavailable,
	} {
		_, err := remote.List(context.Background(), issues.ListOptions{Source: "github", Repository: "octo/" + name})
		if !errors.Is(err, want) || strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: %v, want %v", name, err, want)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := remote.List(canceled, issues.ListOptions{Source: "github", Repository: "octo/demo"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}

func equalIssue(a, b issues.Issue) bool {
	return a.Source == b.Source && a.Repository == b.Repository && a.ID == b.ID && a.Number == b.Number &&
		a.Title == b.Title && a.Body == b.Body && a.State == b.State && a.WebURL == b.WebURL
}
