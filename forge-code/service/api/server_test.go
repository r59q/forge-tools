package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/code"
	pb "github.com/r59q/forge-tools/forge-code/service/codepb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const serviceToken = "service-secret"

// env is a Server over the fake forge with sources gh and gh2 (no Git
// remote) and git (with one), served over REST and gRPC.
type env struct {
	s   *Server
	h   http.Handler
	f   *forge
	rpc pb.CodeClient
}

func setup(t *testing.T, cfg Config, limits code.Limits) *env {
	t.Helper()
	f := newForge(t)
	ctx := context.Background()
	session, err := github.Provider{}.Open(ctx, forgeauth.Connection{BaseURL: f.srv.URL}, forgeauth.StaticToken(forgeToken))
	if err != nil {
		t.Fatal(err)
	}
	client, err := code.NewWithLimits(limits,
		code.Source{ID: "gh", Session: session},
		code.Source{ID: "gh2", Session: session},
		code.Source{ID: "git", Session: session, Git: &forgeauth.GitRemote{BaseURL: f.srv.URL, Client: f.srv.Client()}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}
	s, err := New(Fixed(client), forgeauth.StaticToken(serviceToken), cfg)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.REST(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(s.GRPCOptions()...)
	pb.RegisterCodeServer(srv, s)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(8<<20), grpc.MaxCallSendMsgSize(MaxRecvMsgSize)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &env{s: s, h: h, f: f, rpc: pb.NewCodeClient(conn)}
}

// authed adds the service token to ctx.
func authed() context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+serviceToken)
}

// do sends a REST request and returns the response, checking the headers
// every response carries and that no forge data leaks.
func do(t *testing.T, h http.Handler, method, target string, body io.Reader, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, body)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("%s %s: Cache-Control = %q", method, target, got)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("%s %s: X-Content-Type-Options = %q", method, target, got)
	}
	if strings.Contains(w.Body.String(), forgeToken) {
		t.Fatalf("%s %s leaked forge data: %s", method, target, w.Body)
	}
	return w
}

// rest sends a JSON request and returns the status and compact JSON body.
func rest(t *testing.T, h http.Handler, method, target, body, token string) (int, string) {
	t.Helper()
	w := do(t, h, method, target, strings.NewReader(body), token)
	return w.Code, compact(t, w.Body.Bytes())
}

// get fetches a REST read on mrbean/teddy as source gh and decodes it.
func get(t *testing.T, h http.Handler, path, query string) map[string]any {
	t.Helper()
	target := "/v1/" + path + "?source=gh&repository=mrbean/teddy"
	if query != "" {
		target += "&" + query
	}
	code, body := rest(t, h, "GET", target, "", serviceToken)
	if code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", target, code, body)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestNew(t *testing.T) {
	client, _ := code.New()
	token := forgeauth.StaticToken("x")
	for _, tc := range []struct {
		sources Sources
		token   forgeauth.Credentials
		cfg     Config
	}{
		{nil, token, Config{Timeout: time.Second}},
		{Fixed(client), nil, Config{Timeout: time.Second}},
		{Fixed(client), token, Config{}},
		{Fixed(client), token, Config{Timeout: time.Second, StreamTimeout: -1}},
		{Fixed(client), token, Config{Timeout: time.Second, MaxInFlight: -1}},
	} {
		if _, err := New(tc.sources, tc.token, tc.cfg); err != ErrInvalid {
			t.Errorf("New(%+v) = %v, want ErrInvalid", tc.cfg, err)
		}
	}
	s, err := New(Fixed(client), token, Config{Timeout: time.Second})
	if err != nil || s.streamTimeout != DefaultStreamTimeout || s.inFlight.max != DefaultMaxInFlight {
		t.Fatalf("defaults: %+v %v", s, err)
	}
}

func TestSourcesUnavailable(t *testing.T) {
	failing := func(context.Context) (*code.Client, error) {
		return nil, errors.New("connector " + forgeToken)
	}
	s, err := New(failing, forgeauth.StaticToken(serviceToken), Config{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.REST(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/v1/sources", "/v1/raw?source=gh&repository=a/b&path=x"} {
		if w := do(t, h, "GET", target, nil, serviceToken); w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d %s", target, w.Code, w.Body)
		}
		if w := do(t, h, "GET", target, nil, "wrong"); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: unauthenticated call reached sources: %d", target, w.Code)
		}
	}
}

