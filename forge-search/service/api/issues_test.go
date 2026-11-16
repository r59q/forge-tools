package api

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-issues/library/issues"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// issueRepo is the repository of the fake issues.
var issueRepo = &pb.IssueRepository{Source: "github", Repository: "octo/demo"}

// fakeIssues lists issues for a test server. A set block makes List wait
// until it is closed.
type fakeIssues struct {
	mu    sync.Mutex
	list  []issues.Issue
	err   error
	block chan struct{}
	calls int
	asked []issues.ListOptions
}

func (f *fakeIssues) List(ctx context.Context, options issues.ListOptions) ([]issues.Issue, error) {
	f.mu.Lock()
	f.calls++
	f.asked = append(f.asked, options)
	block, list, err := f.block, f.list, f.err
	f.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return list, err
}

// set replaces the issues and error List returns.
func (f *fakeIssues) set(list []issues.Issue, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.list, f.err = list, err
}

func (f *fakeIssues) called() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func sampleIssue(number int64, title, body string, state issues.State) issues.Issue {
	return issues.Issue{
		Source:     issues.SourceInfo{ID: "github", Kind: "github", Host: "github.com"},
		Repository: "octo/demo",
		ID:         "I" + title,
		Number:     number,
		Title:      title,
		Body:       body,
		State:      state,
		WebURL:     "https://github.com/octo/demo/issues/" + title,
	}
}

// sampleIssues are three issues, two of which mention a crash.
func sampleIssues() []issues.Issue {
	return []issues.Issue{
		sampleIssue(1, "Crash on start", "It crashes.\nEvery time.", issues.Open),
		sampleIssue(2, "Docs", "Typo in the README.", issues.Open),
		sampleIssue(3, "Old", "A crash we fixed.", issues.Closed),
	}
}

func TestIndexIssues(t *testing.T) {
	var all [][]proto.Message
	for i := range 2 {
		f := newFixture(t, nil)
		tr := transports(t, f.s, "Bearer "+token)[i]
		t.Run(tr.name, func(t *testing.T) {
			all = append(all, indexIssues(t, f, tr))
		})
	}

	if len(all) != 2 || len(all[0]) != len(all[1]) {
		t.Fatalf("responses: %v", all)
	}
	for i := range all[0] {
		equal(t, "gRPC and REST", withoutTimes(all[1][i]), withoutTimes(all[0][i]))
	}
}

// withoutTimes returns a copy of m without its sync times, which the issue
// library takes from the clock.
func withoutTimes(m proto.Message) proto.Message {
	m = proto.Clone(m)
	var clear func(protoreflect.Message)
	clear = func(m protoreflect.Message) {
		m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
			switch {
			case fd.Name() == "updated_at" || fd.Name() == "synced_at":
				m.Clear(fd)
			case fd.IsList() && fd.Message() != nil:
				for i := range v.List().Len() {
					clear(v.List().Get(i).Message())
				}
			case fd.Message() != nil && !fd.IsMap():
				clear(v.Message())
			}
			return true
		})
	}
	clear(m.ProtoReflect())
	return m
}

// indexIssues indexes the sample issues, searches them with and without the
// index, refreshes, and deletes the index, returning the responses.
func indexIssues(t *testing.T, f *fixture, tr transport) []proto.Message {
	t.Helper()
	f.issues.set(sampleIssues(), nil)

	indexed := must[*pb.IndexIssuesResponse](t, tr, &pb.IndexIssuesRequest{Repository: issueRepo})
	index := indexed.GetIndex()
	if indexed.GetAdded() != 3 || !strings.HasPrefix(index.GetName(), issuesPrefix) ||
		index.GetDocuments() != 3 || index.GetUpdatedAt() == nil || !proto.Equal(index.GetIssues(), issueRepo) {
		t.Fatalf("index issues = %v", indexed)
	}

	query := &pb.IssueQuery{Text: "crash"}
	search := must[*pb.SearchIndexedIssuesResponse](t, tr, &pb.SearchIndexedIssuesRequest{Repository: issueRepo, Query: query})
	live := must[*pb.SearchUnindexedIssuesResponse](t, tr, &pb.SearchUnindexedIssuesRequest{Repository: issueRepo, Query: query})
	checkIssueMatches(t, search.GetMatches(), live.GetMatches(), 1, 3)
	equal(t, "state", live.GetState(), search.GetState())

	query = &pb.IssueQuery{Text: "crash", State: "closed"}
	closed := must[*pb.SearchIndexedIssuesResponse](t, tr, &pb.SearchIndexedIssuesRequest{Repository: issueRepo, Query: query})
	liveClosed := must[*pb.SearchUnindexedIssuesResponse](t, tr, &pb.SearchUnindexedIssuesRequest{Repository: issueRepo, Query: query})
	checkIssueMatches(t, closed.GetMatches(), liveClosed.GetMatches(), 3)

	list := must[*pb.ListIndexesResponse](t, tr, &pb.ListIndexesRequest{})
	if len(list.GetIndexes()) != 1 || !proto.Equal(list.GetIndexes()[0], index) {
		t.Fatalf("list = %v, want %v", list, index)
	}

	f.issues.set(sampleIssues()[1:], nil)
	refreshed := must[*pb.IndexIssuesResponse](t, tr, &pb.IndexIssuesRequest{Repository: issueRepo})
	if refreshed.GetRemoved() != 1 || refreshed.GetUnchanged() != 2 || refreshed.GetIndex().GetDocuments() != 2 {
		t.Fatalf("refresh = %v", refreshed)
	}

	must[*pb.DeleteIndexResponse](t, tr, &pb.DeleteIndexRequest{Index: index.GetName()})
	if code := tr.call(t, &pb.GetIndexRequest{Index: index.GetName()}, &pb.GetIndexResponse{}); code != codes.NotFound {
		t.Fatalf("get deleted = %v", code)
	}
	return []proto.Message{indexed, search, live, closed, liveClosed, list, refreshed}
}

