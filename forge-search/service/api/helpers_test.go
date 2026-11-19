package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-search/library/sqlite"
	"github.com/r59q/forge-tools/forge-search/library/symbol"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	token = "test-token"

	// checkoutID and otherID are forge-checkouts checkout IDs.
	checkoutID = "0123456789abcdef0123456789abcdef"
	otherID    = "fedcba9876543210fedcba9876543210"
)

// repository is the default branch of a test repository.
var repository = Target{Source: "github", Repository: "octo/demo"}

// now is the time every test server records.
var now = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

// analyzer is the symbol analyzer of every test server. Its runtime is
// shared, as it compiles each grammar once.
var analyzer = sync.OnceValue(func() *symbols.Analyzer { return symbols.New(symbols.Limits{}) })

// sample is a small checkout: Greet is defined in one file and called from
// two others.
func sample() map[string]string {
	return map[string]string{
		"cmd/main.go":       "package main\n\nfunc main() {\n\tprintln(lib.Greet(\"world\"))\n}\n",
		"lib/greet.go":      "package lib\n\n// Greet returns a greeting.\nfunc Greet(name string) string {\n\treturn \"hello \" + name\n}\n",
		"lib/greet_test.go": "package lib\n\nfunc TestGreet(t *testing.T) {\n\t_ = Greet(\"x\")\n}\n",
	}
}

// fakeCheckouts holds the checkouts a test server resolves, and the
// branches it checks out.
type fakeCheckouts struct {
	mu        sync.Mutex
	checkouts map[string]Checkout
	err       error
	calls     int

	// root holds checkout directories; branches are what Create checks out.
	root     string
	branches map[Target]branch
	next     int

	// createErr and removeErr fail Create and Remove; created and removed
	// list their IDs; creating, when set, runs in Create before it returns.
	createErr, removeErr error
	created, removed     []string
	creating             func(context.Context)
}

// branch is the commit and files a fake reference points at.
type branch struct {
	revision string
	files    map[string]string
}

func (f *fakeCheckouts) Checkout(_ context.Context, id string) (Checkout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	if f.err != nil {
		return Checkout{}, f.err
	}
	c, ok := f.checkouts[id]
	if !ok {
		return Checkout{}, ErrNotFound
	}
	return c, nil
}

func (f *fakeCheckouts) Create(ctx context.Context, target Target) (Checkout, error) {
	f.mu.Lock()
	hook := f.creating
	b, ok := f.branches[target]
	err := f.createErr
	f.next++
	id := fmt.Sprintf("%032x", f.next)
	f.mu.Unlock()

	switch {
	case err != nil:
		return Checkout{}, err
	case !ok:
		return Checkout{}, ErrNotFound
	}

	dir := filepath.Join(f.root, id)
	if err := writeFiles(dir, b.files); err != nil {
		return Checkout{}, err
	}
	c := Checkout{ID: id, Revision: b.revision, State: "ready", Location: dir}
	f.mu.Lock()
	f.checkouts[id] = c
	f.created = append(f.created, id)
	f.mu.Unlock()

	if hook != nil {
		hook(ctx)
	}
	return c, nil
}

func (f *fakeCheckouts) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.removeErr != nil {
		return f.removeErr
	}
	if _, ok := f.checkouts[id]; !ok {
		return ErrNotFound
	}
	delete(f.checkouts, id)
	f.removed = append(f.removed, id)
	return os.RemoveAll(filepath.Join(f.root, id))
}

func (f *fakeCheckouts) set(c Checkout) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkouts[c.ID] = c
}

func (f *fakeCheckouts) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// branch points a fake reference at a commit with files.
func (f *fakeCheckouts) branch(target Target, revision string, files map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.branches[target] = branch{revision: revision, files: files}
}