func TestRESTReads(t *testing.T) {
	e := setup(t, Config{}, code.Limits{})
	h := e.h

	code, body := rest(t, h, "GET", "/v1/sources", "", serviceToken)
	if want := `{"sources":[{"id":"gh","kind":"github"},{"id":"gh2","kind":"github"},{"id":"git","kind":"github"}]}`; code != 200 || body != want {
		t.Errorf("sources: %d %s", code, body)
	}
	code, body = rest(t, h, "GET", "/v1/capabilities?source=gh", "", serviceToken)
	if code != 200 || body != `{"treeIds":true,"mergeBase":true}` {
		t.Errorf("capabilities: %d %s", code, body)
	}
	code, body = rest(t, h, "GET", "/v1/capabilities?source=git", "", serviceToken)
	if code != 200 || body != `{"treeIds":true,"mergeBase":true,"writes":true}` {
		t.Errorf("git capabilities: %d %s", code, body)
	}

	for rev, want := range map[string]string{
		"":       fmt.Sprintf(`{"commit":"%s","ref":"refs/heads/main"}`, c2),
		"HEAD~1": fmt.Sprintf(`{"commit":"%s","ref":"refs/heads/main"}`, c1),
		"v1":     fmt.Sprintf(`{"commit":"%s","ref":"refs/tags/v1"}`, c1),
		c3:       fmt.Sprintf(`{"commit":"%s"}`, c3),
	} {
		code, body := rest(t, h, "GET", "/v1/resolve?source=gh&repository=mrbean/teddy&revision="+url.QueryEscape(rev), "", serviceToken)
		if code != 200 || body != want {
			t.Errorf("resolve %q: %d %s, want %s", rev, code, body, want)
		}
	}

	refs := get(t, h, "refs", "")
	got, _ := json.Marshal(refs)
	want := fmt.Sprintf(`{"defaultBranch":"refs/heads/main","refs":[{"commit":"%[2]s","name":"refs/heads/dup","target":"%[2]s"},`+
		`{"commit":"%[2]s","name":"refs/heads/main","target":"%[2]s"},{"commit":"%[3]s","name":"refs/heads/side","target":"%[3]s"},`+
		`{"commit":"%[1]s","name":"refs/tags/dup","target":"%[1]s"},{"commit":"%[1]s","name":"refs/tags/v1","target":"%[4]s"}]}`, c1, c2, c3, tagObj)
	if string(got) != want {
		t.Errorf("refs:\n got %s\nwant %s", got, want)
	}
	if refs := get(t, h, "refs", "prefix=refs/tags/&limit=1"); refs["truncated"] != true || len(refs["refs"].([]any)) != 1 {
		t.Errorf("limited refs: %v", refs)
	}

	cm := get(t, h, "commit", "revision=main")
	got, _ = json.Marshal(cm)
	person := `{"email":"bean@example.com","name":"Mr Bean","when":"2024-01-02T03:04:05Z"}`
	if want := fmt.Sprintf(`{"author":%s,"committer":%s,"id":"%s","message":"Second","parents":["%s"],"tree":"%s"}`, person, person, c2, c1, tree2); string(got) != want {
		t.Errorf("commit:\n got %s\nwant %s", got, want)
	}

	page := get(t, h, "history", "page_size=1")
	next, _ := page["next"].(string)
	if page["tip"] != c2 || len(page["commits"].([]any)) != 1 || next == "" {
		t.Fatalf("history page 1: %v", page)
	}
	page = get(t, h, "history", "cursor="+url.QueryEscape(next))
	if commits := page["commits"].([]any); page["tip"] != c2 || len(commits) != 1 || commits[0].(map[string]any)["id"] != c1 || page["next"] != nil {
		t.Errorf("history page 2: %v", page)
	}

	tree := get(t, h, "tree", "revision=main")
	if tree["commit"] != c2 || tree["id"] != tree2 || len(tree["entries"].([]any)) != 9 {
		t.Errorf("tree: %v", tree)
	}
	got, _ = json.Marshal(tree["entries"].([]any)[0])
	if want := fmt.Sprintf(`{"id":"%s","mode":"100644","name":"README.md","path":"README.md","size":"12","type":"blob"}`, readme2); string(got) != want {
		t.Errorf("tree entry %s", got)
	}
	got, _ = json.Marshal(get(t, h, "tree", "path=bin&recursive=true"))
	if want := fmt.Sprintf(`{"commit":"%s","entries":[{"id":"%s","mode":"100755","name":"run.sh","path":"bin/run.sh","size":"18","type":"blob"}],"id":"%s","path":"bin"}`, c2, script, binTree); string(got) != want {
		t.Errorf("subtree:\n got %s\nwant %s", got, want)
	}
	if tree := get(t, h, "tree", "recursive=true&max_entries=3"); tree["truncated"] != true || len(tree["entries"].([]any)) != 3 {
		t.Errorf("truncated tree: %v", tree)
	}

	for path, want := range map[string]string{
		"link": `"mode":"120000"`, "lib": `"mode":"160000","name":"lib","path":"lib","size":"-1","type":"commit"`, "bin": `"type":"tree"`,
	} {
		got, _ := json.Marshal(get(t, h, "stat", "path="+path))
		if !strings.Contains(string(got), want) || !strings.Contains(string(got), c2) {
			t.Errorf("stat %s: %s", path, got)
		}
	}

	got, _ = json.Marshal(get(t, h, "file", "path=README.md"))
	if want := fmt.Sprintf(`{"blob":{"commit":"%s","id":"%s","path":"README.md","size":"12"},"content":"%s"}`, c2, readme2, b64("hello world\n")); string(got) != want {
		t.Errorf("file:\n got %s\nwant %s", got, want)
	}
	if file := get(t, h, "file", "path=model.bin&revision=main"); file["blob"].(map[string]any)["lfsPointer"] != true {
		t.Errorf("lfs: %v", file)
	}
	got, _ = json.Marshal(get(t, h, "blob", "id="+readme1))
	if want := fmt.Sprintf(`{"blob":{"id":"%s","size":"6"},"content":"%s"}`, readme1, b64("hello\n")); string(got) != want {
		t.Errorf("blob:\n got %s\nwant %s", got, want)
	}
	if mb := get(t, h, "merge-base", "a=main&b=side"); mb["commit"] != c1 {
		t.Errorf("merge base: %v", mb)
	}
}

func TestGRPCReads(t *testing.T) {
	e := setup(t, Config{}, code.Limits{})
	c, ctx := e.rpc, authed()
	teddy := func() (string, string) { return "gh", "mrbean/teddy" }
	src, repository := teddy()

	srcs, err := c.ListSources(ctx, &emptypb.Empty{})
	if err != nil || len(srcs.GetSources()) != 3 || srcs.GetSources()[2].GetId() != "git" {
		t.Fatalf("sources: %v %v", srcs, err)
	}
	caps, err := c.GetCapabilities(ctx, &pb.CapabilitiesRequest{Source: "git"})
	if err != nil || !caps.GetWrites() || !caps.GetTreeIds() {
		t.Fatalf("capabilities: %v %v", caps, err)
	}
	res, err := c.Resolve(ctx, &pb.RevisionRequest{Source: src, Repository: repository, Revision: "refs/heads/side"})
	if err != nil || res.GetCommit() != c3 || res.GetRef() != "refs/heads/side" {
		t.Fatalf("resolve: %v %v", res, err)
	}
	refs, err := c.ListRefs(ctx, &pb.ListRefsRequest{Source: src, Repository: repository, Prefix: "refs/heads/"})
	if err != nil || len(refs.GetRefs()) != 3 || refs.GetDefaultBranch() != "refs/heads/main" {
		t.Fatalf("refs: %v %v", refs, err)
	}
	cm, err := c.GetCommit(ctx, &pb.RevisionRequest{Source: src, Repository: repository, Revision: "main^1"})
	if err != nil || cm.GetId() != c1 || len(cm.GetParents()) != 0 || cm.GetAuthor().GetWhen().AsTime() != time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) {
		t.Fatalf("commit: %v %v", cm, err)
	}
	hist, err := c.ListHistory(ctx, &pb.HistoryRequest{Source: src, Repository: repository, Revision: "side"})
	if err != nil || len(hist.GetCommits()) != 2 || hist.GetTip() != c3 || hist.GetNext() != "" {
		t.Fatalf("history: %v %v", hist, err)
	}
	tree, err := c.GetTree(ctx, &pb.TreeRequest{Source: src, Repository: repository, Recursive: true})
	if err != nil || len(tree.GetEntries()) != 10 || tree.GetEntries()[3].GetPath() != "bin/run.sh" {
		t.Fatalf("tree: %v %v", tree, err)
	}
	st, err := c.Stat(ctx, &pb.PathRequest{Source: src, Repository: repository, Path: "bin/run.sh"})
	if err != nil || st.GetEntry().GetMode() != "100755" || st.GetCommit() != c2 {
		t.Fatalf("stat: %v %v", st, err)
	}
	file, err := c.GetFile(ctx, &pb.PathRequest{Source: src, Repository: repository, Path: "four.bin"})
	if err != nil || len(file.GetContent()) != MaxFileBytes || file.GetBlob().GetId() != four {
		t.Fatalf("4 MiB file: %v", err)
	}
	file, err = c.GetFile(ctx, &pb.PathRequest{Source: src, Repository: repository, Path: "empty"})
	if err != nil || len(file.GetContent()) != 0 || file.GetBlob().GetSize() != 0 {
		t.Fatalf("empty file: %v %v", file, err)
	}
	blob, err := c.GetBlob(ctx, &pb.BlobRequest{Source: src, Repository: repository, Id: script})
	if err != nil || string(blob.GetContent()) != "#!/bin/sh\necho hi\n" || blob.GetBlob().GetCommit() != "" {
		t.Fatalf("blob: %v %v", blob, err)
	}
	mb, err := c.GetMergeBase(ctx, &pb.MergeBaseRequest{Source: src, Repository: repository, A: "side", B: c2})
	if err != nil || mb.GetCommit() != c1 {
		t.Fatalf("merge base: %v %v", mb, err)
	}
	for _, tc := range []struct {
		name string
		call func() error
		want codes.Code
	}{
		{"huge file", func() error {
			_, err := c.GetFile(ctx, &pb.PathRequest{Source: src, Repository: repository, Path: "huge.bin"})
			return err
		}, codes.OutOfRange},
		{"huge blob", func() error {
			_, err := c.GetBlob(ctx, &pb.BlobRequest{Source: src, Repository: repository, Id: huge})
			return err
		}, codes.OutOfRange},
		{"ambiguous", func() error {
			_, err := c.Resolve(ctx, &pb.RevisionRequest{Source: src, Repository: repository, Revision: "dup"})
			return err
		}, codes.FailedPrecondition},
		{"directory", func() error {
			_, err := c.GetFile(ctx, &pb.PathRequest{Source: src, Repository: repository, Path: "bin"})
			return err
		}, codes.FailedPrecondition},
		{"missing", func() error {
			_, err := c.Stat(ctx, &pb.PathRequest{Source: src, Repository: repository, Path: "nope"})
			return err
		}, codes.NotFound},
	} {
		if err := tc.call(); status.Code(err) != tc.want || status.Convert(err).Message() != tc.want.String() {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
}

// chunks reads a stream to its end.
func chunks(t *testing.T, s grpc.ServerStreamingClient[pb.Chunk]) ([]*pb.Chunk, error) {
	t.Helper()
	var out []*pb.Chunk
	for {
		c, err := s.Recv()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, c)
	}
}

