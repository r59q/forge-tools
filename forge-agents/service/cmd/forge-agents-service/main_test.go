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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/r59q/forge-tools/forge-agents/service/agentspb"
	"github.com/r59q/forge-tools/forge-checkouts/service/checkoutspb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const checkoutID = "0123456789abcdef0123456789abcdef"

// fakeCodex is a hand-built Codex turn: it records its environment in the
// workspace and replays a fixed transcript. No model is ever called.
const fakeCodex = `#!/bin/sh
cat >/dev/null
env > "$PWD/client-env"
printf '%s\n' \
  '{"type":"thread.started","thread_id":"thread-1"}' \
  '{"type":"turn.started"}' \
  '{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"hello"}}' \
  '{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":5}}'
`

func noEnv(string) string { return "" }

func TestSettings(t *testing.T) {
	s, err := parse(nil, noEnv, io.Discard)
	if err != nil || s.GRPCAddress != "127.0.0.1:9106" || s.HTTPAddress != "127.0.0.1:8096" || s.Timeout != 30*time.Second ||
		s.TokenEnv != "FORGE_AGENTS_TOKEN" || s.dataDirectory != "./agents-data" || len(s.clients) != 3 || s.clients["codex"] != "codex" ||
		s.roots != nil || s.checkouts != (checkoutSettings{}) || s.bounds.MaxAgents != 100 || s.bounds.MaxRunning != 4 ||
		s.bounds.MaxEvents != 10000 || s.bounds.TurnTimeout != time.Hour || s.streamTimeout != 10*time.Minute || s.toolsListen != "127.0.0.1:0" {
		t.Fatalf("defaults: %+v %v", s, err)
	}

	env := map[string]string{
		"FORGE_AGENTS_CLIENTS":                     "codex,claude",
		"FORGE_AGENTS_CLAUDE_PATH":                 "/opt/claude",
		"FORGE_AGENTS_CHECKOUTS_INSECURE":          "true",
		"FORGE_AGENTS_CHECKOUTS_REPORTED_DATA_DIR": "/srv/checkouts",
		"FORGE_AGENTS_MAX_RUNNING":                 "2",
	}
	s, err = parse(
		[]string{
			"--checkouts-addr=checkouts:9090", "--checkouts-data-dir=/data", "--checkouts-token-file=/run/token",
			"--path-roots=/work,/src,/work", "--turn-timeout=5m", "--stream-timeout=1m", "--max-events=50",
		},
		func(k string) string { return env[k] },
		io.Discard,
	)
	if err != nil || len(s.clients) != 2 || s.clients["claude"] != "/opt/claude" || !slices.Equal(s.roots, []string{"/work", "/src"}) ||
		s.checkouts.address != "checkouts:9090" || !s.checkouts.insecure || s.checkouts.tokenFile != "/run/token" || s.checkouts.tokenEnv != "" ||
		s.checkouts.root.Local != "/data/copies" || s.checkouts.root.Reported != "/srv/checkouts/copies" ||
		s.bounds.MaxRunning != 2 || s.bounds.MaxEvents != 50 || s.bounds.TurnTimeout != 5*time.Minute || s.streamTimeout != time.Minute {
		t.Fatalf("overrides: %+v %v", s, err)
	}
	s, err = parse([]string{"--checkouts-addr=c:1", "--checkouts-data-dir=/data"}, noEnv, io.Discard)
	if err != nil || s.checkouts.tokenEnv != "FORGE_AGENTS_CHECKOUTS_TOKEN" {
		t.Fatalf("checkouts token default: %+v %v", s.checkouts, err)
	}

	for _, args := range [][]string{
		{"--unknown=secret-sentinel"},
		{"--clients=secret-sentinel"},
		{"--clients="},
		{"--codex-path="},
		{"--path-roots=secret-sentinel"},
		{"--path-roots=/a/../b"},
		{"--max-agents=0"},
		{"--max-running=secret-sentinel"},
		{"--max-events=-1"},
		{"--turn-timeout=0s"},
		{"--stream-timeout=secret-sentinel"},
		{"--data-dir="},
		{"--tools-listen="},
		{"--checkouts-insecure=secret-sentinel"},
		{"--checkouts-data-dir=/data"},
		{"--checkouts-token-file=/run/token"},
		{"--checkouts-addr=c:1"},
		{"--checkouts-addr=c:1", "--checkouts-data-dir=/data", "--checkouts-reported-data-dir=secret-sentinel"},
		{"--checkouts-addr=c:1", "--checkouts-data-dir=/data", "--checkouts-token-file=/a", "--checkouts-token-env=B"},
	} {
		if _, err := parse(args, noEnv, io.Discard); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
			t.Errorf("%v: %v", args, err)
		}
	}

	if _, err := parse([]string{"--help"}, noEnv, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
}