// checkIssueMatches checks that indexed and unindexed matches are equal and
// name the given issues, in order.
func checkIssueMatches(t *testing.T, indexed, unindexed []*pb.IssueMatch, numbers ...int64) {
	t.Helper()
	if len(indexed) != len(numbers) || len(unindexed) != len(numbers) {
		t.Fatalf("matches = %v and %v, want issues %v", indexed, unindexed, numbers)
	}
	for i, m := range indexed {
		equal(t, "match", unindexed[i], m)
		if m.GetNumber() != numbers[i] || m.GetWebUrl() == "" || m.GetPreview() == "" || m.GetRange() == nil {
			t.Fatalf("match %d = %v, want issue %d", i, m, numbers[i])
		}
	}
}

func TestIssueErrors(t *testing.T) {
	f := newFixture(t, nil)
	other := &pb.IssueRepository{Source: "github", Repository: "octo/other"}
	cases := []struct {
		name string
		req  proto.Message
		want codes.Code
	}{
		{"no source", &pb.IndexIssuesRequest{Repository: &pb.IssueRepository{Repository: "octo/demo"}}, codes.InvalidArgument},
		{"bad repository", &pb.SearchUnindexedIssuesRequest{
			Repository: &pb.IssueRepository{Source: "github", Repository: "../x"}, Query: &pb.IssueQuery{Text: "x"},
		}, codes.InvalidArgument},
		{"bad state", &pb.SearchUnindexedIssuesRequest{
			Repository: issueRepo, Query: &pb.IssueQuery{Text: "x", State: "all"},
		}, codes.InvalidArgument},
		{"too many results", &pb.SearchUnindexedIssuesRequest{
			Repository: issueRepo, Query: &pb.IssueQuery{Text: "x", MaxResults: MaxResults + 1},
		}, codes.InvalidArgument},
		{"empty query", &pb.SearchUnindexedIssuesRequest{Repository: issueRepo, Query: &pb.IssueQuery{}}, codes.InvalidArgument},
		{"not indexed", &pb.SearchIndexedIssuesRequest{Repository: other, Query: &pb.IssueQuery{Text: "x"}}, codes.NotFound},
		{"checkout into issue name", &pb.IndexCheckoutRequest{Index: issuesPrefix + "x", CheckoutId: checkoutID}, codes.InvalidArgument},
	}
	for _, tr := range transports(t, f.s, "Bearer "+token) {
		for _, c := range cases {
			if got := tr.call(t, c.req, response(c.req)); got != c.want {
				t.Errorf("%s %s = %v, want %v", tr.name, c.name, got, c.want)
			}
		}
	}
	if n := f.issues.called(); n != 0 {
		t.Fatalf("invalid requests listed issues %d times", n)
	}
}

func TestIssueReaderErrors(t *testing.T) {
	cases := []struct {
		err  error
		want codes.Code
	}{
		{ErrNotFound, codes.NotFound},
		{ErrIssuesRejected, codes.InvalidArgument},
		{ErrIssuesDenied, codes.PermissionDenied},
		{ErrIssuesExhausted, codes.ResourceExhausted},
		{errIssuesUnavailable, codes.Unavailable},
	}
	for _, c := range cases {
		f := newFixture(t, nil)
		f.issues.set(sampleIssues(), c.err)
		for _, tr := range transports(t, f.s, "Bearer "+token) {
			for _, req := range []proto.Message{
				&pb.IndexIssuesRequest{Repository: issueRepo},
				&pb.SearchUnindexedIssuesRequest{Repository: issueRepo, Query: &pb.IssueQuery{Text: "crash"}},
			} {
				if got := tr.call(t, req, response(req)); got != c.want {
					t.Errorf("%s %s with %v = %v, want %v", tr.name, method(req), c.err, got, c.want)
				}
			}
		}
	}
}

