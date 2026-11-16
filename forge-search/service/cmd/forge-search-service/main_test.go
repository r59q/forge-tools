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

	"github.com/r59q/forge-tools/forge-checkouts/service/checkoutspb"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const checkoutID = "0123456789abcdef0123456789abcdef"

func noEnv(string) string { return "" }

func TestSettings(t *testing.T) {
	required := []string{"--checkouts-data-dir=/checkouts"}
	s, err := parse(required, noEnv, io.Discard)
	if err != nil || s.GRPCAddress != "127.0.0.1:9105" || s.HTTPAddress != "127.0.0.1:8095" ||
		s.Timeout != 30*time.Second || s.TokenEnv != "FORGE_SEARCH_TOKEN" || s.dataDirectory != "./search-data" ||
		s.checkoutsAddress != "127.0.0.1:9098" || s.checkoutsInsecure || s.checkoutsTokenEnv != "FORGE_SEARCH_CHECKOUTS_TOKEN" ||
		s.root.Local != "/checkouts/copies" || s.root.Reported != "" || s.indexTimeout != 10*time.Minute || s.maxIndexes != 32 {
		t.Fatalf("defaults: %+v %v", s, err)
	}

	env := map[string]string{
		"FORGE_SEARCH_DATA_DIR":                    "/data",
		"FORGE_SEARCH_CHECKOUTS_INSECURE":          "true",
		"FORGE_SEARCH_CHECKOUTS_REPORTED_DATA_DIR": "/srv/checkouts",
		"FORGE_SEARCH_MAX_INDEXES":                 "4",
	}
	s, err = parse(
		[]string{"--checkouts-data-dir=/mnt/checkouts", "--checkouts-addr=checkouts:9090", "--checkouts-token-file=/run/token", "--index-timeout=1m"},
		func(k string) string { return env[k] },
		io.Discard,
	)
	if err != nil || s.dataDirectory != "/data" || !s.checkoutsInsecure || s.checkoutsAddress != "checkouts:9090" ||
		s.checkoutsTokenFile != "/run/token" || s.checkoutsTokenEnv != "" || s.root.Local != "/mnt/checkouts/copies" ||
		s.root.Reported != "/srv/checkouts/copies" || s.indexTimeout != time.Minute || s.maxIndexes != 4 {
		t.Fatalf("overrides: %+v %v", s, err)
	}

	for _, args := range [][]string{
		{"--unknown=secret-sentinel"},
		{"--checkouts-data-dir=/checkouts", "--checkouts-insecure=secret-sentinel"},
		{"--checkouts-data-dir=/checkouts", "--index-timeout=secret-sentinel"},
		{"--checkouts-data-dir=/checkouts", "--index-timeout=0s"},
		{"--checkouts-data-dir=/checkouts", "--max-indexes=0"},
		{"--checkouts-data-dir=/checkouts", "--checkouts-reported-data-dir=secret-sentinel"},
		{"--checkouts-data-dir=/checkouts", "--checkouts-token-file=/a", "--checkouts-token-env=B"},
		{"--checkouts-data-dir=/checkouts", "--data-dir="},
		{"--checkouts-data-dir=/checkouts", "--checkouts-addr="},
		{},
	} {
		if _, err := parse(args, noEnv, io.Discard); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
			t.Errorf("%v: %v", args, err)
		}
	}

	if _, err := parse([]string{"--help"}, noEnv, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
}

// fakeCheckouts serves one checkout to callers with the checkouts token.
type fakeCheckouts struct {
	checkoutspb.UnimplementedCheckoutsServiceServer
	checkout *checkoutspb.Checkout
}

func (f *fakeCheckouts) GetCheckout(ctx context.Context, r *checkoutspb.GetCheckoutRequest) (*checkoutspb.GetCheckoutResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer checkouts" {
		return nil, status.Error(codes.Unauthenticated, "")
	}
	if r.GetId() != f.checkout.GetId() {
		return nil, status.Error(codes.NotFound, "")
	}
	return &checkoutspb.GetCheckoutResponse{Checkout: f.checkout}, nil
}

// installation is a test installation: a forge-checkouts data directory with one
// ready checkout, a fake forge-checkouts and the settings of this service.
type installation struct {
	dataDir string
	args    []string
}

func newInstallation(t *testing.T) installation {
	t.Helper()
	checkoutsData := t.TempDir()
	dir := filepath.Join(checkoutsData, copies, checkoutID)
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "greet.go"), []byte("package lib\n\nfunc Greet() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	checkoutspb.RegisterCheckoutsServiceServer(g, &fakeCheckouts{checkout: &checkoutspb.Checkout{
		Id: checkoutID, Revision: "rev1", State: "ready", Location: dir,
	}})
	go func() { _ = g.Serve(listener) }()
	t.Cleanup(g.Stop)

	// Tokens are read from the process environment.
	t.Setenv("FORGE_SEARCH_TOKEN", "service")
	t.Setenv("FORGE_SEARCH_CHECKOUTS_TOKEN", "checkouts")

	dataDir := filepath.Join(t.TempDir(), "search")
	return installation{
		dataDir: dataDir,
		args: []string{
			"--data-dir=" + dataDir,
			"--checkouts-data-dir=" + checkoutsData,
			"--checkouts-addr=" + listener.Addr().String(),
			"--checkouts-insecure=true",
			"--grpc-listen=127.0.0.1:0",
			"--http-listen=127.0.0.1:0",
		},
	}
}