func TestClientEnv(t *testing.T) {
	got := clientEnv([]string{"PATH=/bin", "FORGE_AGENTS_TOKEN=s", "FORGE_X=y", "MY_TOKEN=t", "OPENAI_API_KEY=k"}, "MY_TOKEN", "")
	if !slices.Equal(got, []string{"PATH=/bin", "OPENAI_API_KEY=k"}) {
		t.Fatalf("env %q", got)
	}
}

// fakeCheckouts creates and removes worktree directories for callers with
// the checkouts token.
type fakeCheckouts struct {
	checkoutspb.UnimplementedCheckoutsServiceServer
	copies string

	mu      sync.Mutex
	removed []*checkoutspb.RemoveCheckoutRequest
}

func authorizedCheckouts(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer checkouts" {
		return status.Error(codes.Unauthenticated, "")
	}
	return nil
}

func (f *fakeCheckouts) CreateCheckout(ctx context.Context, r *checkoutspb.CreateCheckoutRequest) (*checkoutspb.CreateCheckoutResponse, error) {
	if err := authorizedCheckouts(ctx); err != nil {
		return nil, err
	}
	if r.GetMode() != "worktree" || r.GetRepository().GetPath() != "o/r" {
		return nil, status.Error(codes.InvalidArgument, "")
	}
	dir := filepath.Join(f.copies, checkoutID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, status.Error(codes.Internal, "")
	}
	return &checkoutspb.CreateCheckoutResponse{Checkout: &checkoutspb.Checkout{
		Id: checkoutID, State: "ready", Location: dir, Reference: "main", Mode: "worktree",
	}}, nil
}

func (f *fakeCheckouts) RemoveCheckout(ctx context.Context, r *checkoutspb.RemoveCheckoutRequest) (*checkoutspb.RemoveCheckoutResponse, error) {
	if err := authorizedCheckouts(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, r)
	return &checkoutspb.RemoveCheckoutResponse{}, nil
}

// installation is a test installation: a fake Codex binary, a fake
// forge-checkouts and the settings of this service.
type installation struct {
	dataDir   string
	checkouts *fakeCheckouts
	args      []string
}

