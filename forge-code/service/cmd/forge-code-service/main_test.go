package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/r59q/forge-tools/forge-code/service/codepb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const commitID = "1111111111111111111111111111111111111111"

func TestSettings(t *testing.T) {
	values := map[string]string{
		"FORGE_CODE_CONNECTOR_URL": "http://env", "FORGE_CODE_GRPC_LISTEN": "127.0.0.1:9001", "FORGE_CODE_HTTP_LISTEN": "127.0.0.1:9002",
		"FORGE_CODE_REQUEST_TIMEOUT": "2s", "FORGE_CODE_STREAM_TIMEOUT": "1m", "FORGE_CODE_MAX_INFLIGHT_PER_SOURCE": "3",
	}
	getenv := func(name string) string { return values[name] }
	s, err := parse([]string{"--connector-url=http://flag", "--request-timeout=3s", "--max-inflight-per-source=4"}, getenv, io.Discard)
	if err != nil || s.connectorURL != "http://flag" || s.timeout != 3*time.Second || s.streamTimeout != time.Minute || s.maxInFlight != 4 ||
		s.grpcAddress != "127.0.0.1:9001" || s.httpAddress != "127.0.0.1:9002" ||
		s.tokenEnv != "FORGE_CODE_TOKEN" || s.connectorTokenEnv != "FORGE_CODE_CONNECTOR_TOKEN" {
		t.Fatalf("settings: %+v %v", s, err)
	}
	for _, args := range [][]string{
		{"--token-file=t", "--token-env=T"}, {"--connector-token-file=t", "--connector-token-env=T"}, {"--request-timeout=0"},
		{"--stream-timeout=-1s"}, {"--stream-timeout=x-secret-sentinel"}, {"--max-inflight-per-source=0"},
		{"--max-inflight-per-source=many-secret-sentinel"}, {"--connector-url="}, {"extra"}, {"--unexpected=secret-sentinel"},
	} {
		if _, err := parse(args, getenv, io.Discard); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
			t.Fatalf("parse(%q) accepted or echoed input: %v", args, err)
		}
	}
	values["FORGE_CODE_REQUEST_TIMEOUT"] = "invalid-secret-sentinel"
	if _, err := parse(nil, getenv, io.Discard); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("invalid environment timeout accepted or echoed")
	}
	var help bytes.Buffer
	if _, err := parse([]string{"--help"}, getenv, &help); !errors.Is(err, flag.ErrHelp) || !strings.Contains(help.String(), "-stream-timeout") {
		t.Fatal("missing flag help")
	}
}