func TestStreams(t *testing.T) {
	e := setup(t, Config{}, code.Limits{})
	c, ctx := e.rpc, authed()

	s, err := c.ReadFile(ctx, &pb.PathRequest{Source: "gh", Repository: "mrbean/teddy", Path: "big.bin"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := chunks(t, s)
	if err != nil || len(got) != 3 {
		t.Fatalf("big.bin: %d chunks, %v", len(got), err)
	}
	var data []byte
	for i, ch := range got {
		if (ch.GetBlob() != nil) != (i == 0) {
			t.Errorf("chunk %d blob = %v", i, ch.GetBlob())
		}
		if i < 2 && len(ch.GetData()) != ChunkBytes {
			t.Errorf("chunk %d has %d bytes", i, len(ch.GetData()))
		}
		data = append(data, ch.GetData()...)
	}
	if b := got[0].GetBlob(); string(data) != string(fx.blobs[big]) || b.GetId() != big || b.GetCommit() != c2 || b.GetPath() != "big.bin" || b.GetSize() != int64(len(data)) {
		t.Errorf("big.bin content or blob wrong: %v", b)
	}

	s, err = c.ReadBlob(ctx, &pb.BlobRequest{Source: "gh", Repository: "mrbean/teddy", Id: empty})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := chunks(t, s); err != nil || len(got) != 1 || got[0].GetBlob().GetId() != empty || len(got[0].GetData()) != 0 {
		t.Fatalf("empty blob: %v %v", got, err)
	}

	// A failure before the first chunk is a bare status.
	s, err = c.ReadFile(ctx, &pb.PathRequest{Source: "gh", Repository: "mrbean/teddy", Path: "nope"})
	if err == nil {
		_, err = chunks(t, s)
	}
	if status.Code(err) != codes.NotFound || status.Convert(err).Message() != "NotFound" {
		t.Errorf("missing file: %v", err)
	}

	// A failure mid-stream ends the stream with an error after some data.
	s, err = c.ReadBlob(ctx, &pb.BlobRequest{Source: "gh", Repository: "mrbean/teddy", Id: broken})
	if err != nil {
		t.Fatal(err)
	}
	got, err = chunks(t, s)
	if err == nil || len(got) == 0 || status.Code(err) != codes.Unavailable {
		t.Errorf("broken blob: %d chunks, %v", len(got), err)
	}

	// Streams need the token too.
	s, err = c.ReadBlob(context.Background(), &pb.BlobRequest{Source: "gh", Repository: "mrbean/teddy", Id: empty})
	if err == nil {
		_, err = chunks(t, s)
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("stream without token: %v", err)
	}
}

func TestRaw(t *testing.T) {
	e := setup(t, Config{}, code.Limits{})
	for _, tc := range []struct {
		query, body, blob, commit, lfs, length string
	}{
		{"path=README.md&revision=main", "hello world\n", readme2, c2, "false", "12"},
		{"path=bin/run.sh", "#!/bin/sh\necho hi\n", script, c2, "false", "18"},
		{"path=model.bin", string(fx.blobs[lfs]), lfs, c2, "true", fmt.Sprint(len(fx.blobs[lfs]))},
		{"path=big.bin", string(fx.blobs[big]), big, c2, "false", fmt.Sprint(len(fx.blobs[big]))},
		{"path=empty", "", empty, c2, "false", "0"},
		{"blob=" + readme1, "hello\n", readme1, "", "false", "6"},
		{"blob=" + huge, string(fx.blobs[huge]), huge, "", "false", ""},
	} {
		w := do(t, e.h, "GET", "/v1/raw?source=gh&repository=mrbean/teddy&"+tc.query, nil, serviceToken)
		h := w.Header()
		if w.Code != 200 || w.Body.String() != tc.body {
			t.Errorf("%s: %d, %d bytes", tc.query, w.Code, w.Body.Len())
			continue
		}
		for name, want := range map[string]string{
			"Content-Type": "application/octet-stream", "Content-Disposition": "attachment", "Content-Length": tc.length,
			"X-Forge-Code-Blob": tc.blob, "X-Forge-Code-Commit": tc.commit, "X-Forge-Code-LFS-Pointer": tc.lfs,
		} {
			if got := h.Get(name); got != want {
				t.Errorf("%s: %s = %q, want %q", tc.query, name, got, want)
			}
		}
	}

	for _, tc := range []struct {
		method, query, token string
		code                 int
	}{
		{"GET", "path=README.md", "", http.StatusUnauthorized},
		{"GET", "path=README.md", "wrong", http.StatusUnauthorized},
		{"POST", "path=README.md", serviceToken, http.StatusMethodNotAllowed},
		{"GET", "", serviceToken, http.StatusBadRequest},
		{"GET", "path=README.md&blob=" + readme1, serviceToken, http.StatusBadRequest},
		{"GET", "blob=" + readme1 + "&revision=main", serviceToken, http.StatusBadRequest},
		{"GET", "path=README.md&path=link", serviceToken, http.StatusBadRequest},
		{"GET", "path=README.md&extra=1", serviceToken, http.StatusBadRequest},
		{"GET", "path=%zz", serviceToken, http.StatusBadRequest},
		{"GET", "path=../etc/passwd", serviceToken, http.StatusBadRequest},
		{"GET", "path=README.md&revision=abc1234", serviceToken, http.StatusBadRequest},
		{"GET", "path=README.md&revision=dup", serviceToken, http.StatusBadRequest},
		{"GET", "path=bin", serviceToken, http.StatusBadRequest},
		{"GET", "path=nope", serviceToken, http.StatusNotFound},
		{"GET", "blob=" + strings.Repeat("0", 40), serviceToken, http.StatusNotFound},
	} {
		w := do(t, e.h, tc.method, "/v1/raw?source=gh&repository=mrbean/teddy&"+tc.query, nil, tc.token)
		if w.Code != tc.code {
			t.Errorf("%s %s: %d %s, want %d", tc.method, tc.query, w.Code, w.Body, tc.code)
		}
		if w.Code >= 400 && (strings.Contains(w.Body.String(), "README") || w.Header().Get("X-Forge-Code-Blob") != "") {
			t.Errorf("%s: error echoes input or carries blob headers: %s", tc.query, w.Body)
		}
	}
	for _, target := range []string{"/v1/raw?source=gh&repository=denied/x&path=a", "/v1/raw?source=nope&repository=mrbean/teddy&path=a"} {
		if w := do(t, e.h, "GET", target, nil, serviceToken); w.Code != http.StatusForbidden && w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", target, w.Code)
		}
	}

	r := httptest.NewRequest("GET", "/v1/raw?source=gh&repository=mrbean/teddy&path=README.md", nil)
	r.Header.Set("Grpc-Metadata-Authorization", "Bearer "+serviceToken)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("Grpc-Metadata-Authorization accepted on raw: %d", w.Code)
	}
}

