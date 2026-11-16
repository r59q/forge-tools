package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// created is the ID of the nth checkout a fake creates.
func created(n int) string {
	return fmt.Sprintf("%032x", n)
}

// authorized returns a context that calls the server directly with the
// token.
func authorized(ctx context.Context) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
}

// TestIndexRepository indexes a branch, again unchanged, and after it moved,
// searches it with and without its index, through each transport, and
// checks that both return the same responses and leave no checkout.
func TestIndexRepository(t *testing.T) {
	var all [][]proto.Message
	for i := range 2 {
		f := newFixture(t, nil)
		tr := transports(t, f.s, "Bearer "+token)[i]
		t.Run(tr.name, func(t *testing.T) {
			all = append(all, indexRepository(t, f, tr))
			f.clean(t)
		})
	}

	if len(all) != 2 || len(all[0]) != len(all[1]) {
		t.Fatalf("responses: %v", all)
	}
	for i := range all[0] {
		equal(t, "gRPC and REST", all[1][i], all[0][i])
	}
}

func indexRepository(t *testing.T, f *fixture, tr transport) []proto.Message {
	files := sample()
	f.checkouts.branch(repository, "rev1", files)
	index := &pb.IndexRepositoryRequest{Target: &pb.RepositoryTarget{Source: "github", Repository: "octo/demo", Reference: "HEAD"}}
	name := repository.index()
	updated := timestamppb.New(now)

	first := must[*pb.IndexRepositoryResponse](t, tr, index)
	status := &pb.Index{
		Name: name, Documents: 3, Bytes: size(files), Revision: "rev1",
		CheckoutId: created(1), UpdatedAt: updated, Target: repository.proto(),
	}
	equal(t, "first", first, &pb.IndexRepositoryResponse{Index: status, Added: 3, Files: &pb.FileState{}})

	again := must[*pb.IndexRepositoryResponse](t, tr, index)
	equal(t, "again", again, &pb.IndexRepositoryResponse{Index: status, Files: &pb.FileState{}, UpToDate: true})

	files["lib/greet.go"] += "\n// Greet is stable.\n"
	delete(files, "cmd/main.go")
	f.checkouts.branch(repository, "rev2", files)
	moved := must[*pb.IndexRepositoryResponse](t, tr, index)
	status = &pb.Index{
		Name: name, Documents: 2, Bytes: size(files), Revision: "rev2",
		CheckoutId: created(3), UpdatedAt: updated, Target: repository.proto(),
	}
	equal(t, "moved", moved, &pb.IndexRepositoryResponse{Index: status, Removed: 1, Replaced: 1, Unchanged: 1, Files: &pb.FileState{}})

	list := must[*pb.ListIndexesResponse](t, tr, &pb.ListIndexesRequest{})
	equal(t, "list", list, &pb.ListIndexesResponse{Indexes: []*pb.Index{status}})

	query := &pb.CodeQuery{Text: "Greet"}
	search := &pb.SearchRepositoryRequest{Target: repository.proto(), Query: query}
	indexed := must[*pb.SearchRepositoryResponse](t, tr, search)
	byName := must[*pb.SearchIndexResponse](t, tr, &pb.SearchIndexRequest{Index: name, Query: query})
	equal(t, "indexed", indexed, &pb.SearchRepositoryResponse{
		Matches: byName.GetMatches(), State: byName.GetState(), Revision: "rev2", Index: status,
	})
	if len(indexed.GetMatches()) != 5 {
		t.Fatalf("matches: %v", indexed.GetMatches())
	}

	must[*pb.DeleteIndexResponse](t, tr, &pb.DeleteIndexRequest{Index: name})
	unindexed := must[*pb.SearchRepositoryResponse](t, tr, search)
	equal(t, "unindexed", unindexed, &pb.SearchRepositoryResponse{
		Matches: indexed.GetMatches(), State: indexed.GetState(), Revision: "rev2", Files: &pb.FileState{},
	})

	if code := tr.call(t, &pb.GetIndexRequest{Index: name}, &pb.GetIndexResponse{}); code != codes.NotFound {
		t.Fatalf("unindexed search left an index: %v", code)
	}
	return []proto.Message{first, again, moved, list, indexed, unindexed}
}

