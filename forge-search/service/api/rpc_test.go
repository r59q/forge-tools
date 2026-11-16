package api

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	checkoutspb "github.com/r59q/forge-tools/forge-checkouts/service/checkoutspb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func size(files map[string]string) int64 {
	var n int64
	for _, content := range files {
		n += int64(len(content))
	}
	return n
}

// TestIndexCheckout indexes a checkout, then changed versions of it and
// another checkout, through each transport, and checks that both return
// the same responses.
func TestIndexCheckout(t *testing.T) {
	var all [][]proto.Message
	for i := range 2 {
		f := newFixture(t, nil)
		tr := transports(t, f.s, "Bearer "+token)[i]
		t.Run(tr.name, func(t *testing.T) {
			all = append(all, indexCheckout(t, f, tr))
		})
	}

	if len(all) != 2 || len(all[0]) != len(all[1]) {
		t.Fatalf("responses: %v", all)
	}
	for i := range all[0] {
		equal(t, "gRPC and REST", all[1][i], all[0][i])
	}
}

func indexCheckout(t *testing.T, f *fixture, tr transport) []proto.Message {
	files := sample()
	f.add(t, checkoutID, "rev1", files)
	index := &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID}
	updated := timestamppb.New(now)

	first := must[*pb.IndexCheckoutResponse](t, tr, index)
	status := &pb.Index{Name: "main", Documents: 3, Bytes: size(files), Revision: "rev1", CheckoutId: checkoutID, UpdatedAt: updated}
	equal(t, "first", first, &pb.IndexCheckoutResponse{Index: status, Added: 3, Files: &pb.FileState{}})

	get := must[*pb.GetIndexResponse](t, tr, &pb.GetIndexRequest{Index: "main"})
	equal(t, "get", get, &pb.GetIndexResponse{Index: status})
	list := must[*pb.ListIndexesResponse](t, tr, &pb.ListIndexesRequest{})
	equal(t, "list", list, &pb.ListIndexesResponse{Indexes: []*pb.Index{status}})

	again := must[*pb.IndexCheckoutResponse](t, tr, index)
	equal(t, "again", again, &pb.IndexCheckoutResponse{Index: status, Unchanged: 3, Files: &pb.FileState{}})

	// rev2 changes one file, removes one and adds one.
	files["lib/greet.go"] += "\n// Greet is stable.\n"
	delete(files, "cmd/main.go")
	files["cmd/serve.go"] = "package main\n\nfunc serve() { lib.Greet(\"server\") }\n"
	f.add(t, checkoutID, "rev2", files)
	changed := must[*pb.IndexCheckoutResponse](t, tr, index)
	status = &pb.Index{Name: "main", Documents: 3, Bytes: size(files), Revision: "rev2", CheckoutId: checkoutID, UpdatedAt: updated}
	equal(t, "changed", changed, &pb.IndexCheckoutResponse{
		Index: status, Added: 1, Replaced: 1, Removed: 1, Unchanged: 1, Files: &pb.FileState{},
	})

	// Another checkout replaces the content of the index.
	other := map[string]string{"README.md": "Greet people.\n", "lib/greet.go": files["lib/greet.go"]}
	f.add(t, otherID, "rev3", other)
	replaced := must[*pb.IndexCheckoutResponse](t, tr, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: otherID})
	status = &pb.Index{Name: "main", Documents: 2, Bytes: size(other), Revision: "rev3", CheckoutId: otherID, UpdatedAt: updated}
	equal(t, "other checkout", replaced, &pb.IndexCheckoutResponse{
		Index: status, Added: 1, Removed: 2, Unchanged: 1, Files: &pb.FileState{},
	})

	found := must[*pb.SearchIndexResponse](t, tr, &pb.SearchIndexRequest{Index: "main", Query: &pb.CodeQuery{Text: "Greet"}})
	if len(found.GetMatches()) != 4 || found.GetRevision() != "rev3" || found.GetMatches()[0].GetPath() != "README.md" {
		t.Errorf("search: %v", found)
	}

	return []proto.Message{first, get, list, again, changed, replaced, found}
}