func newInstallation(t *testing.T) installation {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(fakeCodex), 0o700); err != nil { //nolint:gosec // an executable test script.
		t.Fatal(err)
	}

	checkoutsData := t.TempDir()
	fake := &fakeCheckouts{copies: filepath.Join(checkoutsData, copies)}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	checkoutspb.RegisterCheckoutsServiceServer(g, fake)
	go func() { _ = g.Serve(listener) }()
	t.Cleanup(g.Stop)

	// Tokens are read from the process environment; the client credential
	// must reach the client and the tokens must not.
	t.Setenv("FORGE_AGENTS_TOKEN", "service")
	t.Setenv("FORGE_AGENTS_CHECKOUTS_TOKEN", "checkouts")
	t.Setenv("OPENAI_API_KEY", "client-credential")

	dataDir := filepath.Join(t.TempDir(), "agents")
	return installation{
		dataDir:   dataDir,
		checkouts: fake,
		args: []string{
			"--data-dir=" + dataDir,
			"--clients=codex",
			"--codex-path=" + filepath.Join(bin, "codex"),
			"--checkouts-addr=" + listener.Addr().String(),
			"--checkouts-insecure=true",
			"--checkouts-data-dir=" + checkoutsData,
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

func client(t *testing.T, address string) (pb.AgentsServiceClient, context.Context) {
	t.Helper()
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewAgentsServiceClient(conn), metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer service")
}

// turn sends text and waits for the agent to be idle again.
func turn(t *testing.T, c pb.AgentsServiceClient, ctx context.Context, id, text string) *pb.Agent { //nolint:revive // t first, as in every helper.
	t.Helper()
	if _, err := c.SendMessage(ctx, &pb.SendMessageRequest{Id: id, Text: text}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := c.GetAgent(ctx, &pb.GetAgentRequest{Id: id})
		if err != nil {
			t.Fatal(err)
		}
		if r.GetAgent().GetState() == "idle" {
			return r.GetAgent()
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent %v", r.GetAgent())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRun(t *testing.T) {
	e := newInstallation(t)
	grpcAddr, httpAddr, _ := e.start(t)
	c, ctx := client(t, grpcAddr)

	created, err := c.CreateAgent(ctx, &pb.CreateAgentRequest{Client: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	a := turn(t, c, ctx, created.GetAgent().GetId(), "hi")
	if a.GetSession() != "thread-1" || a.GetWorkspace().GetPath() != filepath.Join(e.dataDir, scratchDir, a.GetId()) {
		t.Fatalf("agent %v", a)
	}
	env, err := os.ReadFile(filepath.Join(a.GetWorkspace().GetPath(), "client-env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(env), "OPENAI_API_KEY=client-credential") || strings.Contains(string(env), "=service\n") || strings.Contains(string(env), "=checkouts\n") {
		t.Error("client environment lacks the client credential or holds a service token")
	}

	req, _ := http.NewRequest(http.MethodGet, "http://"+httpAddr+"/v1/agents/"+a.GetId()+"/events", nil)
	req.Header.Set("Authorization", "Bearer service")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(got), `"text":"hello"`) || strings.Contains(string(got), "client-credential") {
		t.Fatalf("REST: %d %s", resp.StatusCode, got)
	}
	if _, err := c.DeleteAgent(ctx, &pb.DeleteAgentRequest{Id: a.GetId()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.GetWorkspace().GetPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("scratch directory kept: %v", err)
	}
}

func TestRunCheckout(t *testing.T) {
	e := newInstallation(t)
	grpcAddr, _, _ := e.start(t)
	c, ctx := client(t, grpcAddr)

	created, err := c.CreateAgent(ctx, &pb.CreateAgentRequest{
		Client:    "codex",
		Workspace: &pb.WorkspaceSpec{Kind: "checkout", Repository: &pb.Repository{Source: "github", Path: "o/r"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	a := turn(t, c, ctx, created.GetAgent().GetId(), "hi")
	dir := filepath.Join(e.checkouts.copies, checkoutID)
	if a.GetWorkspace().GetCheckout() != checkoutID || a.GetWorkspace().GetPath() != dir || a.GetWorkspace().GetReference() != "main" {
		t.Fatalf("workspace %v", a.GetWorkspace())
	}
	if _, err := os.Stat(filepath.Join(dir, "client-env")); err != nil {
		t.Errorf("client did not run in the checkout: %v", err)
	}
	if _, err := c.DeleteAgent(ctx, &pb.DeleteAgentRequest{Id: a.GetId(), RemoveCheckout: true}); err != nil {
		t.Fatal(err)
	}
	e.checkouts.mu.Lock()
	defer e.checkouts.mu.Unlock()
	if len(e.checkouts.removed) != 1 || e.checkouts.removed[0].GetId() != checkoutID || e.checkouts.removed[0].GetDiscard() {
		t.Fatalf("removed %v", e.checkouts.removed)
	}
}

// TestRestart runs the service twice on one data directory.
func TestRestart(t *testing.T) {
	e := newInstallation(t)
	grpcAddr, _, stop := e.start(t)
	c, ctx := client(t, grpcAddr)
	created, err := c.CreateAgent(ctx, &pb.CreateAgentRequest{Client: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetAgent().GetId()
	turn(t, c, ctx, id, "hi")
	stop()

	for _, path := range []string{e.dataDir, filepath.Join(e.dataDir, scratchDir), filepath.Join(e.dataDir, runDir)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s: mode %v", path, info.Mode().Perm())
		}
	}

	grpcAddr, _, _ = e.start(t)
	c, ctx = client(t, grpcAddr)
	a := turn(t, c, ctx, id, "again")
	if a.GetTurns() != 2 || a.GetSession() != "thread-1" {
		t.Fatalf("agent after restart: %v", a)
	}
	page, err := c.ListEvents(ctx, &pb.ListEventsRequest{Id: id})
	if err != nil || len(page.GetEvents()) != 8 {
		t.Fatalf("events after restart: %v %v", page, err)
	}
}

func TestRunRejectsSharedDataDirectory(t *testing.T) {
	e := newInstallation(t)
	if err := os.Mkdir(e.dataDir, 0o755); err != nil { //nolint:gosec // a shared directory on purpose.
		t.Fatal(err)
	}
	if err := os.Chmod(e.dataDir, 0o755); err != nil { //nolint:gosec // a shared directory on purpose.
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

func TestRunRequiresTokensAndClients(t *testing.T) {
	for name, change := range map[string]func(*installation){
		"service token":   func(*installation) { t.Setenv("FORGE_AGENTS_TOKEN", "") },
		"checkouts token": func(*installation) { t.Setenv("FORGE_AGENTS_CHECKOUTS_TOKEN", "") },
		"client binary":   func(e *installation) { e.args = append(e.args, "--codex-path=/nonexistent/secret-sentinel") },
	} {
		e := newInstallation(t)
		change(&e)
		s, err := parse(e.args, noEnv, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if err := run(context.Background(), s, nil); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
			t.Fatalf("%s: got %v", name, err)
		}
		if _, err := os.Stat(e.dataDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: data directory created: %v", name, err)
		}
	}
}