func TestDefaults(t *testing.T) {
	s, err := parse([]string{"--connector-url=http://p", "--token-file=t", "--connector-token-env=C"}, func(string) string { return "" }, io.Discard)
	if err != nil || s.grpcAddress != "127.0.0.1:9095" || s.httpAddress != "127.0.0.1:8085" || s.timeout != 30*time.Second ||
		s.streamTimeout != 10*time.Minute || s.maxInFlight != 8 || s.tokenFile != "t" || s.tokenEnv != "" || s.connectorTokenEnv != "C" {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	if _, err := parse(nil, func(string) string { return "" }, io.Discard); err == nil || !strings.Contains(err.Error(), "FORGE_CODE_CONNECTOR_URL") {
		t.Fatalf("missing connector URL: %v", err)
	}
}

// fakeProxy serves GET /connections and one GitHub connection, conn, like a
// forge-connector proxy. It accepts only the client token "client".
func fakeProxy(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer client" && r.Header.Get("Private-Token") != "client" {
			http.Error(w, "proxy: Unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/connections":
			fmt.Fprint(w, `{"connections":[
				{"id":"conn","name":"me","kind":"github","status":"ok"},
				{"id":"old","kind":"gitlab","status":"revoked"},
				{"id":"svn","kind":"svn","status":"ok"},
				{"id":"work","kind":"gitlab","status":"ok"}]}`)
		case "/c/conn/repos/mrbean/teddy/git/ref/heads/main":
			fmt.Fprintf(w, `{"ref":"refs/heads/main","object":{"type":"commit","sha":"%s"}}`, commitID)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRun(t *testing.T) {
	p := fakeProxy(t)
	t.Setenv("TEST_CONNECTOR_TOKEN", "client")
	tokenFile := writeFile(t, "token", "service\n")
	s, err := parse([]string{"--connector-url=" + p.URL, "--connector-token-env=TEST_CONNECTOR_TOKEN", "--token-file=" + tokenFile, "--grpc-listen=127.0.0.1:0", "--http-listen=127.0.0.1:0"}, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(chan [2]net.Addr, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, s, func(g, h net.Addr) { addrs <- [2]net.Addr{g, h} }) }()
	var a [2]net.Addr
	select {
	case a = <-addrs:
	case err := <-done:
		t.Fatalf("run: %v", err)
	}

	rest := func(target, token string) (int, string) {
		req, _ := http.NewRequest("GET", "http://"+a[1].String()+target, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, body := rest("/v1/resolve?source=conn&repository=mrbean/teddy&revision=main", "service"); code != 200 || !strings.Contains(body, commitID) {
		t.Fatalf("REST: %d %s", code, body)
	}
	if code, _ := rest("/v1/sources", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("REST wrong token: %d", code)
	}
	// The token file is reread on each request.
	if err := os.WriteFile(tokenFile, []byte("rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _ := rest("/v1/sources", "service"); code != http.StatusUnauthorized {
		t.Fatalf("old token after rotation: %d", code)
	}
	if code, _ := rest("/v1/sources", "rotated"); code != 200 {
		t.Fatalf("rotated token: %d", code)
	}

	conn, err := grpc.NewClient(a[0].String(), grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(64<<20)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rpcCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer rotated")
	c := pb.NewCodeClient(conn)
	sources, err := c.ListSources(rpcCtx, &emptypb.Empty{})
	if err != nil || len(sources.GetSources()) != 2 || sources.GetSources()[0].GetId() != "conn" {
		t.Fatalf("gRPC: %v %v", sources, err)
	}
	caps, err := c.GetCapabilities(rpcCtx, &pb.CapabilitiesRequest{Source: "conn"})
	if err != nil || !caps.GetWrites() {
		t.Fatalf("connections must carry their Git remote: %v %v", caps, err)
	}
	// The gRPC server accepts commit requests above the 4 MiB default; this
	// one is rejected by the library for its invalid ref, not for its size.
	_, err = c.CreateCommit(rpcCtx, &pb.CreateCommitRequest{Source: "conn", Repository: "mrbean/teddy", Ref: "main",
		Changes: []*pb.FileChange{{Path: "f", Content: make([]byte, 16<<20)}}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("16 MiB commit: %v", err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestRunRequiresTokens(t *testing.T) {
	t.Setenv("TEST_CONNECTOR_TOKEN", "client")
	base := settings{connectorURL: "http://p", grpcAddress: "127.0.0.1:0", httpAddress: "127.0.0.1:0", timeout: time.Second, streamTimeout: time.Second, maxInFlight: 1}
	for _, token := range []string{"", "two words"} {
		s := base
		s.tokenFile, s.connectorTokenEnv = writeFile(t, "token", token), "TEST_CONNECTOR_TOKEN"
		if err := run(context.Background(), s, nil); err == nil || !strings.Contains(err.Error(), "service token") {
			t.Errorf("service token %q: %v", token, err)
		}
		s = base
		s.tokenFile, s.connectorTokenFile = writeFile(t, "token", "service"), writeFile(t, "connector", token)
		if err := run(context.Background(), s, nil); err == nil || !strings.Contains(err.Error(), "connector token") {
			t.Errorf("connector token %q: %v", token, err)
		}
	}
}

func TestRunStartupFailures(t *testing.T) {
	t.Setenv("TEST_TOKEN", "service")
	base := settings{connectorURL: "http://p", tokenEnv: "TEST_TOKEN", connectorTokenEnv: "TEST_TOKEN", grpcAddress: "127.0.0.1:0", httpAddress: "127.0.0.1:0", timeout: time.Second, streamTimeout: time.Second, maxInFlight: 1}
	s := base
	s.connectorURL = "ftp://p"
	if err := run(context.Background(), s, nil); err == nil || !strings.Contains(err.Error(), "connector URL") {
		t.Errorf("ftp connector URL: %v", err)
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	s = base
	s.grpcAddress = busy.Addr().String()
	if err := run(context.Background(), s, nil); err == nil || !strings.Contains(err.Error(), "gRPC") {
		t.Errorf("busy gRPC address: %v", err)
	}
	s = base
	s.httpAddress = busy.Addr().String()
	if err := run(context.Background(), s, nil); err == nil || !strings.Contains(err.Error(), "REST") {
		t.Errorf("busy REST address: %v", err)
	}
}