// TestIndexedMatchesUnindexed checks that SearchIndex and SearchCheckout
// return the same matches, in the same order, for the same files.
func TestIndexedMatchesUnindexed(t *testing.T) {
	files := sample()
	files["lib/binary.dat"] = "Greet\x00"
	files["lib/wide.go"] = "package lib\n\n// Grüße, Greet: «Greet»\nvar greetings = []string{\"Greet\", \"Greet\"}\n"
	files["docs/greet.md"] = strings.Repeat("Greet\n", 150)

	f := newFixture(t, nil)
	f.add(t, checkoutID, "rev1", files)

	queries := []*pb.CodeQuery{
		{Text: "Greet"},
		{Text: "G"},
		{Text: "Gr"},
		{Text: "«Greet»"},
		{Text: "Greet", PathPrefix: "lib/"},
		{Text: "Greet", PathPrefix: "lib/greet"},
		{Text: "Greet", PathPrefix: "cmd"},
		{Text: "Greet", PathPrefix: "none/"},
		{Text: "Greet", MaxResults: 1},
		{Text: "Greet", MaxResults: 5, PathPrefix: "docs/"},
		{Text: "missing"},
	}

	for _, tr := range transports(t, f.s, "Bearer "+token) {
		must[*pb.IndexCheckoutResponse](t, tr, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID})

		for _, q := range queries {
			indexed := must[*pb.SearchIndexResponse](t, tr, &pb.SearchIndexRequest{Index: "main", Query: q})
			unindexed := must[*pb.SearchCheckoutResponse](t, tr, &pb.SearchCheckoutRequest{CheckoutId: checkoutID, Query: q})

			name := tr.name + " " + q.String()
			equal(t, name+" matches", &pb.SearchIndexResponse{Matches: unindexed.GetMatches(), State: unindexed.GetState()},
				&pb.SearchIndexResponse{Matches: indexed.GetMatches(), State: indexed.GetState()})
			if indexed.GetRevision() != "rev1" || unindexed.GetRevision() != "rev1" {
				t.Errorf("%s revisions %q %q", name, indexed.GetRevision(), unindexed.GetRevision())
			}
			equal(t, name+" files", unindexed.GetFiles(), &pb.FileState{})
		}
	}

	tr := transports(t, f.s, "Bearer "+token)[0]
	all := must[*pb.SearchIndexResponse](t, tr, &pb.SearchIndexRequest{Index: "main", Query: &pb.CodeQuery{Text: "Greet"}})
	if n := len(all.GetMatches()); n != DefaultResults || !all.GetState().GetTruncated() {
		t.Errorf("default limit: %d matches, %v", n, all.GetState())
	}
	one := must[*pb.SearchIndexResponse](t, tr, &pb.SearchIndexRequest{Index: "main", Query: &pb.CodeQuery{Text: "Greet", MaxResults: 1}})
	equal(t, "first match", one.GetMatches()[0], &pb.CodeMatch{
		Path:          "cmd/main.go",
		Version:       one.GetMatches()[0].GetVersion(),
		Range:         &pb.Range{StartLine: 4, StartColumn: 14, EndLine: 4, EndColumn: 19},
		Preview:       "\tprintln(lib.Greet(\"world\"))",
		PreviewColumn: 1,
	})
	if len(one.GetMatches()[0].GetVersion()) != 64 {
		t.Errorf("version %q", one.GetMatches()[0].GetVersion())
	}
	lib := must[*pb.SearchIndexResponse](t, tr, &pb.SearchIndexRequest{Index: "main", Query: &pb.CodeQuery{Text: "Greet", PathPrefix: "lib/"}})
	if len(lib.GetMatches()) == 0 || lib.GetState().GetSkipped() != 1 {
		t.Errorf("lib: %v", lib)
	}
	for _, m := range lib.GetMatches() {
		if !strings.HasPrefix(m.GetPath(), "lib/") {
			t.Errorf("prefix: %v", m)
		}
	}
}