// TestFailedIssueSync checks that a failed refresh leaves the index without
// an update time, so that it is never shown as current.
func TestFailedIssueSync(t *testing.T) {
	f := newFixture(t, nil)
	tr := transports(t, f.s, "Bearer "+token)[0]
	f.issues.set(sampleIssues(), nil)
	index := must[*pb.IndexIssuesResponse](t, tr, &pb.IndexIssuesRequest{Repository: issueRepo}).GetIndex()

	f.issues.set(nil, errIssuesUnavailable)
	if code := tr.call(t, &pb.IndexIssuesRequest{Repository: issueRepo}, &pb.IndexIssuesResponse{}); code != codes.Unavailable {
		t.Fatalf("failed sync = %v", code)
	}
	got := must[*pb.GetIndexResponse](t, tr, &pb.GetIndexRequest{Index: index.GetName()}).GetIndex()
	if got.GetUpdatedAt() != nil || !proto.Equal(got.GetIssues(), issueRepo) {
		t.Fatalf("index after failed sync = %v", got)
	}
}

func TestConcurrentIndexIssues(t *testing.T) {
	f := newFixture(t, nil)
	tr := transports(t, f.s, "Bearer "+token)[0]
	f.issues.set(sampleIssues(), nil)
	block := make(chan struct{})
	f.issues.mu.Lock()
	f.issues.block = block
	f.issues.mu.Unlock()

	done := make(chan codes.Code)
	go func() { done <- tr.call(t, &pb.IndexIssuesRequest{Repository: issueRepo}, &pb.IndexIssuesResponse{}) }()
	for f.issues.called() == 0 {
		select {
		case code := <-done:
			t.Fatalf("first sync ended early: %v", code)
		default:
		}
	}

	for _, req := range []proto.Message{
		&pb.IndexIssuesRequest{Repository: issueRepo},
		&pb.DeleteIndexRequest{Index: issueIndex(issues.RepositoryRef{Source: "github", Repository: "octo/demo"})},
	} {
		if code := tr.call(t, req, response(req)); code != codes.Aborted {
			t.Errorf("%s during sync = %v, want %v", method(req), code, codes.Aborted)
		}
	}
	close(block)
	if code := <-done; code != codes.OK {
		t.Fatalf("first sync = %v", code)
	}
	if n := f.issues.called(); n != 1 {
		t.Fatalf("issues listed %d times, want 1", n)
	}
}

func TestNoIssueBackend(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Issues = nil })
	f.add(t, checkoutID, "rev1", sample())
	for _, tr := range transports(t, f.s, "Bearer "+token) {
		for _, req := range []proto.Message{
			&pb.IndexIssuesRequest{Repository: issueRepo},
			&pb.SearchUnindexedIssuesRequest{Repository: issueRepo, Query: &pb.IssueQuery{Text: "crash"}},
		} {
			if code := tr.call(t, req, response(req)); code != codes.Unimplemented {
				t.Errorf("%s %s = %v, want %v", tr.name, method(req), code, codes.Unimplemented)
			}
		}
		search := must[*pb.SearchCheckoutResponse](t, tr, &pb.SearchCheckoutRequest{
			CheckoutId: checkoutID, Query: &pb.CodeQuery{Text: "Greet"},
		})
		if len(search.GetMatches()) == 0 {
			t.Fatalf("%s code search without issues = %v", tr.name, search)
		}
	}
}

func TestIssueIndexName(t *testing.T) {
	a := issueIndex(issues.RepositoryRef{Source: "github", Repository: "octo/demo"})
	b := issueIndex(issues.RepositoryRef{Source: "github", Repository: "octo/other"})
	if a == b || !strings.HasPrefix(a, issuesPrefix) || strings.Contains(a, "octo") {
		t.Fatalf("issue index names %q and %q", a, b)
	}
	if !reserved(a) || reserved("mine") {
		t.Fatal("reserved does not know issue index names")
	}
	if _, err := parseIssueRecord("{"); err == nil {
		t.Fatal("parseIssueRecord accepted bad JSON")
	}
	if errors.Is(errNoIssues, errBadRequest) {
		t.Fatal("errNoIssues is a bad request")
	}
}