func TestRawAbortsMidStream(t *testing.T) {
	e := setup(t, Config{}, code.Limits{})
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/v1/raw?source=gh&repository=mrbean/teddy&blob="+broken, nil)
	req.Header.Set("Authorization", "Bearer "+serviceToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength != 300000 {
		t.Fatalf("status %d, length %d", resp.StatusCode, resp.ContentLength)
	}
	n, err := io.Copy(io.Discard, resp.Body)
	if err == nil || n == 0 || n >= 300000 {
		t.Fatalf("read %d bytes, %v; want a truncated body with an error", n, err)
	}

	// A blob over MaxBlobBytes of unknown size fails after the first
	// byte, so the connection is aborted too.
	small := setup(t, Config{}, code.Limits{MaxBlobBytes: 100 << 10})
	srv2 := httptest.NewServer(small.h)
	defer srv2.Close()
	req, _ = http.NewRequest("GET", srv2.URL+"/v1/raw?source=gh&repository=mrbean/teddy&blob="+huge, nil)
	req.Header.Set("Authorization", "Bearer "+serviceToken)
	resp, err = srv2.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if n, err := io.Copy(io.Discard, resp.Body); resp.StatusCode != 200 || err == nil || n != 100<<10 {
		t.Fatalf("over the cap: %d, read %d bytes, %v", resp.StatusCode, n, err)
	}
	// With a known size the cap applies before the first byte.
	if w := do(t, small.h, "GET", "/v1/raw?source=gh&repository=mrbean/teddy&path=huge.bin", nil, serviceToken); w.Code != http.StatusBadRequest {
		t.Fatalf("known size over the cap: %d", w.Code)
	}
}

func TestRawExtendsWriteDeadline(t *testing.T) {
	e := setup(t, Config{Timeout: time.Second, StreamTimeout: time.Minute}, code.Limits{})
	srv := httptest.NewUnstartedServer(e.h)
	srv.Config.WriteTimeout = 50 * time.Millisecond
	srv.Start()
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/v1/raw?source=gh&repository=mrbean/teddy&path=big.bin", nil)
	req.Header.Set("Authorization", "Bearer "+serviceToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	time.Sleep(100 * time.Millisecond) // past the server's WriteTimeout
	data, err := io.ReadAll(resp.Body)
	if err != nil || len(data) != len(fx.blobs[big]) {
		t.Fatalf("read %d bytes, %v", len(data), err)
	}
}

func TestRESTErrors(t *testing.T) {
	e := setup(t, Config{}, code.Limits{})
	teddy := "source=gh&repository=mrbean/teddy"
	for _, tc := range []struct {
		target string
		code   int
	}{
		{"/v1/resolve?" + teddy + "&revision=abc1234", http.StatusBadRequest},    // short SHA
		{"/v1/resolve?" + teddy + "&revision=main..side", http.StatusBadRequest}, // range
		{"/v1/resolve?" + teddy + "&revision=dup", http.StatusBadRequest},        // ambiguous
		{"/v1/resolve?" + teddy + "&revision=nope", http.StatusNotFound},
		{"/v1/resolve?" + teddy + "&revision=" + strings.Repeat("1", 40), http.StatusNotFound}, // forge 422
		{"/v1/resolve?source=nope&repository=mrbean/teddy", http.StatusBadRequest},
		{"/v1/resolve?repository=mrbean/teddy", http.StatusBadRequest},
		{"/v1/resolve?source=gh&repository=nobody/nothing", http.StatusNotFound},
		{"/v1/resolve?source=gh&repository=teddy", http.StatusBadRequest},
		{"/v1/resolve?source=gh&repository=denied/x", http.StatusForbidden},
		{"/v1/resolve?source=gh&repository=limited/x", http.StatusTooManyRequests},
		{"/v1/resolve?source=gh&repository=broken/x", http.StatusServiceUnavailable},
		{"/v1/resolve?source=gh&repository=expired/x", http.StatusServiceUnavailable},
		{"/v1/refs?" + teddy + "&prefix=refs/notes/", http.StatusBadRequest},
		{"/v1/history?" + teddy + "&page_size=101", http.StatusBadRequest},
		{"/v1/history?" + teddy + "&cursor=junk", http.StatusBadRequest},
		{"/v1/tree?" + teddy + "&path=nope", http.StatusNotFound},
		{"/v1/tree?" + teddy + "&path=README.md", http.StatusNotFound},
		{"/v1/stat?" + teddy + "&path=a//b", http.StatusBadRequest},
		{"/v1/file?" + teddy + "&path=lib", http.StatusBadRequest},
		{"/v1/file?" + teddy + "&path=huge.bin", http.StatusBadRequest},
		{"/v1/blob?" + teddy + "&id=" + huge, http.StatusBadRequest},
		{"/v1/blob?" + teddy + "&id=abc", http.StatusBadRequest},
		{"/v1/merge-base?" + teddy + "&a=main&b=nope", http.StatusNotFound},
		{"/v1/compare?" + teddy + "&base=main&head=side&mode=sideways", http.StatusBadRequest},
		{"/v1/capabilities?source=nope", http.StatusBadRequest},
		{"/v1/unknown", http.StatusNotFound},
	} {
		code, body := rest(t, e.h, "GET", tc.target, "", serviceToken)
		if code != tc.code {
			t.Errorf("%s: %d %s, want %d", tc.target, code, body, tc.code)
		}
		if code >= 400 && (strings.Contains(body, "mrbean") || strings.Contains(body, "upstream") || strings.Contains(body, "Bad credentials")) {
			t.Errorf("%s: error echoes input or forge response: %s", tc.target, body)
		}
	}
}

func TestDeadline(t *testing.T) {
	e := setup(t, Config{Timeout: 100 * time.Millisecond, StreamTimeout: 100 * time.Millisecond}, code.Limits{})
	if code, body := rest(t, e.h, "GET", "/v1/resolve?source=gh&repository=slow/x", "", serviceToken); code != http.StatusGatewayTimeout {
		t.Errorf("unary: %d %s", code, body)
	}
	if w := do(t, e.h, "GET", "/v1/raw?source=gh&repository=slow/x&path=a", nil, serviceToken); w.Code != http.StatusGatewayTimeout {
		t.Errorf("raw: %d %s", w.Code, w.Body)
	}
	s, err := e.rpc.ReadFile(authed(), &pb.PathRequest{Source: "gh", Repository: "slow/x", Path: "a"})
	if err == nil {
		_, err = chunks(t, s)
	}
	if status.Code(err) != codes.DeadlineExceeded {
		t.Errorf("stream: %v", err)
	}
}

func TestCodeOf(t *testing.T) {
	forge := func(code int, sentinel error) error {
		return fmt.Errorf("code: source gh: %w", &forgehttp.Error{StatusCode: code, Err: sentinel})
	}
	wrap := func(err error) error { return fmt.Errorf("code: source gh: %w", err) }
	for _, tc := range []struct {
		err    error
		writes bool
		want   codes.Code
	}{
		{context.Canceled, false, codes.Canceled},
		{wrap(context.DeadlineExceeded), false, codes.DeadlineExceeded},
		{errors.Join(context.DeadlineExceeded, code.ErrInvalid), false, codes.DeadlineExceeded},
		{wrap(code.ErrInvalid), false, codes.InvalidArgument},
		{wrap(code.ErrUnknownSource), false, codes.InvalidArgument},
		{wrap(code.ErrStale), true, codes.Aborted},
		{errors.Join(code.ErrStale, forgehttp.ErrConflict), true, codes.Aborted},
		{wrap(code.ErrAmbiguous), false, codes.FailedPrecondition},
		{wrap(code.ErrNotFile), false, codes.FailedPrecondition},
		{wrap(code.ErrRejected), true, codes.FailedPrecondition},
		{wrap(code.ErrTooLarge), false, codes.OutOfRange},
		{errFileTooLarge, false, codes.OutOfRange},
		{wrap(code.ErrUnsupported), true, codes.Unimplemented},
		{wrap(code.ErrRevisionNotFound), false, codes.NotFound},
		{wrap(code.ErrPathNotFound), false, codes.NotFound},
		{forge(http.StatusNotFound, forgehttp.ErrNotFound), false, codes.NotFound},
		{forge(http.StatusForbidden, forgehttp.ErrForbidden), false, codes.PermissionDenied},
		{forge(http.StatusForbidden, forgehttp.ErrRateLimited), false, codes.ResourceExhausted},
		{forge(http.StatusTooManyRequests, forgehttp.ErrRateLimited), false, codes.ResourceExhausted},
		{forge(http.StatusConflict, forgehttp.ErrConflict), false, codes.FailedPrecondition},
		{forge(http.StatusBadRequest, nil), true, codes.InvalidArgument},
		{forge(http.StatusUnprocessableEntity, nil), true, codes.InvalidArgument},
		{forge(http.StatusBadRequest, nil), false, codes.Unavailable},
		{forge(http.StatusUnauthorized, forgehttp.ErrUnauthorized), false, codes.Unavailable},
		{forge(http.StatusUnauthorized, forgehttp.ErrUnauthorized), true, codes.Unavailable},
		{forge(http.StatusBadGateway, nil), false, codes.Unavailable},
		{errors.New("boom"), false, codes.Unavailable},
	} {
		if got := codeOf(tc.err, tc.writes); got != tc.want {
			t.Errorf("codeOf(%v, %v) = %v, want %v", tc.err, tc.writes, got, tc.want)
		}
	}
	if err := rpcError(wrap(code.ErrNotFile)); status.Convert(err).Message() != "FailedPrecondition" {
		t.Errorf("rpcError message = %q", status.Convert(err).Message())
	}
}

// TestAuthRequired calls every REST route and RPC without the token.
func TestAuthRequired(t *testing.T) {
	e := setup(t, Config{}, code.Limits{})
	before := e.f.count()
	for _, tc := range []struct{ method, target string }{
		{"GET", "/v1/sources"}, {"GET", "/v1/capabilities?source=gh"}, {"GET", "/v1/resolve"}, {"GET", "/v1/refs"},
		{"GET", "/v1/commit"}, {"GET", "/v1/history"}, {"GET", "/v1/tree"}, {"GET", "/v1/stat"}, {"GET", "/v1/file"},
		{"GET", "/v1/blob"}, {"GET", "/v1/merge-base"}, {"GET", "/v1/compare"}, {"GET", "/v1/raw"},
		{"POST", "/v1/commits"}, {"POST", "/v1/ref-updates"},
	} {
		for _, token := range []string{"", "wrong"} {
			w := do(t, e.h, tc.method, tc.target+"?source=gh&repository=mrbean/teddy&path=README.md", strings.NewReader("{}"), token)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s token %q: %d", tc.method, tc.target, token, w.Code)
			}
		}
		r := httptest.NewRequest(tc.method, tc.target, strings.NewReader("{}"))
		r.Header.Set("Grpc-Metadata-Authorization", "Bearer "+serviceToken)
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: Grpc-Metadata-Authorization accepted: %d", tc.method, tc.target, w.Code)
		}
	}

	c := e.rpc
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer wrong")
	unary := map[string]func() error{
		"ListSources":     func() error { _, err := c.ListSources(ctx, &emptypb.Empty{}); return err },
		"GetCapabilities": func() error { _, err := c.GetCapabilities(ctx, &pb.CapabilitiesRequest{}); return err },
		"Resolve":         func() error { _, err := c.Resolve(ctx, &pb.RevisionRequest{}); return err },
		"ListRefs":        func() error { _, err := c.ListRefs(ctx, &pb.ListRefsRequest{}); return err },
		"GetCommit":       func() error { _, err := c.GetCommit(ctx, &pb.RevisionRequest{}); return err },
		"ListHistory":     func() error { _, err := c.ListHistory(ctx, &pb.HistoryRequest{}); return err },
		"GetTree":         func() error { _, err := c.GetTree(ctx, &pb.TreeRequest{}); return err },
		"Stat":            func() error { _, err := c.Stat(ctx, &pb.PathRequest{}); return err },
		"GetFile":         func() error { _, err := c.GetFile(ctx, &pb.PathRequest{}); return err },
		"GetBlob":         func() error { _, err := c.GetBlob(ctx, &pb.BlobRequest{}); return err },
		"GetMergeBase":    func() error { _, err := c.GetMergeBase(ctx, &pb.MergeBaseRequest{}); return err },
		"Compare":         func() error { _, err := c.Compare(ctx, &pb.CompareRequest{}); return err },
		"CreateCommit":    func() error { _, err := c.CreateCommit(ctx, &pb.CreateCommitRequest{}); return err },
		"UpdateRefs":      func() error { _, err := c.UpdateRefs(ctx, &pb.UpdateRefsRequest{}); return err },
		"ReadFile": func() error {
			s, err := c.ReadFile(ctx, &pb.PathRequest{})
			if err == nil {
				_, err = chunks(t, s)
			}
			return err
		},
		"ReadBlob": func() error {
			s, err := c.ReadBlob(ctx, &pb.BlobRequest{})
			if err == nil {
				_, err = chunks(t, s)
			}
			return err
		},
	}
	if len(unary) != len(pb.Code_ServiceDesc.Methods)+len(pb.Code_ServiceDesc.Streams) {
		t.Fatalf("test covers %d RPCs, service has more", len(unary))
	}
	for name, fn := range unary {
		if err := fn(); status.Code(err) != codes.Unauthenticated {
			t.Errorf("%s: %v", name, err)
		}
	}
	if e.f.count() != before {
		t.Errorf("unauthenticated calls reached the forge")
	}
}