// TestRepositoryErrors checks the code of each failure, that every
// checkout made is removed, and that a failure before the checkout leaves
// no index.
func TestRepositoryErrors(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.MaxIndexes = 1 })
	f.add(t, checkoutID, "rev0", sample())
	f.checkouts.branch(repository, "rev1", sample())
	query := &pb.CodeQuery{Text: "Greet"}
	missing := Target{Source: "github", Repository: "octo/demo", Reference: "missing"}

	for name, test := range map[string]struct {
		req   proto.Message
		fail  error
		want  codes.Code
		index bool
	}{
		"missing reference": {req: &pb.IndexRepositoryRequest{Target: missing.proto()}, want: codes.NotFound},
		"missing search":    {req: &pb.SearchRepositoryRequest{Target: missing.proto(), Query: query}, want: codes.NotFound},
		"rejected":          {req: &pb.IndexRepositoryRequest{Target: repository.proto()}, fail: ErrRejected, want: codes.InvalidArgument},
		"too large":         {req: &pb.IndexRepositoryRequest{Target: repository.proto()}, fail: ErrTooLarge, want: codes.ResourceExhausted},
		"conflict":          {req: &pb.SearchRepositoryRequest{Target: repository.proto(), Query: query}, fail: ErrConflict, want: codes.FailedPrecondition},
		"unavailable":       {req: &pb.IndexRepositoryRequest{Target: repository.proto()}, fail: errors.New("dial /run/secret"), want: codes.Unavailable},
		"bad query":         {req: &pb.SearchRepositoryRequest{Target: repository.proto(), Query: &pb.CodeQuery{}}, want: codes.InvalidArgument},
		"bad limit":         {req: &pb.SearchRepositoryRequest{Target: repository.proto(), Query: &pb.CodeQuery{Text: "x", MaxResults: -1}}, want: codes.InvalidArgument},
		"too many indexes":  {req: &pb.IndexRepositoryRequest{Target: repository.proto()}, want: codes.ResourceExhausted, index: true},
	} {
		for _, tr := range transports(t, f.s, "Bearer "+token) {
			if test.index {
				must[*pb.IndexCheckoutResponse](t, tr, &pb.IndexCheckoutRequest{Index: "other", CheckoutId: checkoutID})
			}
			f.checkouts.change(func(c *fakeCheckouts) { c.createErr = test.fail })
			if got := tr.call(t, test.req, response(test.req)); got != test.want {
				t.Errorf("%s %s: %v, want %v", tr.name, name, got, test.want)
			}
			f.checkouts.change(func(c *fakeCheckouts) { c.createErr = nil })

			list := must[*pb.ListIndexesResponse](t, tr, &pb.ListIndexesRequest{})
			if want := map[bool]int{true: 1}[test.index]; len(list.GetIndexes()) != want {
				t.Errorf("%s %s: indexes %v", tr.name, name, list.GetIndexes())
			}
			if test.index {
				must[*pb.DeleteIndexResponse](t, tr, &pb.DeleteIndexRequest{Index: "other"})
			}
			f.clean(t)
		}
	}
}

// TestCheckoutRemovedOnEnd ends calls by their timeout and by
// cancellation while the checkout exists, and checks it is removed.
func TestCheckoutRemovedOnEnd(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.IndexTimeout = 100 * time.Millisecond })
	f.checkouts.branch(repository, "rev1", sample())
	f.checkouts.change(func(c *fakeCheckouts) { c.creating = func(ctx context.Context) { <-ctx.Done() } })

	reqs := []proto.Message{
		&pb.IndexRepositoryRequest{Target: repository.proto()},
		&pb.SearchRepositoryRequest{Target: repository.proto(), Query: &pb.CodeQuery{Text: "Greet"}},
	}
	for _, tr := range transports(t, f.s, "Bearer "+token) {
		for _, req := range reqs {
			if got := tr.call(t, req, response(req)); got != codes.DeadlineExceeded {
				t.Fatalf("%s %s: %v", tr.name, method(req), got)
			}
			f.clean(t)
		}
	}

	ctx, cancel := context.WithCancel(authorized(context.Background()))
	f.checkouts.change(func(c *fakeCheckouts) { c.creating = func(context.Context) { cancel() } })
	_, err := f.s.IndexRepository(ctx, &pb.IndexRepositoryRequest{Target: repository.proto()})
	if status.Code(err) != codes.Canceled {
		t.Fatalf("canceled: %v", err)
	}
	f.clean(t)
	if len(f.checkouts.created) != 5 {
		t.Fatalf("created %v", f.checkouts.created)
	}
}

// TestFailedRemoval keeps a checkout forge-checkouts did not remove in the
// journal, and removes it in the sweep before the next checkout.
func TestFailedRemoval(t *testing.T) {
	f := newFixture(t, nil)
	f.checkouts.branch(repository, "rev1", sample())
	tr := transports(t, f.s, "Bearer "+token)[0]
	search := &pb.SearchRepositoryRequest{Target: repository.proto(), Query: &pb.CodeQuery{Text: "Greet"}}

	f.checkouts.change(func(c *fakeCheckouts) { c.removeErr = errUnavailable })
	must[*pb.SearchRepositoryResponse](t, tr, search)
	journaled, err := f.journal.List()
	if err != nil || len(journaled) != 1 || journaled[0] != created(1) {
		t.Fatalf("journal %v %v", journaled, err)
	}

	f.checkouts.change(func(c *fakeCheckouts) { c.removeErr = nil })
	must[*pb.SearchRepositoryResponse](t, tr, search)
	f.clean(t)
}