// TestCheckoutErrors checks the checkouts that cannot be read, on both
// transports, and that a failed indexing creates no index.
func TestCheckoutErrors(t *testing.T) {
	f := newFixture(t, nil)
	f.add(t, checkoutID, "rev1", sample())
	ready := Checkout{ID: checkoutID, Revision: "rev1", State: "ready", Location: filepath.Join(f.root, checkoutID)}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(f.root, otherID)); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		id      string
		set     func(Checkout) Checkout
		lookups error
		want    codes.Code
	}{
		{name: "malformed ID", id: "not-a-checkout", want: codes.InvalidArgument},
		{name: "uppercase ID", id: strings.ToUpper(checkoutID), want: codes.InvalidArgument},
		{name: "unknown", id: "00000000000000000000000000000000", want: codes.NotFound},
		{name: "creating", set: state("creating"), want: codes.FailedPrecondition},
		{name: "removing", set: state("removing"), want: codes.FailedPrecondition},
		{name: "failed", set: state("failed"), want: codes.FailedPrecondition},
		{name: "unknown state", set: state(""), want: codes.FailedPrecondition},
		{name: "location elsewhere", set: location(filepath.Join(outside, checkoutID)), want: codes.FailedPrecondition},
		{name: "location above", set: location(f.root), want: codes.FailedPrecondition},
		{name: "location unclean", set: location(f.root + "/./" + checkoutID), want: codes.FailedPrecondition},
		{name: "relative location", set: location(checkoutID), want: codes.FailedPrecondition},
		{name: "missing directory", id: "11111111111111111111111111111111", want: codes.FailedPrecondition},
		{name: "symlinked directory", id: otherID, want: codes.FailedPrecondition},
		{name: "other ID returned", id: "22222222222222222222222222222222", want: codes.Unavailable},
		{name: "lookup fails", lookups: errors.New("connection refused to /secret/path"), want: codes.Unavailable},
		{name: "retained", set: state("retained"), want: codes.OK},
	}

	missing := "11111111111111111111111111111111"
	f.checkouts.set(Checkout{ID: missing, State: "ready", Location: filepath.Join(f.root, missing)})
	f.checkouts.set(Checkout{ID: otherID, State: "ready", Location: filepath.Join(f.root, otherID)})
	f.checkouts.checkouts["22222222222222222222222222222222"] = ready

	for _, c := range cases {
		id := c.id
		if id == "" {
			id = checkoutID
		}
		f.checkouts.set(ready)
		if c.set != nil {
			f.checkouts.set(c.set(ready))
		}
		f.checkouts.fail(c.lookups)

		for _, tr := range transports(t, f.s, "Bearer "+token) {
			query := &pb.CodeQuery{Text: "Greet"}
			if got := tr.call(t, &pb.SearchCheckoutRequest{CheckoutId: id, Query: query}, &pb.SearchCheckoutResponse{}); got != c.want {
				t.Errorf("%s search %s: got %v, want %v", tr.name, c.name, got, c.want)
			}
			if c.want == codes.OK {
				continue
			}
			if got := tr.call(t, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: id}, &pb.IndexCheckoutResponse{}); got != c.want {
				t.Errorf("%s index %s: got %v, want %v", tr.name, c.name, got, c.want)
			}
		}
	}

	f.checkouts.fail(nil)
	tr := transports(t, f.s, "Bearer "+token)[0]
	if list := must[*pb.ListIndexesResponse](t, tr, &pb.ListIndexesRequest{}); len(list.GetIndexes()) != 0 {
		t.Fatalf("failed indexing created %v", list)
	}
}

func state(s string) func(Checkout) Checkout {
	return func(c Checkout) Checkout { c.State = s; return c }
}

func location(l string) func(Checkout) Checkout {
	return func(c Checkout) Checkout { c.Location = l; return c }
}

// TestReportedRoot reads a checkout whose location forge-checkouts names
// below another root, as when its data directory is mounted elsewhere.
func TestReportedRoot(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Root.Reported = "/var/lib/forge-checkouts/copies" })
	f.add(t, checkoutID, "rev1", sample())
	tr := transports(t, f.s, "Bearer "+token)[0]

	query := &pb.SearchCheckoutRequest{CheckoutId: checkoutID, Query: &pb.CodeQuery{Text: "Greet"}}
	if got := tr.call(t, query, &pb.SearchCheckoutResponse{}); got != codes.FailedPrecondition {
		t.Fatalf("local location: %v", got)
	}

	f.checkouts.set(Checkout{ID: checkoutID, Revision: "rev1", State: "ready", Location: "/var/lib/forge-checkouts/copies/" + checkoutID})
	if found := must[*pb.SearchCheckoutResponse](t, tr, query); len(found.GetMatches()) != 5 {
		t.Fatalf("matches: %v", found)
	}
}