func TestBodyCaps(t *testing.T) {
	e := setup(t, Config{}, code.Limits{})
	// pad builds a CreateCommit body with n bytes of base64 content,
	// generated as it is read.
	pad := func(n int64) io.Reader {
		return io.MultiReader(
			strings.NewReader(`{"source":"gh","repository":"mrbean/teddy","ref":"refs/heads/main","old":"`+c2+`","parents":["`+c2+`"],`+
				`"message":"m","author":{"name":"a","email":"a@b"},"changes":[{"path":"f","content":"`),
			io.LimitReader(repeat('A'), n),
			strings.NewReader(`"}]}`))
	}
	// Under the cap the request reaches the library: gh has no Git remote.
	if w := do(t, e.h, "POST", "/v1/commits", pad(2<<20), serviceToken); w.Code != http.StatusNotImplemented {
		t.Errorf("2 MiB commit: %d %s", w.Code, w.Body)
	}
	if w := do(t, e.h, "POST", "/v1/commits", pad(maxCommitBytes), serviceToken); w.Code != http.StatusBadRequest {
		t.Errorf("96 MiB commit: %d %s", w.Code, w.Body)
	}
	updates := `{"source":"gh","repository":"mrbean/teddy","updates":[{"name":"refs/heads/x","old":"` + strings.Repeat("0", 40) + `","new":"` + c2 + `"}]`
	if w := do(t, e.h, "POST", "/v1/ref-updates", strings.NewReader(updates+"}"), serviceToken); w.Code != http.StatusNotImplemented {
		t.Errorf("ref updates: %d %s", w.Code, w.Body)
	}
	big := updates + `,"pad":"` + strings.Repeat(" ", maxBodyBytes) + `"}`
	if w := do(t, e.h, "POST", "/v1/ref-updates", strings.NewReader(big), serviceToken); w.Code != http.StatusBadRequest {
		t.Errorf("1 MiB ref updates: %d %s", w.Code, w.Body)
	}

	// gRPC accepts commits up to MaxRecvMsgSize.
	content := make([]byte, 8<<20)
	_, err := e.rpc.CreateCommit(authed(), &pb.CreateCommitRequest{
		Source: "gh", Repository: "mrbean/teddy", Ref: "refs/heads/main", Old: c2, Parents: []string{c2}, Message: "m",
		Author: &pb.Signature{Name: "a", Email: "a@b"}, Changes: []*pb.FileChange{{Path: "f", Content: content}},
	})
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("8 MiB gRPC commit: %v", err)
	}
}

