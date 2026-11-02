package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func noEnv(string) string { return "" }

func TestParseDefaults(t *testing.T) {
	s, err := parse(nil, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if s.httpAddress != "127.0.0.1:8081" || s.brokerAddress != "127.0.0.1:9090" || s.tokenEnv != "FORGE_PROXY_TOKEN" {
		t.Errorf("settings = %+v", s)
	}
	if s.tokenTTL != 60*time.Second || s.verifyTTL != 30*time.Second {
		t.Errorf("ttl = %v / %v", s.tokenTTL, s.verifyTTL)
	}
	if s.brokerTLS {
		t.Error("TLS enabled by default")
	}
	if s.allowGitPush || s.maxGitPushBytes != 2<<30 {
		t.Errorf("git push = %v, limit %d", s.allowGitPush, s.maxGitPushBytes)
	}
}

func TestParseGitPush(t *testing.T) {
	env := func(key string) string {
		return map[string]string{"FORGE_PROXY_ALLOW_GIT_PUSH": "true", "FORGE_PROXY_MAX_GIT_PUSH_BYTES": "1024"}[key]
	}
	s, err := parse(nil, env, io.Discard)
	if err != nil || !s.allowGitPush || s.maxGitPushBytes != 1024 {
		t.Fatalf("settings = %+v, %v", s, err)
	}
	s, err = parse([]string{"--allow-git-push=false"}, env, io.Discard)
	if err != nil || s.allowGitPush {
		t.Fatalf("flag did not override env: %+v, %v", s, err)
	}
}

func TestParseEnvAndFlagPrecedence(t *testing.T) {
	env := func(key string) string {
		return map[string]string{
			"FORGE_PROXY_BROKER_ADDR": "broker:1", "FORGE_PROXY_TOKEN_TTL": "2m", "FORGE_PROXY_TOKEN_FILE": "/run/token",
		}[key]
	}
	s, err := parse([]string{"--broker-addr", "other:2", "--verify-ttl", "5s"}, env, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if s.brokerAddress != "other:2" || s.tokenTTL != 2*time.Minute || s.verifyTTL != 5*time.Second || s.tokenFile != "/run/token" {
		t.Errorf("settings = %+v", s)
	}
}

func TestParseCAImpliesTLS(t *testing.T) {
	s, err := parse([]string{"--broker-ca-file", "ca.pem"}, noEnv, io.Discard)
	if err != nil || !s.brokerTLS {
		t.Errorf("settings = %+v, err = %v", s, err)
	}
}

func TestParseRejects(t *testing.T) {
	for name, args := range map[string][]string{
		"two token sources": {"--token-file", "f", "--token-env", "E"},
		"bad duration":      {"--token-ttl", "soon"},
		"negative duration": {"--verify-ttl", "-1s"},
		"bad bool":          {"--broker-tls", "maybe"},
		"bad push bool":     {"--allow-git-push", "maybe"},
		"zero push limit":   {"--max-git-push-bytes", "0"},
		"bad push limit":    {"--max-git-push-bytes", "2G"},
		"empty broker":      {"--broker-addr", ""},
		"positional":        {"extra"},
		"unknown flag":      {"--nope"},
	} {
		if _, err := parse(args, noEnv, io.Discard); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseHelp(t *testing.T) {
	var out bytes.Buffer
	if _, err := parse([]string{"--help"}, noEnv, &out); err != flag.ErrHelp {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "--token-ttl") && !strings.Contains(out.String(), "-token-ttl") {
		t.Errorf("help = %q", out.String())
	}
}

func TestRunRequiresToken(t *testing.T) {
	s, _ := parse([]string{"--token-env", "FORGE_PROXY_TEST_UNSET_TOKEN"}, noEnv, io.Discard)
	if err := run(context.Background(), s, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Error("run started without a token")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestRunServesHealthAndStops(t *testing.T) {
	t.Setenv("FORGE_PROXY_TOKEN", "proxy-secret")
	httpAddr, grpcAddr := freeAddr(t), freeAddr(t)
	s, err := parse([]string{"--http-listen", httpAddr, "--grpc-listen", grpcAddr, "--broker-addr", "127.0.0.1:1"}, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, s, slog.New(slog.NewTextHandler(&logs, nil))) }()

	var resp *http.Response
	for i := 0; i < 100; i++ {
		if resp, err = http.Get("http://" + httpAddr + "/healthz"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz = %d", resp.StatusCode)
	}

	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rctx, rcancel := context.WithTimeout(ctx, 3*time.Second)
	defer rcancel()
	check, err := healthpb.NewHealthClient(conn).Check(rctx, &healthpb.HealthCheckRequest{})
	if err != nil || check.Status != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("grpc health = %v, %v", check, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop")
	}
	if strings.Contains(logs.String(), "proxy-secret") {
		t.Error("logs contain the proxy token")
	}
}
