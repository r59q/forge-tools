package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-search/library/checkout"
	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-search/library/sqlite"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestNew(t *testing.T) {
	valid := Config{
		Engine:       search.NewEngine(&search.MemoryStore{}, search.IndexLimits{}),
		Checkouts:    &fakeCheckouts{},
		Journal:      &DirJournal{dir: "/journal"},
		Root:         Root{Local: "/checkouts"},
		Token:        forgeauth.StaticToken(token),
		Timeout:      time.Second,
		IndexTimeout: time.Second,
		Analyzer:     analyzer(),
	}
	if _, err := New(valid); err != nil {
		t.Fatal(err)
	}

	for name, change := range map[string]func(*Config){
		"no engine":         func(c *Config) { c.Engine = nil },
		"no checkouts":      func(c *Config) { c.Checkouts = nil },
		"no journal":        func(c *Config) { c.Journal = nil },
		"no token":          func(c *Config) { c.Token = nil },
		"no timeout":        func(c *Config) { c.Timeout = 0 },
		"no index timeout":  func(c *Config) { c.IndexTimeout = 0 },
		"negative indexes":  func(c *Config) { c.MaxIndexes = -1 },
		"no root":           func(c *Config) { c.Root = Root{} },
		"relative root":     func(c *Config) { c.Root = Root{Local: "checkouts"} },
		"unclean root":      func(c *Config) { c.Root = Root{Local: "/checkouts/"} },
		"relative reported": func(c *Config) { c.Root.Reported = "copies" },
		"no analyzer":       func(c *Config) { c.Analyzer = nil },
		"negative parses":   func(c *Config) { c.SymbolParses = -1 },
	} {
		config := valid
		change(&config)
		if _, err := New(config); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestAuthentication checks every RPC on both transports, and that no
// rejected call reaches forge-checkouts or forge-issues.
func TestAuthentication(t *testing.T) {
	f := newFixture(t, nil)
	f.add(t, checkoutID, "rev1", sample())

	for _, authorization := range []string{"", "Bearer wrong", "Basic " + token, "Bearer " + token + "x"} {
		for _, tr := range transports(t, f.s, authorization) {
			for _, req := range requests() {
				if got := tr.call(t, req, response(req)); got != codes.Unauthenticated {
					t.Errorf("%s %s %q: %v", tr.name, method(req), authorization, got)
				}
			}
		}
	}
	if f.checkouts.calls != 0 || len(f.checkouts.created) != 0 {
		t.Fatalf("%d checkout lookups, %d created", f.checkouts.calls, len(f.checkouts.created))
	}
	if n := f.issues.called(); n != 0 {
		t.Fatalf("%d issue listings", n)
	}
}

// TestRESTChecksBeforeReading sends a Content-Length over the limit: the
// token is checked first, then the length, before the body is read.
func TestRESTChecksBeforeReading(t *testing.T) {
	f := newFixture(t, nil)
	handler, err := f.s.REST(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	path := "/v1/indexes/main:search"
	if rec := rest(handler, http.MethodPost, path, []byte("{}"), RequestBytes+1, ""); restCode(t, rec) != codes.Unauthenticated {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	if rec := rest(handler, http.MethodPost, path, []byte("{}"), RequestBytes+1, "Bearer "+token); restCode(t, rec) != codes.ResourceExhausted {
		t.Fatalf("too long: %d", rec.Code)
	}
}

// TestRequestSize sends requests over RequestBytes on both transports.
func TestRequestSize(t *testing.T) {
	f := newFixture(t, nil)
	req := &pb.SearchIndexRequest{Index: "main", Query: &pb.CodeQuery{Text: string(make([]byte, RequestBytes))}}
	for _, tr := range transports(t, f.s, "Bearer "+token) {
		if got := tr.call(t, req, &pb.SearchIndexResponse{}); got != codes.ResourceExhausted {
			t.Errorf("%s: %v", tr.name, got)
		}
	}
}

func TestTimeout(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Timeout = time.Nanosecond })
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	if _, err := f.s.ListIndexes(ctx, &pb.ListIndexesRequest{}); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("got %v", err)
	}
}