// repeat is an endless reader of b.
type repeat byte

func (r repeat) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

func TestInFlightLimit(t *testing.T) {
	e := setup(t, Config{MaxInFlight: 1}, code.Limits{})
	done := make(chan int, 1)
	go func() {
		code, _ := rest(t, e.h, "GET", "/v1/resolve?source=gh&repository=slow/x", "", serviceToken)
		done <- code
	}()
	<-e.f.waiting
	for _, target := range []string{"/v1/resolve?source=gh&repository=mrbean/teddy", "/v1/raw?source=gh&repository=mrbean/teddy&path=README.md"} {
		if w := do(t, e.h, "GET", target, nil, serviceToken); w.Code != http.StatusTooManyRequests {
			t.Errorf("%s over the limit: %d", target, w.Code)
		}
	}
	s, err := e.rpc.ReadBlob(authed(), &pb.BlobRequest{Source: "gh", Repository: "mrbean/teddy", Id: readme1})
	if err == nil {
		_, err = chunks(t, s)
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Errorf("stream over the limit: %v", err)
	}
	// Other sources and source-less calls are not limited.
	if code, body := rest(t, e.h, "GET", "/v1/resolve?source=gh2&repository=mrbean/teddy", "", serviceToken); code != 200 {
		t.Errorf("other source: %d %s", code, body)
	}
	if code, _ := rest(t, e.h, "GET", "/v1/sources", "", serviceToken); code != 200 {
		t.Errorf("sources: %d", code)
	}
	close(e.f.release)
	if code := <-done; code != http.StatusNotFound { // slow/x has no branches
		t.Errorf("slow request: %d", code)
	}
	if code, body := rest(t, e.h, "GET", "/v1/resolve?source=gh&repository=mrbean/teddy", "", serviceToken); code != 200 {
		t.Errorf("after release: %d %s", code, body)
	}
	if len(e.s.inFlight.n) != 0 {
		t.Errorf("limiter kept entries: %v", e.s.inFlight.n)
	}
}

