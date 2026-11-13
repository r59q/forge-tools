package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/r59q/forge-tools/forge-symbols/service/symbolspb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func TestSettings(t *testing.T) {
	s, err := parse(nil, func(string) string { return "" }, io.Discard)
	if err != nil || s.GRPCAddress != "127.0.0.1:9104" || s.HTTPAddress != "127.0.0.1:8094" ||
		s.Timeout != 30*time.Second || s.TokenEnv != "FORGE_SYMBOLS_TOKEN" {
		t.Fatalf("defaults: %+v %v", s, err)
	}

	env := map[string]string{"FORGE_SYMBOLS_HTTP_LISTEN": "127.0.0.1:1", "FORGE_SYMBOLS_GRPC_LISTEN": "127.0.0.1:2"}
	s, err = parse([]string{"--request-timeout=2s", "--grpc-listen=127.0.0.1:3"}, func(k string) string { return env[k] }, io.Discard)
	if err != nil || s.HTTPAddress != "127.0.0.1:1" || s.GRPCAddress != "127.0.0.1:3" || s.Timeout != 2*time.Second {
		t.Fatalf("overrides: %+v %v", s, err)
	}

	if _, err := parse([]string{"--unknown=secret-sentinel"}, func(string) string { return "" }, io.Discard); err == nil ||
		strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatalf("bad flag: %v", err)
	}

	if _, err := parse([]string{"--help"}, func(string) string { return "" }, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
}

func TestRun(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("service\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := parse(
		[]string{"--token-file=" + tokenFile, "--grpc-listen=127.0.0.1:0", "--http-listen=127.0.0.1:0"},
		func(string) string { return "" },
		io.Discard,
	)
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

	// The content is "package f\n\nfunc F() {}\n" in base64.
	body := `{"files":[{"path":"f.go","content":"cGFja2FnZSBmCgpmdW5jIEYoKSB7fQo="}],"target":{"name":"F"}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+a[1].String()+"/v1/symbols/definitions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer service")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(got), `"name":"F"`) || !strings.Contains(string(got), `"path":"f.go"`) {
		t.Fatalf("REST: %d %s", resp.StatusCode, got)
	}

	conn, err := grpc.NewClient(a[0].String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}

	defer conn.Close()
	rpcCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer service")
	analyzed, err := pb.NewSymbolsServiceClient(conn).Analyze(rpcCtx, &pb.AnalyzeRequest{
		Files: []*pb.File{{Path: "f.go", Content: []byte("package f\n\nfunc F() {}\n")}},
	})
	if err != nil || len(analyzed.GetAnalyses()) != 1 || analyzed.GetAnalyses()[0].GetDefinitions()[0].GetName() != "F" {
		t.Fatalf("gRPC: %v %v", analyzed, err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestRunRequiresToken(t *testing.T) {
	s := settings{TokenEnv: "FORGE_SYMBOLS_TEST_UNSET", GRPCAddress: "127.0.0.1:0", HTTPAddress: "127.0.0.1:0", Timeout: time.Second}
	if err := run(context.Background(), s, nil); err == nil || !strings.Contains(err.Error(), "service token") {
		t.Fatalf("got %v", err)
	}
}
