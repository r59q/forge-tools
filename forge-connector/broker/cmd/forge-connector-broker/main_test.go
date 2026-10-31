package main

import (
	"bytes"
	"context"
	"encoding/base64"
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

	pb "github.com/r59q/forge-tools/forge-connector/library/brokerpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestSettings(t *testing.T) {
	values := map[string]string{"FORGE_BROKER_DB": "env.db", "FORGE_BROKER_GRPC_LISTEN": "127.0.0.1:9001", "FORGE_BROKER_HTTP_LISTEN": "127.0.0.1:9002", "FORGE_BROKER_REQUEST_TIMEOUT": "2s"}
	getenv := func(name string) string { return values[name] }
	s, err := parse([]string{"--db=flag.db", "--request-timeout=3s"}, getenv, io.Discard)
	if err != nil || s.dbPath != "flag.db" || s.timeout != 3*time.Second || s.grpcAddress != "127.0.0.1:9001" || s.httpAddress != "127.0.0.1:9002" || s.keyEnv != "FORGE_BROKER_KEY" || s.adminEnv != "FORGE_BROKER_ADMIN_TOKEN" {
		t.Fatalf("settings: %+v %v", s, err)
	}
	for _, args := range [][]string{{"--key-file=key", "--key-env=KEY"}, {"--admin-token-file=admin", "--admin-token-env=ADMIN"}, {"--request-timeout=0"}, {"--db="}, {"extra"}, {"--unexpected=secret-sentinel"}} {
		if _, err := parse(args, getenv, io.Discard); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
			t.Fatal("invalid settings accepted or error echoed input")
		}
	}
	values["FORGE_BROKER_REQUEST_TIMEOUT"] = "invalid-secret-sentinel"
	if _, err := parse(nil, getenv, io.Discard); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("invalid environment timeout")
	}
	delete(values, "FORGE_BROKER_REQUEST_TIMEOUT")
	var help bytes.Buffer
	if _, err := parse([]string{"--help"}, getenv, &help); !errors.Is(err, flag.ErrHelp) || !strings.Contains(help.String(), "-key-file") {
		t.Fatal("missing flag help")
	}
}

func TestRequiredSecrets(t *testing.T) {
	dir := t.TempDir()
	keyFile, adminFile := filepath.Join(dir, "key"), filepath.Join(dir, "admin")
	validKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	if err := os.WriteFile(keyFile, []byte(validKey+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adminFile, []byte("test-admin\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_BROKER_KEY", validKey)
	t.Setenv("TEST_BROKER_ADMIN", "test-admin")
	t.Setenv("TEST_BROKER_EMPTY", "")
	t.Setenv("TEST_BROKER_INVALID", "secret-sentinel")
	for _, s := range []settings{{keyEnv: "TEST_BROKER_EMPTY", adminFile: adminFile}, {keyEnv: "TEST_BROKER_INVALID", adminFile: adminFile}, {keyFile: keyFile, adminEnv: "TEST_BROKER_EMPTY"}} {
		s.dbPath = filepath.Join(dir, "must-not-exist.db")
		if err := run(context.Background(), s); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
			t.Fatal("missing or invalid secret accepted, or leaked input")
		}
		if _, err := os.Stat(s.dbPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("database opened before validating secrets")
		}
	}
	for _, s := range []settings{{keyFile: keyFile, adminFile: adminFile}, {keyEnv: "TEST_BROKER_KEY", adminEnv: "TEST_BROKER_ADMIN"}} {
		if _, _, err := s.secrets(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TEST_BROKER_ADMIN", "bad token")
	if _, _, err := (settings{keyEnv: "TEST_BROKER_KEY", adminEnv: "TEST_BROKER_ADMIN"}).secrets(context.Background()); err == nil {
		t.Fatal("invalid admin bearer accepted")
	}
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	return addr
}

func TestRun(t *testing.T) {
	t.Setenv("TEST_BROKER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	t.Setenv("TEST_BROKER_ADMIN", "test-admin")
	s := settings{grpcAddress: freeAddress(t), httpAddress: freeAddress(t), dbPath: filepath.Join(t.TempDir(), "broker.db"), keyEnv: "TEST_BROKER_KEY", adminEnv: "TEST_BROKER_ADMIN", timeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, s) }()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get("http://" + s.httpAddress + "/v1/forges")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != 401 {
				t.Fatalf("unauthenticated REST status: %d", resp.StatusCode)
			}
			break
		}
		select {
		case err := <-done:
			t.Fatalf("startup: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("REST did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn, err := grpc.NewClient(s.grpcAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	callCtx, callCancel := context.WithTimeout(ctx, time.Second)
	defer callCancel()
	if _, err := pb.NewBrokerClient(conn).ListForges(callCtx, &emptypb.Empty{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated gRPC: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("shutdown timed out")
	}
}