func TestWrites(t *testing.T) {
	e := setup(t, Config{}, code.Limits{})
	zero := strings.Repeat("0", 40)
	for _, tc := range []struct {
		path, body string
		code       int
	}{
		// gh has no Git remote, so valid writes are unsupported there.
		{"/v1/commits", `{"source":"gh","repository":"mrbean/teddy","ref":"refs/heads/new","old":"` + zero + `","message":"m","author":{"name":"a","email":"a@b"},"changes":[{"path":"f","content":"aGk="}]}`, http.StatusNotImplemented},
		{"/v1/commits", `{"source":"gh","repository":"mrbean/teddy","ref":"refs/heads/new","old":"` + zero + `","message":"m","author":{"name":"a","email":"a@b"},"changes":[{"path":"f","mode":"777"}]}`, http.StatusBadRequest},
		{"/v1/commits", `{"source":"gh","repository":"mrbean/teddy","ref":"refs/heads/new","old":"` + zero + `","message":"m","author":{"name":"a","email":"a@b"},"changes":[{"path":".git/config"}]}`, http.StatusBadRequest},
		{"/v1/commits", `{"source":"gh","repository":"mrbean/teddy","ref":"refs/heads/new","old":"` + zero + `","message":"","author":{"name":"a","email":"a@b"}}`, http.StatusBadRequest},
		{"/v1/commits", `{"source":"gh","repository":"mrbean/teddy","ref":"main","old":"` + zero + `","message":"m","author":{"name":"a","email":"a@b"}}`, http.StatusBadRequest},
		{"/v1/commits", `{"source":"gh","repository":"mrbean/teddy","ref":"refs/heads/new","old":"","message":"m","author":{"name":"a","email":"a@b"}}`, http.StatusBadRequest},
		{"/v1/commits", `{"source":"gh","repository":"mrbean/teddy","ref":"refs/heads/new","old":"` + zero + `","message":"m","author":{"name":"a","email":"a@b","when":"10000-01-01T00:00:00Z"}}`, http.StatusBadRequest},
		{"/v1/commits", `{"source":"gh","repository":"mrbean/teddy","unknown":1}`, http.StatusBadRequest},
		{"/v1/ref-updates", `{"source":"gh","repository":"mrbean/teddy","updates":[{"name":"refs/heads/x","old":"` + zero + `","new":"` + c2 + `"}]}`, http.StatusNotImplemented},
		{"/v1/ref-updates", `{"source":"gh","repository":"mrbean/teddy","updates":[]}`, http.StatusBadRequest},
		{"/v1/ref-updates", `{"source":"gh","repository":"mrbean/teddy","updates":[{"name":"refs/heads/x","old":"abc","new":"` + c2 + `"}]}`, http.StatusBadRequest},
		{"/v1/ref-updates", `{"source":"nope","repository":"mrbean/teddy","updates":[{"name":"refs/heads/x","old":"` + zero + `","new":"` + c2 + `"}]}`, http.StatusBadRequest},
	} {
		if code, body := rest(t, e.h, "POST", tc.path, tc.body, serviceToken); code != tc.code {
			t.Errorf("%s %s: %d %s, want %d", tc.path, tc.body, code, body, tc.code)
		}
	}
	_, err := e.rpc.UpdateRefs(authed(), &pb.UpdateRefsRequest{Source: "gh", Repository: "mrbean/teddy",
		Updates: []*pb.RefUpdate{{Name: "refs/heads/x", Old: zero, New: c2}}})
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("gRPC update refs: %v", err)
	}
	if e.f.count() != 0 {
		t.Errorf("writes reached the forge API %d times", e.f.count())
	}
}