// TestSweep removes journaled checkouts at start, keeps those in use, and
// never creates one.
func TestSweep(t *testing.T) {
	f := newFixture(t, nil)
	f.add(t, checkoutID, "rev1", sample())
	f.add(t, otherID, "rev1", sample())
	for _, id := range []string{checkoutID, otherID, created(9)} {
		if err := f.journal.Add(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.track(otherID); err != nil {
		t.Fatal(err)
	}

	f.s.Sweep(context.Background())
	journaled, err := f.journal.List()
	if err != nil || len(journaled) != 1 || journaled[0] != otherID {
		t.Fatalf("journal %v %v", journaled, err)
	}
	if _, err := f.checkouts.Checkout(context.Background(), checkoutID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("leftover not removed: %v", err)
	}
	if len(f.checkouts.created) != 0 {
		t.Fatalf("created %v", f.checkouts.created)
	}
}

// TestConcurrentIndexRepository starts a second indexing of a branch while
// the first holds its checkout: it is refused before checking anything
// out, and the index holds only the commit the first indexed.
func TestConcurrentIndexRepository(t *testing.T) {
	f := newFixture(t, nil)
	old := sample()
	f.checkouts.branch(repository, "rev1", old)
	tr := transports(t, f.s, "Bearer "+token)[1]
	index := &pb.IndexRepositoryRequest{Target: repository.proto()}
	must[*pb.IndexRepositoryResponse](t, tr, index)

	f.checkouts.branch(repository, "rev2", map[string]string{"new.go": "package lib // Renamed\n"})
	started, release := make(chan struct{}), make(chan struct{})
	f.checkouts.change(func(c *fakeCheckouts) {
		c.creating = func(context.Context) {
			close(started)
			<-release
		}
	})

	var wg sync.WaitGroup
	var first *pb.IndexRepositoryResponse
	var firstErr error
	wg.Go(func() {
		first, firstErr = f.s.IndexRepository(authorized(context.Background()), index)
	})
	<-started
	f.checkouts.change(func(c *fakeCheckouts) { c.creating = nil })
	for _, tr := range transports(t, f.s, "Bearer "+token) {
		if got := tr.call(t, index, response(index)); got != codes.Aborted {
			t.Fatalf("%s concurrent: %v", tr.name, got)
		}
	}
	close(release)
	wg.Wait()

	if firstErr != nil || first.GetIndex().GetRevision() != "rev2" || first.GetIndex().GetDocuments() != 1 {
		t.Fatalf("first: %v %v", first, firstErr)
	}
	if len(f.checkouts.created) != 2 {
		t.Fatalf("created %v", f.checkouts.created)
	}
	for text, want := range map[string]int{"Greet": 0, "Renamed": 1} {
		got := must[*pb.SearchRepositoryResponse](t, tr, &pb.SearchRepositoryRequest{Target: repository.proto(), Query: &pb.CodeQuery{Text: text}})
		if len(got.GetMatches()) != want || got.GetRevision() != "rev2" {
			t.Fatalf("%s: %v", text, got)
		}
	}
	f.clean(t)
}

// TestTemporaryCheckoutLimit refuses a checkout over MaxTemporaryCheckouts
// without waiting.
func TestTemporaryCheckoutLimit(t *testing.T) {
	f := newFixture(t, nil)
	f.checkouts.branch(repository, "rev1", sample())
	started, release := make(chan struct{}), make(chan struct{})
	f.checkouts.change(func(c *fakeCheckouts) {
		c.creating = func(context.Context) {
			started <- struct{}{}
			<-release
		}
	})
	search := &pb.SearchRepositoryRequest{Target: repository.proto(), Query: &pb.CodeQuery{Text: "Greet"}}

	var wg sync.WaitGroup
	errs := make(chan error, MaxTemporaryCheckouts)
	for range MaxTemporaryCheckouts {
		wg.Go(func() {
			_, err := f.s.SearchRepository(authorized(context.Background()), search)
			errs <- err
		})
		<-started
	}
	for _, tr := range transports(t, f.s, "Bearer "+token) {
		if got := tr.call(t, search, response(search)); got != codes.ResourceExhausted {
			t.Fatalf("%s over the limit: %v", tr.name, got)
		}
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(f.checkouts.created) != MaxTemporaryCheckouts {
		t.Fatalf("created %v", f.checkouts.created)
	}
	f.clean(t)
}

// TestInvalidTargets refuses targets before any checkout or index.
func TestInvalidTargets(t *testing.T) {
	f := newFixture(t, nil)
	f.add(t, checkoutID, "rev1", sample())
	long := strings.Repeat("a", 257)

	targets := []*pb.RepositoryTarget{
		nil,
		{Repository: "octo/demo"},
		{Source: "git/hub", Repository: "octo/demo"},
		{Source: strings.Repeat("s", MaxSourceBytes+1), Repository: "octo/demo"},
		{Source: "github", Repository: "demo"},
		{Source: "github", Repository: "octo/../demo"},
		{Source: "github", Repository: "octo/./demo"},
		{Source: "github", Repository: "/octo/demo"},
		{Source: "github", Repository: "octo//demo"},
		{Source: "github", Repository: "octo/demo/"},
		{Source: "github", Repository: "octo/de mo"},
		{Source: "github", Repository: "octo/" + long},
		{Source: "github", Repository: strings.Repeat("a/", 16) + "a"},
		{Source: "github", Repository: "octo/demo", Reference: "-main"},
		{Source: "github", Repository: "octo/demo", Reference: "main..x"},
		{Source: "github", Repository: "octo/demo", Reference: "main.lock"},
		{Source: "github", Repository: "octo/demo", Reference: "feature/.x"},
		{Source: "github", Repository: "octo/demo", Reference: "a:b"},
		{Source: "github", Repository: "octo/demo", Reference: "a b"},
		{Source: "github", Repository: "octo/demo", Reference: "a\x00b"},
		{Source: "github", Repository: "octo/demo", Reference: "HEAD@{1}"},
		{Source: "github", Repository: "octo/demo", Reference: "@"},
		{Source: "github", Repository: "octo/demo", Reference: "x/"},
		{Source: "github", Repository: "octo/demo", Reference: long},
		{Source: "github", Repository: "octo/demo", Reference: "日本"},
	}
	for _, tr := range transports(t, f.s, "Bearer "+token) {
		for _, target := range targets {
			for _, req := range []proto.Message{
				&pb.IndexRepositoryRequest{Target: target},
				&pb.SearchRepositoryRequest{Target: target, Query: &pb.CodeQuery{Text: "Greet"}},
			} {
				if got := tr.call(t, req, response(req)); got != codes.InvalidArgument {
					t.Errorf("%s %s %v: %v", tr.name, method(req), target, got)
				}
			}
		}

		reserved := &pb.IndexCheckoutRequest{Index: repository.index(), CheckoutId: checkoutID}
		if got := tr.call(t, reserved, response(reserved)); got != codes.InvalidArgument {
			t.Errorf("%s repository index name: %v", tr.name, got)
		}
	}
	if len(f.checkouts.created) != 0 {
		t.Fatalf("created %v", f.checkouts.created)
	}

	for _, ref := range []string{"", "main", "v1.2.3", "feature/x-y_z", "0123456789abcdef0123456789abcdef01234567", "a.b/c@d"} {
		if !validReference(ref) {
			t.Errorf("valid reference %q refused", ref)
		}
	}
}

// TestTargetIndex names each target's index by a hash, and the default
// branch the same however it is requested.
func TestTargetIndex(t *testing.T) {
	head, err := target(&pb.RepositoryTarget{Source: "github", Repository: "octo/demo", Reference: "HEAD"})
	if err != nil || head != repository {
		t.Fatalf("HEAD: %+v %v", head, err)
	}

	names := map[string]bool{}
	for _, t := range []Target{
		repository,
		{Source: "github", Repository: "octo/demo", Reference: "main"},
		{Source: "github", Repository: "octo/demo-main"},
		{Source: "gitlab", Repository: "octo/demo"},
	} {
		names[t.index()] = true
	}
	if len(names) != 4 {
		t.Fatalf("names %v", names)
	}
	for name := range names {
		if !strings.HasPrefix(name, repositoryPrefix) || len(name) != len(repositoryPrefix)+40 {
			t.Fatalf("name %q", name)
		}
	}
}

func TestDirJournal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("mode: %v %v", info, err)
	}

	for _, id := range []string{otherID, checkoutID, checkoutID} {
		if err := j.Add(id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"", "../x", "ABCDEF", strings.ToUpper(checkoutID)} {
		if err := j.Add(id); err == nil {
			t.Fatalf("added %q", id)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "notes"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	ids, err := j.List()
	if err != nil || strings.Join(ids, ",") != checkoutID+","+otherID {
		t.Fatalf("list %v %v", ids, err)
	}
	for _, id := range []string{checkoutID, checkoutID} {
		if err := j.Remove(id); err != nil {
			t.Fatal(err)
		}
	}
	if ids, err := j.List(); err != nil || len(ids) != 1 || ids[0] != otherID {
		t.Fatalf("list %v %v", ids, err)
	}
}