func TestErrors(t *testing.T) {
	f := newFixture(t, nil)
	f.add(t, checkoutID, "rev1", sample())
	query := &pb.CodeQuery{Text: "Greet"}

	cases := []struct {
		name string
		req  proto.Message
		want codes.Code
	}{
		{"uppercase index", &pb.IndexCheckoutRequest{Index: "Main", CheckoutId: checkoutID}, codes.InvalidArgument},
		{"index with space", &pb.IndexCheckoutRequest{Index: "a b", CheckoutId: checkoutID}, codes.InvalidArgument},
		{"long index", &pb.IndexCheckoutRequest{Index: strings.Repeat("a", 65), CheckoutId: checkoutID}, codes.InvalidArgument},
		{"get invalid", &pb.GetIndexRequest{Index: "a..b"}, codes.InvalidArgument},
		{"get missing", &pb.GetIndexRequest{Index: "missing"}, codes.NotFound},
		{"delete missing", &pb.DeleteIndexRequest{Index: "missing"}, codes.NotFound},
		{"search missing", &pb.SearchIndexRequest{Index: "missing", Query: query}, codes.NotFound},
		{"no query", &pb.SearchCheckoutRequest{CheckoutId: checkoutID}, codes.InvalidArgument},
		{"newline", &pb.SearchCheckoutRequest{CheckoutId: checkoutID, Query: &pb.CodeQuery{Text: "a\nb"}}, codes.InvalidArgument},
		{"long query", &pb.SearchCheckoutRequest{CheckoutId: checkoutID, Query: &pb.CodeQuery{Text: strings.Repeat("a", 257)}},
			codes.InvalidArgument},
		{"negative max_results", &pb.SearchCheckoutRequest{CheckoutId: checkoutID, Query: &pb.CodeQuery{Text: "a", MaxResults: -1}},
			codes.InvalidArgument},
		{"max_results over 500", &pb.SearchCheckoutRequest{CheckoutId: checkoutID, Query: &pb.CodeQuery{Text: "a", MaxResults: 501}},
			codes.InvalidArgument},
		{"long prefix", &pb.SearchCheckoutRequest{CheckoutId: checkoutID, Query: &pb.CodeQuery{Text: "a", PathPrefix: strings.Repeat("a", 4097)}},
			codes.InvalidArgument},
		{"index no query", &pb.SearchIndexRequest{Index: "main"}, codes.InvalidArgument},
		{"index max_results over 500", &pb.SearchIndexRequest{Index: "main", Query: &pb.CodeQuery{Text: "a", MaxResults: 501}},
			codes.InvalidArgument},
		{"index long prefix", &pb.SearchIndexRequest{Index: "main", Query: &pb.CodeQuery{Text: "a", PathPrefix: strings.Repeat("a", 4097)}},
			codes.InvalidArgument},
	}

	for _, tr := range transports(t, f.s, "Bearer "+token) {
		must[*pb.IndexCheckoutResponse](t, tr, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID})

		for _, c := range cases {
			if got := tr.call(t, c.req, response(c.req)); got != c.want {
				t.Errorf("%s %s: got %v, want %v", tr.name, c.name, got, c.want)
			}
		}
		must[*pb.DeleteIndexResponse](t, tr, &pb.DeleteIndexRequest{Index: "main"})
	}
}