func TestWriteTranslation(t *testing.T) {
	when := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	n, err := newCommit(&pb.CreateCommitRequest{
		Parents: []string{c1, c2},
		Changes: []*pb.FileChange{
			{Path: "a", Content: []byte("hi"), Mode: "100755"},
			{Path: "b", Delete: true, Content: []byte{}},
			{Path: "c", Mode: "160000", Submodule: c3, Content: []byte{}},
			{Path: "d"},
		},
		Author:  &pb.Signature{Name: "A", Email: "a@x", When: timestamppb.New(when)},
		Message: "msg",
		Ref:     "refs/heads/main",
		Old:     c2,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(n)
	want := fmt.Sprintf(`{"Parents":["%s","%s"],"Changes":[{"Path":"a","Delete":false,"Mode":33261,"Content":"aGk=","Submodule":""},`+
		`{"Path":"b","Delete":true,"Mode":0,"Content":null,"Submodule":""},{"Path":"c","Delete":false,"Mode":57344,"Content":null,"Submodule":"%s"},`+
		`{"Path":"d","Delete":false,"Mode":0,"Content":null,"Submodule":""}],`+
		`"Author":{"Name":"A","Email":"a@x","When":"2024-05-06T07:08:09Z"},"Committer":{"Name":"","Email":"","When":"0001-01-01T00:00:00Z"},`+
		`"Message":"msg","Ref":"refs/heads/main","Old":"%s"}`, c1, c2, c3, c2)
	if string(got) != want {
		t.Errorf("newCommit:\n got %s\nwant %s", got, want)
	}
	if _, err := newCommit(&pb.CreateCommitRequest{Changes: []*pb.FileChange{{Path: "a", Mode: "644"}}}); !errors.Is(err, code.ErrInvalid) {
		t.Errorf("bad mode: %v", err)
	}
	if _, err := newCommit(&pb.CreateCommitRequest{Committer: &pb.Signature{When: &timestamppb.Timestamp{Nanos: -1}}}); !errors.Is(err, code.ErrInvalid) {
		t.Errorf("bad time: %v", err)
	}
	u := refUpdates(&pb.UpdateRefsRequest{Updates: []*pb.RefUpdate{{Name: "refs/tags/v2", Old: c1, New: c2}}})
	if len(u) != 1 || u[0] != (code.RefUpdate{Name: "refs/tags/v2", Old: code.OID(c1), New: code.OID(c2)}) {
		t.Errorf("refUpdates = %+v", u)
	}
	o := compareOptions(&pb.CompareRequest{Mode: "direct", Paths: []string{"a"}, Patches: true, Context: -1, MaxChanges: 5, MaxPatchFiles: 6, MaxPatchBytes: 7})
	if got, _ := json.Marshal(o); string(got) != `{"Mode":"direct","Paths":["a"],"Patches":true,"Context":-1,"MaxChanges":5,"MaxPatchFiles":6,"MaxPatchBytes":7}` {
		t.Errorf("compareOptions = %s", got)
	}
}

func TestCompare(t *testing.T) {
	e := setup(t, Config{}, code.Limits{})
	d := get(t, e.h, "compare", "base=side&head=main&paths=README.md&patches=true")
	got, _ := json.Marshal(d)
	want := fmt.Sprintf(`{"base":"%s","changes":[{"additions":1,"deletions":1,"id":"%s","mode":"100644","oldId":"%s","oldMode":"100644",`+
		`"oldPath":"README.md","patch":"@@ -1 +1 @@\n-hello\n+hello world\n","path":"README.md","status":"modified"}],"head":"%s","mergeBase":"%s"}`,
		c3, readme2, readme1, c2, c1)
	if string(got) != want {
		t.Errorf("compare:\n got %s\nwant %s", got, want)
	}

	diff, err := e.rpc.Compare(authed(), &pb.CompareRequest{Source: "gh", Repository: "mrbean/teddy", Base: c1, Head: "main", Mode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, c := range diff.GetChanges() {
		paths = append(paths, c.GetStatus()+":"+c.GetPath())
	}
	if want := "modified:README.md added:big.bin added:bin/run.sh added:empty added:four.bin added:huge.bin added:lib added:link added:model.bin"; strings.Join(paths, " ") != want {
		t.Errorf("direct compare = %q", paths)
	}
	if diff.GetMergeBase() != "" || diff.GetBase() != c1 || diff.GetHead() != c2 {
		t.Errorf("direct compare = %v", diff)
	}
	diff, err = e.rpc.Compare(authed(), &pb.CompareRequest{Source: "gh", Repository: "mrbean/teddy", Base: c1, Head: "main", Mode: "direct", MaxChanges: 2})
	if err != nil || !diff.GetTruncated() || len(diff.GetChanges()) != 2 {
		t.Errorf("truncated compare: %v %v", diff, err)
	}
}

// pause is a reader that waits, then reports EOF, to delay a MultiReader.
type pause time.Duration

func (p pause) Read([]byte) (int, error) {
	time.Sleep(time.Duration(p))
	return 0, io.EOF
}

// counter counts the bytes read from r.
type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// TestCommitExtendsDeadlines sends a commit whose upload and push each take
// most of the timeout. The response must still arrive.
func TestCommitExtendsDeadlines(t *testing.T) {
	const timeout = time.Second
	e := setup(t, Config{Timeout: timeout}, code.Limits{})
	sources := e.s.sources
	e.s.sources = func(ctx context.Context) (*code.Client, error) {
		time.Sleep(timeout * 3 / 4) // a slow push
		return sources(ctx)
	}
	srv := httptest.NewUnstartedServer(e.h)
	srv.Config.ReadTimeout = timeout / 4
	srv.Config.WriteTimeout = timeout + 100*time.Millisecond
	srv.Start()
	defer srv.Close()
	body := `{"source":"gh","repository":"mrbean/teddy","ref":"refs/heads/main","old":"` + c2 + `","parents":["` + c2 + `"],` +
		`"message":"m","author":{"name":"a","email":"a@b"},"changes":[{"path":"f","content":"` + b64("x") + `"}]}`
	req, _ := http.NewRequest("POST", srv.URL+"/v1/commits", io.MultiReader(pause(timeout*3/4), strings.NewReader(body)))
	req.ContentLength = int64(len(body))
	req.Header.Set("Authorization", "Bearer "+serviceToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented { // gh has no Git remote
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// TestCommitTokenBeforeBody checks that an unauthenticated commit upload is
// rejected before its body is read.
func TestCommitTokenBeforeBody(t *testing.T) {
	e := setup(t, Config{}, code.Limits{})
	for _, token := range []string{"", "wrong"} {
		body := &counter{r: io.MultiReader(strings.NewReader(`{"message":"`), io.LimitReader(repeat('A'), 4*maxBodyBytes), strings.NewReader(`"}`))}
		w := do(t, e.h, "POST", "/v1/commits", body, token)
		if w.Code != http.StatusUnauthorized || body.n > maxBodyBytes {
			t.Errorf("token %q: %d after reading %d bytes", token, w.Code, body.n)
		}
	}
}

// TestGRPCTokenFirst checks that GRPCOptions rejects callers before their
// request message reaches the handler.
func TestGRPCTokenFirst(t *testing.T) {
	e := setup(t, Config{}, code.Limits{})
	var reached atomic.Bool
	srv := grpc.NewServer(append(e.s.GRPCOptions(),
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			reached.Store(true)
			return h(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			reached.Store(true)
			return h(srv, ss)
		}))...)
	pb.RegisterCodeServer(srv, e.s)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(lis)
	defer srv.Stop()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(MaxRecvMsgSize)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := pb.NewCodeClient(conn)
	for _, md := range []metadata.MD{nil, metadata.Pairs("authorization", "Bearer wrong")} {
		ctx := metadata.NewOutgoingContext(context.Background(), md)
		_, err := c.CreateCommit(ctx, &pb.CreateCommitRequest{Source: "gh", Changes: []*pb.FileChange{{Path: "f", Content: make([]byte, 8<<20)}}})
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("CreateCommit %v: %v", md, err)
		}
		s, err := c.ReadFile(ctx, &pb.PathRequest{Source: "gh"})
		if err == nil {
			_, err = chunks(t, s)
		}
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("ReadFile %v: %v", md, err)
		}
	}
	if reached.Load() {
		t.Error("unauthenticated calls reached the handler")
	}
	if _, err := c.GetFile(authed(), &pb.PathRequest{Source: "gh", Repository: "mrbean/teddy", Path: "README.md"}); err != nil || !reached.Load() {
		t.Errorf("with the token: %v, reached %v", err, reached.Load())
	}
}

// TestStreamTimeoutStalledClient checks that a client that stops reading
// loses its stream, and its in-flight slot, at the stream timeout.
func TestStreamTimeoutStalledClient(t *testing.T) {
	e := setup(t, Config{StreamTimeout: 300 * time.Millisecond, MaxInFlight: 1}, code.Limits{})
	ctx, cancel := context.WithCancel(authed())
	defer cancel()
	s, err := e.rpc.ReadFile(ctx, &pb.PathRequest{Source: "gh", Repository: "mrbean/teddy", Path: "huge.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond) // the client stops reading
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		_, err = e.rpc.GetFile(authed(), &pb.PathRequest{Source: "gh", Repository: "mrbean/teddy", Path: "README.md"})
		if err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		t.Fatalf("slot still held: %v", err)
	}
	if _, err := chunks(t, s); status.Code(err) != codes.DeadlineExceeded {
		t.Errorf("stalled stream: %v", err)
	}
}