// TestBusy checks that an index being changed cannot be indexed or deleted
// by another call.
func TestBusy(t *testing.T) {
	f := newFixture(t, nil)
	f.add(t, checkoutID, "rev1", sample())
	tr := transports(t, f.s, "Bearer "+token)[0]
	must[*pb.IndexCheckoutResponse](t, tr, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID})

	release, err := f.s.reserve("main")
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range transports(t, f.s, "Bearer "+token) {
		if got := tr.call(t, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID}, &pb.IndexCheckoutResponse{}); got != codes.Aborted {
			t.Errorf("%s index: %v", tr.name, got)
		}
		if got := tr.call(t, &pb.DeleteIndexRequest{Index: "main"}, &pb.DeleteIndexResponse{}); got != codes.Aborted {
			t.Errorf("%s delete: %v", tr.name, got)
		}
		must[*pb.SearchIndexResponse](t, tr, &pb.SearchIndexRequest{Index: "main", Query: &pb.CodeQuery{Text: "Greet"}})
	}

	release()
	must[*pb.DeleteIndexResponse](t, tr, &pb.DeleteIndexRequest{Index: "main"})
}

func TestMaxIndexes(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.MaxIndexes = 1 })
	f.add(t, checkoutID, "rev1", sample())
	tr := transports(t, f.s, "Bearer "+token)[1]

	must[*pb.IndexCheckoutResponse](t, tr, &pb.IndexCheckoutRequest{Index: "one", CheckoutId: checkoutID})
	if got := tr.call(t, &pb.IndexCheckoutRequest{Index: "two", CheckoutId: checkoutID}, &pb.IndexCheckoutResponse{}); got != codes.ResourceExhausted {
		t.Fatalf("second index: %v", got)
	}
	must[*pb.IndexCheckoutResponse](t, tr, &pb.IndexCheckoutRequest{Index: "one", CheckoutId: checkoutID})

	list := must[*pb.ListIndexesResponse](t, tr, &pb.ListIndexesRequest{})
	if len(list.GetIndexes()) != 1 {
		t.Fatalf("indexes: %v", list)
	}
}

// TestIndexLimit indexes a checkout over the index size limit.
func TestIndexLimit(t *testing.T) {
	f := newFixture(t, func(c *Config) {
		c.Engine = search.NewEngine(&search.MemoryStore{}, search.IndexLimits{Documents: 2})
	})
	f.add(t, checkoutID, "rev1", sample())
	for _, tr := range transports(t, f.s, "Bearer "+token) {
		if got := tr.call(t, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID}, &pb.IndexCheckoutResponse{}); got != codes.ResourceExhausted {
			t.Errorf("%s: %v", tr.name, got)
		}
	}
}

func TestCode(t *testing.T) {
	invalidQuery := fmt.Errorf("query: %w", search.ErrInvalid)
	unreadable := fmt.Errorf("a.go: %w", checkout.ErrUnreadable)
	other := errors.New("other")

	for err, want := range map[error]codes.Code{
		context.Canceled:         codes.Canceled,
		context.DeadlineExceeded: codes.DeadlineExceeded,
		errBadRequest:            codes.InvalidArgument,
		ErrIssuesRejected:        codes.InvalidArgument,
		ErrIssuesDenied:          codes.PermissionDenied,
		errNoIssues:              codes.Unimplemented,
		ErrIssuesExhausted:       codes.ResourceExhausted,
		errIssuesUnavailable:     codes.Unavailable,
		invalidQuery:             codes.InvalidArgument,
		ErrNotFound:              codes.NotFound,
		search.ErrNotFound:       codes.NotFound,
		search.ErrExists:         codes.AlreadyExists,
		errNotReady:              codes.FailedPrecondition,
		errNotReadable:           codes.FailedPrecondition,
		unreadable:               codes.FailedPrecondition,
		errBusy:                  codes.Aborted,
		checkout.ErrChanged:      codes.Aborted,
		errTooMany:               codes.ResourceExhausted,
		search.ErrTooLarge:       codes.ResourceExhausted,
		search.ErrFull:           codes.ResourceExhausted,
		sqlite.ErrDiskFull:       codes.ResourceExhausted,
		errUnavailable:           codes.Unavailable,
		sqlite.ErrReadOnly:       codes.Unavailable,
		sqlite.ErrStorage:        codes.Internal,
		other:                    codes.Internal,
	} {
		if got := code(err); got != want {
			t.Errorf("%v: got %v, want %v", err, got, want)
		}
	}
}