// start runs the service until the test ends or stop is called.
func (e installation) start(t *testing.T) (grpcAddr, httpAddr string, stop func()) {
	t.Helper()
	s, err := parse(e.args, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(chan [2]net.Addr, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, s, func(g, h net.Addr) { addrs <- [2]net.Addr{g, h} }) }()

	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if err := <-done; err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}
	t.Cleanup(stop)

	select {
	case a := <-addrs:
		return a[0].String(), a[1].String(), stop
	case err := <-done:
		stopped = true
		cancel()
		t.Fatalf("run: %v", err)
	}
	return "", "", nil
}

func client(t *testing.T, address string) (pb.SearchServiceClient, context.Context) {
	t.Helper()
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewSearchServiceClient(conn), metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer service")
}

func TestRun(t *testing.T) {
	e := newInstallation(t)
	grpcAddr, httpAddr, _ := e.start(t)

	c, ctx := client(t, grpcAddr)
	indexed, err := c.IndexCheckout(ctx, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID})
	if err != nil || indexed.GetAdded() != 1 || indexed.GetIndex().GetRevision() != "rev1" {
		t.Fatalf("gRPC index: %v %v", indexed, err)
	}

	body := `{"query":{"text":"Greet"}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+httpAddr+"/v1/indexes/main:search", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer service")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(got), `"path":"lib/greet.go"`) || !strings.Contains(string(got), `"revision":"rev1"`) {
		t.Fatalf("REST: %d %s", resp.StatusCode, got)
	}

	searched, err := c.SearchCheckout(ctx, &pb.SearchCheckoutRequest{CheckoutId: checkoutID, Query: &pb.CodeQuery{Text: "Greet"}})
	if err != nil || len(searched.GetMatches()) != 1 {
		t.Fatalf("gRPC search: %v %v", searched, err)
	}
	if _, err := c.SearchCheckout(ctx, &pb.SearchCheckoutRequest{CheckoutId: strings.Repeat("f", 32), Query: &pb.CodeQuery{Text: "x"}}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown checkout: %v", err)
	}
}

// TestRestart runs the service twice on one data directory.
func TestRestart(t *testing.T) {
	e := newInstallation(t)
	grpcAddr, _, stop := e.start(t)
	c, ctx := client(t, grpcAddr)
	if _, err := c.IndexCheckout(ctx, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID}); err != nil {
		t.Fatal(err)
	}
	stop()

	for _, path := range []string{e.dataDir, filepath.Join(e.dataDir, "search.db")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s: mode %v", filepath.Base(path), info.Mode().Perm())
		}
	}

	grpcAddr, _, _ = e.start(t)
	c, ctx = client(t, grpcAddr)
	index, err := c.GetIndex(ctx, &pb.GetIndexRequest{Index: "main"})
	if err != nil || index.GetIndex().GetDocuments() != 1 || index.GetIndex().GetRevision() != "rev1" || index.GetIndex().GetCheckoutId() != checkoutID {
		t.Fatalf("index after restart: %v %v", index, err)
	}
	searched, err := c.SearchIndex(ctx, &pb.SearchIndexRequest{Index: "main", Query: &pb.CodeQuery{Text: "Greet"}})
	if err != nil || len(searched.GetMatches()) != 1 {
		t.Fatalf("search after restart: %v %v", searched, err)
	}
	again, err := c.IndexCheckout(ctx, &pb.IndexCheckoutRequest{Index: "main", CheckoutId: checkoutID})
	if err != nil || again.GetUnchanged() != 1 || again.GetAdded() != 0 {
		t.Fatalf("reindex after restart: %v %v", again, err)
	}
}

func TestRunRejectsSharedDataDirectory(t *testing.T) {
	e := newInstallation(t)
	if err := os.Mkdir(e.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := parse(e.args, noEnv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), s, nil); err == nil || strings.Contains(err.Error(), e.dataDir) {
		t.Fatalf("got %v", err)
	}
}

func TestRunRequiresTokens(t *testing.T) {
	for _, unset := range []string{"FORGE_SEARCH_TOKEN", "FORGE_SEARCH_CHECKOUTS_TOKEN"} {
		e := newInstallation(t)
		t.Setenv(unset, "")
		s, err := parse(e.args, noEnv, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if err := run(context.Background(), s, nil); err == nil || !strings.Contains(err.Error(), "token required") {
			t.Fatalf("%s: got %v", unset, err)
		}
		if _, err := os.Stat(e.dataDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: data directory created: %v", unset, err)
		}
	}
}
