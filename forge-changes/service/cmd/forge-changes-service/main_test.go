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

	pb "github.com/r59q/forge-tools/forge-changes/service/changespb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestSettings(t *testing.T) {
	values := map[string]string{"FORGE_CHANGES_CONNECTOR_URL": "http://env", "FORGE_CHANGES_GRPC_LISTEN": "127.0.0.1:9001", "FORGE_CHANGES_HTTP_LISTEN": "127.0.0.1:9002", "FORGE_CHANGES_REQUEST_TIMEOUT": "2s"}
	getenv := func(name string) string { return values[name] }
	s, err := parse([]string{"--connector-url=http://flag", "--request-timeout=3s"}, getenv, io.Discard)
	if err != nil || s.connectorURL != "http://flag" || s.timeout != 3*time.Second || s.grpcAddress != "127.0.0.1:9001" || s.httpAddress != "127.0.0.1:9002" ||
		s.tokenEnv != "FORGE_CHANGES_TOKEN" || s.connectorTokenEnv != "FORGE_CHANGES_CONNECTOR_TOKEN" {
		t.Fatalf("settings: %+v %v", s, err)
	}
	for _, args := range [][]string{{"--token-file=t", "--token-env=T"}, {"--connector-token-file=t", "--connector-token-env=T"}, {"--request-timeout=0"}, {"--connector-url="}, {"extra"}, {"--unexpected=secret-sentinel"}} {
		if _, err := parse(args, getenv, io.Discard); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
			t.Fatalf("parse(%q) accepted or echoed input: %v", args, err)
		}
	}
	values["FORGE_CHANGES_REQUEST_TIMEOUT"] = "invalid-secret-sentinel"
	if _, err := parse(nil, getenv, io.Discard); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("invalid environment timeout accepted or echoed")
	}
	var help bytes.Buffer
	if _, err := parse([]string{"--help"}, getenv, &help); !errors.Is(err, flag.ErrHelp) || !strings.Contains(help.String(), "-connector-url") {
		t.Fatal("missing flag help")
	}
}

func TestDefaults(t *testing.T) {
	s, err := parse([]string{"--connector-url=http://p", "--token-file=t", "--connector-token-env=C"}, func(string) string { return "" }, io.Discard)
	if err != nil || s.grpcAddress != "127.0.0.1:9096" || s.httpAddress != "127.0.0.1:8086" || s.timeout != 30*time.Second ||
		s.tokenFile != "t" || s.tokenEnv != "" || s.connectorTokenEnv != "C" {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	if _, err := parse(nil, func(string) string { return "" }, io.Discard); err == nil || !strings.Contains(err.Error(), "FORGE_CHANGES_CONNECTOR_URL") {
		t.Fatalf("missing connector URL: %v", err)
	}
}

// fakeProxy serves GET /connections and one GitHub connection, conn, like a
// forge-connector proxy. It accepts only the client token "client".
func fakeProxy(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GitLab sessions send PRIVATE-TOKEN, which the proxy accepts for GitLab.
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
		case "/c/conn/repos/mrbean/teddy/pulls":
			fmt.Fprint(w, `[{"id":1,"number":3,"title":"Bear is lost","state":"open","html_url":"https://github.com/mrbean/teddy/pull/3","repository_url":"https://api.github.com/repos/mrbean/teddy"}]`)
		case "/c/work/projects/mrbean/teddy/merge_requests":
			fmt.Fprint(w, `[]`)
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

	req, _ := http.NewRequest("GET", "http://"+a[1].String()+"/v1/changes?repository=mrbean/teddy", nil)
	req.Header.Set("Authorization", "Bearer service")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"repository":"mrbean/teddy"`) {
		t.Fatalf("REST: %d %s", resp.StatusCode, body)
	}

	conn, err := grpc.NewClient(a[0].String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rpcCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer service")
	sources, err := pb.NewChangesClient(conn).ListSources(rpcCtx, &emptypb.Empty{})
	if err != nil || len(sources.GetSources()) != 2 || sources.GetSources()[0].GetId() != "conn" {
		t.Fatalf("gRPC: %v %v", sources, err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestRunRequiresTokens(t *testing.T) {
	t.Setenv("TEST_CONNECTOR_TOKEN", "client")
	for _, token := range []string{"", "two words"} {
		s := settings{connectorURL: "http://p", tokenFile: writeFile(t, "token", token), connectorTokenEnv: "TEST_CONNECTOR_TOKEN", grpcAddress: "127.0.0.1:0", httpAddress: "127.0.0.1:0", timeout: time.Second}
		if err := run(context.Background(), s, nil); err == nil || !strings.Contains(err.Error(), "service token") {
			t.Errorf("service token %q: %v", token, err)
		}
		s = settings{connectorURL: "http://p", tokenFile: writeFile(t, "token", "service"), connectorTokenFile: writeFile(t, "connector", token), grpcAddress: "127.0.0.1:0", httpAddress: "127.0.0.1:0", timeout: time.Second}
		if err := run(context.Background(), s, nil); err == nil || !strings.Contains(err.Error(), "connector token") {
			t.Errorf("connector token %q: %v", token, err)
		}
	}
}

func TestRunRejectsBadConnectorURL(t *testing.T) {
	t.Setenv("TEST_TOKEN", "service")
	s := settings{connectorURL: "ftp://p", tokenEnv: "TEST_TOKEN", connectorTokenEnv: "TEST_TOKEN", grpcAddress: "127.0.0.1:0", httpAddress: "127.0.0.1:0", timeout: time.Second}
	if err := run(context.Background(), s, nil); err == nil || !strings.Contains(err.Error(), "connector URL") {
		t.Fatalf("ftp connector URL: %v", err)
	}
}