func TestDeleteIndex(t *testing.T) {
	checkpoints := 0
	f := newFixture(t, func(c *Config) {
		c.Checkpoint = func(context.Context) error { checkpoints++; return nil }
	})
	f.add(t, checkoutID, "rev1", sample())

	for _, tr := range transports(t, f.s, "Bearer "+token) {
		must[*pb.IndexCheckoutResponse](t, tr, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID})
		must[*pb.IndexCheckoutResponse](t, tr, &pb.IndexCheckoutRequest{Index: "kept", CheckoutId: checkoutID})
		must[*pb.DeleteIndexResponse](t, tr, &pb.DeleteIndexRequest{Index: "main"})

		if got := tr.call(t, &pb.GetIndexRequest{Index: "main"}, &pb.GetIndexResponse{}); got != codes.NotFound {
			t.Errorf("%s get deleted: %v", tr.name, got)
		}
		if got := tr.call(t, &pb.SearchIndexRequest{Index: "main", Query: &pb.CodeQuery{Text: "Greet"}}, &pb.SearchIndexResponse{}); got != codes.NotFound {
			t.Errorf("%s search deleted: %v", tr.name, got)
		}
		list := must[*pb.ListIndexesResponse](t, tr, &pb.ListIndexesRequest{})
		if len(list.GetIndexes()) != 1 || list.GetIndexes()[0].GetName() != "kept" {
			t.Errorf("%s list: %v", tr.name, list)
		}

		again := must[*pb.IndexCheckoutResponse](t, tr, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID})
		if again.GetAdded() != 3 {
			t.Errorf("%s reindex: %v", tr.name, again)
		}
		must[*pb.DeleteIndexResponse](t, tr, &pb.DeleteIndexRequest{Index: "main"})
		must[*pb.DeleteIndexResponse](t, tr, &pb.DeleteIndexRequest{Index: "kept"})
	}
	if checkpoints != 6 {
		t.Fatalf("%d checkpoints", checkpoints)
	}

	f.s.config.Checkpoint = func(context.Context) error { return errors.New("disk at /secret") }
	tr := transports(t, f.s, "Bearer "+token)[1]
	must[*pb.IndexCheckoutResponse](t, tr, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID})
	if got := tr.call(t, &pb.DeleteIndexRequest{Index: "main"}, &pb.DeleteIndexResponse{}); got != codes.Internal {
		t.Fatalf("failed checkpoint: %v", got)
	}
}

// fakeService is a forge-checkouts server that knows one checkout.
type fakeService struct {
	checkoutspb.UnimplementedCheckoutsServiceServer
	authorization chan string
}

func (f *fakeService) GetCheckout(ctx context.Context, r *checkoutspb.GetCheckoutRequest) (*checkoutspb.GetCheckoutResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	f.authorization <- strings.Join(md.Get("authorization"), ",")

	switch r.GetId() {
	case checkoutID:
		return &checkoutspb.GetCheckoutResponse{Checkout: &checkoutspb.Checkout{
			Id: checkoutID, Revision: "rev1", State: "ready", Location: "/data/copies/" + checkoutID,
		}}, nil
	case otherID:
		return nil, status.Error(codes.Internal, "failed reading /data/secret")
	}
	return nil, status.Error(codes.NotFound, "no checkout")
}

func TestRemote(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	service := &fakeService{authorization: make(chan string, 10)}
	g := grpc.NewServer()
	checkoutspb.RegisterCheckoutsServiceServer(g, service)
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

	ctx := context.Background()
	remote := NewRemote(checkoutspb.NewCheckoutsServiceClient(conn), forgeauth.StaticToken("checkouts-token"))

	c, err := remote.Checkout(ctx, checkoutID)
	if err != nil {
		t.Fatal(err)
	}
	if c != (Checkout{ID: checkoutID, Revision: "rev1", State: "ready", Location: "/data/copies/" + checkoutID}) {
		t.Fatalf("checkout: %+v", c)
	}
	if got := <-service.authorization; got != "Bearer checkouts-token" {
		t.Fatalf("authorization %q", got)
	}

	if _, err := remote.Checkout(ctx, "00000000000000000000000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := remote.Checkout(ctx, otherID); !errors.Is(err, errUnavailable) {
		t.Fatalf("failing: %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := remote.Checkout(canceled, checkoutID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}

	noToken := NewRemote(checkoutspb.NewCheckoutsServiceClient(conn), forgeauth.EnvToken("FORGE_SEARCH_TEST_UNSET_TOKEN"))
	if _, err := noToken.Checkout(ctx, checkoutID); !errors.Is(err, errUnavailable) {
		t.Fatalf("no token: %v", err)
	}

	g.Stop()
	if _, err := remote.Checkout(ctx, checkoutID); !errors.Is(err, errUnavailable) {
		t.Fatalf("stopped: %v", err)
	}
}