// change runs fn with the fake locked.
func (f *fakeCheckouts) change(fn func(*fakeCheckouts)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// leftovers returns the IDs of created checkouts not yet removed.
func (f *fakeCheckouts) leftovers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	var ids []string
	for _, id := range f.created {
		if _, ok := f.checkouts[id]; ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// fixture is a server with real SQLite storage and a checkouts root.
type fixture struct {
	s         *Server
	root      string
	checkouts *fakeCheckouts
	issues    *fakeIssues
	journal   *DirJournal
	store     *sqlite.Store
}

func newFixture(t *testing.T, change func(*Config)) *fixture {
	t.Helper()
	ctx := context.Background()

	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	journal, err := OpenJournal(filepath.Join(t.TempDir(), "checkouts"))
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	f := &fixture{
		root:      root,
		checkouts: &fakeCheckouts{checkouts: map[string]Checkout{}, root: root, branches: map[Target]branch{}},
		issues:    &fakeIssues{},
		journal:   journal,
		store:     store,
	}
	config := Config{
		Engine:       search.NewEngine(store, search.IndexLimits{}),
		Checkouts:    f.checkouts,
		Issues:       f.issues,
		Journal:      journal,
		Root:         Root{Local: f.root},
		Token:        forgeauth.StaticToken(token),
		Timeout:      10 * time.Second,
		IndexTimeout: 10 * time.Second,
		Now:          func() time.Time { return now },
		Analyzer:     analyzer(),
	}
	if change != nil {
		change(&config)
	}

	f.s, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// add writes the files of a ready checkout and makes it resolvable.
func (f *fixture) add(t *testing.T, id, revision string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(f.root, id)
	if err := writeFiles(dir, files); err != nil {
		t.Fatal(err)
	}
	f.checkouts.set(Checkout{ID: id, Revision: revision, State: "ready", Location: dir})
}

// clean fails the test if a created checkout, or its journal entry, is
// left.
func (f *fixture) clean(t *testing.T) {
	t.Helper()
	journaled, err := f.journal.List()
	if err != nil {
		t.Fatal(err)
	}
	if left := f.checkouts.leftovers(); len(left) > 0 || len(journaled) > 0 {
		t.Fatalf("checkouts left %v, journaled %v", left, journaled)
	}
}

// writeFiles replaces dir with the files.
func writeFiles(dir string, files map[string]string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// transport calls one RPC, named by its request type, returning the status
// code.
type transport struct {
	name string
	call func(t *testing.T, req, resp proto.Message) codes.Code
}

// transports serves s over in-process gRPC, with GRPCOptions, and over its
// REST handler.
func transports(t *testing.T, s *Server, authorization string) []transport {
	t.Helper()
	return []transport{
		{name: "gRPC", call: grpcCaller(t, s, authorization)},
		{name: "REST", call: restCaller(t, s, authorization)},
	}
}

// method is the RPC name of a request message.
func method(req proto.Message) string {
	return strings.TrimSuffix(string(req.ProtoReflect().Descriptor().Name()), "Request")
}

func grpcCaller(t *testing.T, s *Server, authorization string) func(*testing.T, proto.Message, proto.Message) codes.Code {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	g := grpc.NewServer(s.GRPCOptions()...)
	pb.RegisterSearchServiceServer(g, s)
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

	return func(t *testing.T, req, resp proto.Message) codes.Code {
		t.Helper()
		ctx := context.Background()
		if authorization != "" {
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", authorization)
		}

		err := conn.Invoke(ctx, "/forge.search.v1.SearchService/"+method(req), req, resp)
		// Errors of the server's own RPCs carry only the code; those of
		// grpc-go, such as the size limit, never hold paths or tokens.
		if msg := status.Convert(err).Message(); err != nil && (strings.ContainsAny(msg, "/\\") || strings.Contains(msg, token)) {
			t.Fatalf("error message %q holds a path or token", msg)
		}
		return status.Code(err)
	}
}

// route is the REST method and path of a request.
func route(req proto.Message) (string, string) {
	switch r := req.(type) {
	case *pb.CreateIndexRequest:
		return http.MethodPost, "/v1/indexes"
	case *pb.PutDocumentRequest:
		return http.MethodPost, "/v1/documents:put"
	case *pb.RemoveDocumentRequest:
		return http.MethodPost, "/v1/documents:remove"
	case *pb.SearchDocumentsRequest:
		return http.MethodPost, "/v1/documents:search"
	case *pb.IndexCheckoutRequest:
		return http.MethodPost, "/v1/indexes/" + url.PathEscape(r.GetIndex()) + "/checkout"
	case *pb.ListIndexesRequest:
		return http.MethodGet, "/v1/indexes"
	case *pb.GetIndexRequest:
		return http.MethodGet, "/v1/indexes/" + url.PathEscape(r.GetIndex())
	case *pb.DeleteIndexRequest:
		return http.MethodDelete, "/v1/indexes/" + url.PathEscape(r.GetIndex())
	case *pb.SearchIndexRequest:
		return http.MethodPost, "/v1/indexes/" + url.PathEscape(r.GetIndex()) + ":search"
	case *pb.SearchCheckoutRequest:
		return http.MethodPost, "/v1/checkouts/" + url.PathEscape(r.GetCheckoutId()) + ":search"
	case *pb.IndexRepositoryRequest:
		return http.MethodPost, "/v1/repositories:index"
	case *pb.SearchRepositoryRequest:
		return http.MethodPost, "/v1/repositories:search"
	case *pb.IndexIssuesRequest:
		return http.MethodPost, "/v1/issues:index"
	case *pb.SearchIndexedIssuesRequest:
		return http.MethodPost, "/v1/issues:searchIndexed"
	case *pb.SearchUnindexedIssuesRequest:
		return http.MethodPost, "/v1/issues:searchUnindexed"
	case *pb.SearchIndexSymbolsRequest:
		return http.MethodPost, "/v1/indexes/" + url.PathEscape(r.GetIndex()) + "/symbols:search"
	case *pb.SearchCheckoutSymbolsRequest:
		return http.MethodPost, "/v1/checkouts/" + url.PathEscape(r.GetCheckoutId()) + "/symbols:search"
	}
	panic("no route for " + method(req))
}

func restCaller(t *testing.T, s *Server, authorization string) func(*testing.T, proto.Message, proto.Message) codes.Code {
	t.Helper()
	handler, err := s.REST(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	return func(t *testing.T, req, resp proto.Message) codes.Code {
		t.Helper()
		httpMethod, path := route(req)
		var body []byte
		if httpMethod == http.MethodPost {
			var err error
			if body, err = protojson.Marshal(req); err != nil {
				t.Fatal(err)
			}
		}

		rec := rest(handler, httpMethod, path, body, int64(len(body)), authorization)
		if rec.Code != http.StatusOK {
			return restCode(t, rec)
		}
		if err := protojson.Unmarshal(rec.Body.Bytes(), resp); err != nil {
			t.Fatalf("decode %s: %v", rec.Body, err)
		}
		return codes.OK
	}
}

func rest(handler http.Handler, httpMethod, path string, body []byte, length int64, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(httpMethod, path, bytes.NewReader(body))
	req.ContentLength = length
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// restCode checks that an error response carries only a status code, with
// the HTTP status of that code.
func restCode(t *testing.T, rec *httptest.ResponseRecorder) codes.Code {
	t.Helper()
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", rec.Header())
	}

	var e struct {
		Code    codes.Code `json:"code"`
		Message string     `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("error body %q: %v", rec.Body, err)
	}
	if e.Message != e.Code.String() {
		t.Fatalf("error message %q echoes more than the code %v", e.Message, e.Code)
	}
	if want := httpStatus(e.Code); rec.Code != want {
		t.Fatalf("HTTP %d for %v, want %d", rec.Code, e.Code, want)
	}
	return e.Code
}

func httpStatus(code codes.Code) int {
	return map[codes.Code]int{
		codes.Unauthenticated:    http.StatusUnauthorized,
		codes.PermissionDenied:   http.StatusForbidden,
		codes.Unimplemented:      http.StatusNotImplemented,
		codes.InvalidArgument:    http.StatusBadRequest,
		codes.NotFound:           http.StatusNotFound,
		codes.FailedPrecondition: http.StatusBadRequest,
		codes.AlreadyExists:      http.StatusConflict,
		codes.Aborted:            http.StatusConflict,
		codes.ResourceExhausted:  http.StatusTooManyRequests,
		codes.Unavailable:        http.StatusServiceUnavailable,
		codes.DeadlineExceeded:   http.StatusGatewayTimeout,
		codes.Internal:           http.StatusInternalServerError,
	}[code]
}

// requests holds one valid request of every RPC.
func requests() []proto.Message {
	query := &pb.CodeQuery{Text: "Greet"}
	return []proto.Message{
		&pb.CreateIndexRequest{Index: "main"},
		&pb.PutDocumentRequest{Index: "main", Document: &pb.Document{Path: "file"}},
		&pb.RemoveDocumentRequest{Index: "main", Path: "file"},
		&pb.SearchDocumentsRequest{Text: "Greet"},
		&pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID},
		&pb.ListIndexesRequest{},
		&pb.GetIndexRequest{Index: "main"},
		&pb.DeleteIndexRequest{Index: "main"},
		&pb.SearchIndexRequest{Index: "main", Query: query},
		&pb.SearchCheckoutRequest{CheckoutId: checkoutID, Query: query},
		&pb.IndexRepositoryRequest{Target: repository.proto()},
		&pb.SearchRepositoryRequest{Target: repository.proto(), Query: query},
		&pb.IndexIssuesRequest{Repository: issueRepo},
		&pb.SearchIndexedIssuesRequest{Repository: issueRepo, Query: &pb.IssueQuery{Text: "crash"}},
		&pb.SearchUnindexedIssuesRequest{Repository: issueRepo, Query: &pb.IssueQuery{Text: "crash"}},
		&pb.SearchIndexSymbolsRequest{Index: "main", Query: &pb.SymbolQuery{Text: "Greet"}},
		&pb.SearchCheckoutSymbolsRequest{CheckoutId: checkoutID, Query: &pb.SymbolQuery{Text: "Greet"}},
	}
}

// response returns an empty response for a request.
func response(req proto.Message) proto.Message {
	name := strings.TrimSuffix(string(req.ProtoReflect().Descriptor().FullName()), "Request") + "Response"
	for _, m := range []proto.Message{
		&pb.CreateIndexResponse{}, &pb.PutDocumentResponse{}, &pb.RemoveDocumentResponse{}, &pb.SearchDocumentsResponse{},
		&pb.IndexCheckoutResponse{}, &pb.ListIndexesResponse{}, &pb.GetIndexResponse{},
		&pb.DeleteIndexResponse{}, &pb.SearchIndexResponse{}, &pb.SearchCheckoutResponse{},
		&pb.IndexRepositoryResponse{}, &pb.SearchRepositoryResponse{},
		&pb.IndexIssuesResponse{}, &pb.SearchIndexedIssuesResponse{}, &pb.SearchUnindexedIssuesResponse{},
		&pb.SearchIndexSymbolsResponse{}, &pb.SearchCheckoutSymbolsResponse{},
	} {
		if string(m.ProtoReflect().Descriptor().FullName()) == name {
			return m
		}
	}
	panic("no response for " + name)
}

// must calls an RPC that must succeed and returns its response.
func must[T proto.Message](t *testing.T, tr transport, req proto.Message) T {
	t.Helper()
	resp := response(req)
	if got := tr.call(t, req, resp); got != codes.OK {
		t.Fatalf("%s %s: %v", tr.name, method(req), got)
	}
	return resp.(T)
}

func equal(t *testing.T, name string, got, want proto.Message) {
	t.Helper()
	if !proto.Equal(got, want) {
		t.Errorf("%s:\n got %v\nwant %v", name, got, want)
	}
}

// symbolBytes returns the content size of the definitions an index holds.
func (f *fixture) symbolBytes(t *testing.T, index string) int64 {
	t.Helper()
	var n int64
	err := f.s.config.Engine.Read(context.Background(), index, "", search.Filter{Kind: symbol.KindSymbol}, func(d search.Document) bool {
		n += int64(len(d.Content))
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// indexSymbols is the complete symbol state of an index with documents
// files holding definitions and unsupported files without.
func indexSymbols(documents int64, unsupported int32) *pb.IndexSymbols {
	return &pb.IndexSymbols{Complete: true, Documents: documents, Skipped: &pb.SymbolFileState{Unsupported: unsupported}}
}

// passOf is a symbol pass that left out unsupported files only.
func passOf(stored, definitions, removed, unsupported int32, full bool) *pb.SymbolPass {
	return &pb.SymbolPass{
		Stored: stored, Definitions: definitions, Removed: removed,
		Skipped: &pb.SymbolFileState{Unsupported: unsupported}, Full: full,
	}
}
