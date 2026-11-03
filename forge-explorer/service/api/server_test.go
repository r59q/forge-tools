package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-explorer/library/explorer"
	pb "github.com/r59q/forge-tools/forge-explorer/service/explorerpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type harness struct {
	server    *Server
	client    pb.ExplorerServiceClient
	http      *httptest.Server
	root      *explorer.Worktree
	directory string
}

func setup(t *testing.T) harness {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package fixture\n// needle"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := explorer.Open("local", "Local", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	s, err := New([]*explorer.Worktree{root}, forgeauth.StaticToken("test-only"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := s.REST(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(handler)
	t.Cleanup(h.Close)
	listener := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	pb.RegisterExplorerServiceServer(g, s)
	go func() { _ = g.Serve(listener) }()
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient("passthrough:///local", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return harness{s, pb.NewExplorerServiceClient(conn), h, root, dir}
}

func authenticated(t *testing.T) context.Context {
	t.Helper()
	return metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer test-only")
}

func (h harness) request(t *testing.T, method, route, body, token string) (int, []byte) {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), method, h.http.URL+route, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := h.http.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("missing cache protection")
	}
	return resp.StatusCode, data
}

func parity(t *testing.T, h harness, method, route, body string, rpc proto.Message, target proto.Message) {
	t.Helper()
	code, data := h.request(t, method, route, body, "test-only")
	if code != 200 {
		t.Fatalf("REST %d: %s", code, data)
	}
	if err := protojson.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(rpc, target) {
		t.Fatalf("REST/RPC mismatch: %v / %v", target, rpc)
	}
}

func TestTransportParity(t *testing.T) {
	h := setup(t)
	ctx := authenticated(t)
	sources, err := h.client.ListSources(ctx, &pb.ListSourcesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	parity(t, h, "GET", "/v1/explorer/sources", "", sources, &pb.ListSourcesResponse{})
	version := h.root.Source().Version
	query := "?sourceVersion=" + version + "&path="
	dir, err := h.client.Browse(ctx, &pb.BrowseRequest{Source: "local", SourceVersion: version, Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	parity(t, h, "GET", "/v1/explorer/sources/local/tree"+query+".", "", dir, &pb.BrowseResponse{})
	file, err := h.client.Read(ctx, &pb.ReadRequest{Source: "local", SourceVersion: version, Path: "hello.go"})
	if err != nil {
		t.Fatal(err)
	}
	parity(t, h, "GET", "/v1/explorer/sources/local/file"+query+"hello.go", "", file, &pb.ReadResponse{})
	matches, err := h.client.Search(ctx, &pb.SearchRequest{Source: "local", SourceVersion: version, Text: "needle", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"sourceVersion":"` + version + `","text":"needle","limit":1}`
	parity(t, h, "POST", "/v1/explorer/sources/local/search", body, matches, &pb.SearchResponse{})
}

func TestAuthenticationAndErrors(t *testing.T) {
	h := setup(t)
	for _, token := range []string{"", "wrong"} {
		ctx := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer "+token)
		if _, err := h.client.ListSources(ctx, &pb.ListSourcesRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Fatal(err)
		}
		code, _ := h.request(t, "GET", "/v1/explorer/sources", "", token)
		if code != 401 {
			t.Fatal(code)
		}
	}
	version := h.root.Source().Version
	for _, test := range []struct {
		path, version, expected string
		rpc                     codes.Code
		http                    int
	}{
		{"missing", version, "", codes.NotFound, 404},
		{"../escape", version, "", codes.InvalidArgument, 400},
		{"hello.go", "old", "", codes.FailedPrecondition, 400},
		{"hello.go", version, "old-content", codes.FailedPrecondition, 400},
		{"hello.go", "", "", codes.InvalidArgument, 400},
	} {
		_, err := h.client.Read(authenticated(t), &pb.ReadRequest{Source: "local", SourceVersion: test.version, Path: test.path, ExpectedVersion: test.expected})
		if status.Code(err) != test.rpc {
			t.Fatal(test, err)
		}
		q := url.Values{"sourceVersion": {test.version}, "path": {test.path}, "expectedVersion": {test.expected}}
		code, data := h.request(t, "GET", "/v1/explorer/sources/local/file?"+q.Encode(), "", "test-only")
		if code != test.http || strings.Contains(string(data), h.directory) {
			t.Fatal(test, code, string(data))
		}
	}
}

func TestLimitsUnsupportedAndStrictJSON(t *testing.T) {
	h := setup(t)
	version := h.root.Source().Version
	for name, content := range map[string]string{"binary": "\x00", "large": strings.Repeat("x", 1<<20+1)} {
		if err := os.WriteFile(filepath.Join(h.directory, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]codes.Code{"binary": codes.Unimplemented, "large": codes.ResourceExhausted} {
		_, err := h.client.Read(authenticated(t), &pb.ReadRequest{Source: "local", SourceVersion: version, Path: name})
		if status.Code(err) != want {
			t.Fatal(name, err)
		}
	}
	result, err := h.client.Search(authenticated(t), &pb.SearchRequest{Source: "local", SourceVersion: version, Text: "needle"})
	if err != nil || result.Results.State.Skipped != 2 {
		t.Fatal(result, err)
	}
	for _, body := range []string{`{"unknown":true}`, `{"text":"x","limit":201}`, strings.Repeat("x", 1<<20+1)} {
		code, _ := h.request(t, "POST", "/v1/explorer/sources/local/search", body, "test-only")
		if code == 200 {
			t.Fatal("accepted invalid body")
		}
	}
	ctx, cancel := context.WithCancel(authenticated(t))
	cancel()
	if _, err := h.client.ListSources(ctx, &pb.ListSourcesRequest{}); status.Code(err) != codes.Canceled {
		t.Fatal(err)
	}
	data, err := json.Marshal(h.root.Source())
	if err != nil || strings.Contains(string(data), h.directory) {
		t.Fatal(string(data), err)
	}
}
