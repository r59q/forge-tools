package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	brokerapi "github.com/r59q/forge-tools/forge-connector/broker/api"
	"github.com/r59q/forge-tools/forge-connector/library/broker"
	brokerpb "github.com/r59q/forge-tools/forge-connector/library/brokerpb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

// startBroker serves a real broker admin API, in front of a fake GitHub
// Enterprise that accepts forge-synthetic. Until delay has passed, the
// address refuses connections.
func startBroker(t *testing.T, delay time.Duration) (*broker.Manager, string, string) {
	t.Helper()
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/user" || r.Header.Get("Authorization") != "Bearer forge-synthetic" {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "login": "owner"})
	}))
	t.Cleanup(forge.Close)
	registry := &forgeauth.Registry{}
	if err := registry.Register(github.Provider{}); err != nil {
		t.Fatal(err)
	}
	sealer, err := broker.NewAESGCMSealer(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	m, err := broker.NewManager(&broker.MemoryStore{}, sealer, registry)
	if err != nil {
		t.Fatal(err)
	}
	server, err := brokerapi.New(m, forgeauth.StaticToken("admin-synthetic"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	brokerpb.RegisterBrokerServer(g, server)
	address := l.Addr().String()
	if delay > 0 {
		_ = l.Close()
	}
	go func(l net.Listener) {
		if delay > 0 {
			time.Sleep(delay)
			var err error
			if l, err = net.Listen("tcp", address); err != nil {
				t.Error(err)
				return
			}
		}
		_ = g.Serve(l)
	}(l)
	t.Cleanup(g.Stop)
	return m, address, forge.URL
}

// waitForClient waits until the broker accepts token as a client token.
func waitForClient(t *testing.T, m *broker.Manager, token string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := m.ClientConnections(context.Background(), forgeauth.StaticToken(token)); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("client token was not registered")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRegisterServiceClients(t *testing.T) {
	logf = t.Logf
	t.Cleanup(func() { logf = log.Printf })
	proxyToken, reposToken, issuesToken := strings.Repeat("p", 40), strings.Repeat("r", 40), strings.Repeat("i", 40)
	// The broker starts after management, which retries until it answers.
	m, address, _ := startBroker(t, 1500*time.Millisecond)
	t.Setenv("MANAGEMENT_TEST_ADMIN", "admin-synthetic")
	t.Setenv("FORGE_MANAGEMENT_PROXY_TOKEN", proxyToken)
	t.Setenv("FORGE_MANAGEMENT_REPOSITORIES_CONNECTOR_TOKEN", reposToken)
	t.Setenv("FORGE_MANAGEMENT_ISSUES_CONNECTOR_TOKEN", issuesToken)
	args := []string{"--token-file=" + writeToken(t, "operator-synthetic"), "--http-listen=127.0.0.1:0", "--grpc-listen=127.0.0.1:0", "--connector-address=" + address, "--connector-insecure=true", "--connector-admin-token-env=MANAGEMENT_TEST_ADMIN"}
	s, err := parse(args, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	startManagement(t, s)
	waitForClient(t, m, proxyToken)
	waitForClient(t, m, reposToken)
	waitForClient(t, m, issuesToken)
	clients, err := m.ListClients(context.Background())
	if err != nil || len(clients) != 3 {
		t.Fatalf("clients = %+v, %v", clients, err)
	}
	for _, c := range clients {
		if !c.AllConnections || c.CanIssueTokens != (c.Name == "forge-connector-proxy") {
			t.Fatalf("client grants = %+v", c)
		}
	}

	// Rotation: a restart with a new token replaces the client.
	rotated := strings.Repeat("q", 40)
	t.Setenv("FORGE_MANAGEMENT_PROXY_TOKEN", rotated)
	startManagement(t, s)
	waitForClient(t, m, rotated)
	if _, err := m.ClientConnections(context.Background(), forgeauth.StaticToken(proxyToken)); !errors.Is(err, broker.ErrUnauthorized) {
		t.Fatalf("old proxy token = %v", err)
	}
	if clients, _ := m.ListClients(context.Background()); len(clients) != 3 {
		t.Fatalf("old client kept: %+v", clients)
	}
}

func TestClientTokenChecks(t *testing.T) {
	_, address, _ := startBroker(t, 0)
	t.Setenv("MANAGEMENT_TEST_ADMIN", "admin-synthetic-"+strings.Repeat("a", 40))
	base := []string{"--token-file=" + writeToken(t, "operator-synthetic"), "--http-listen=127.0.0.1:0", "--grpc-listen=127.0.0.1:0", "--connector-address=" + address, "--connector-insecure=true", "--connector-admin-token-env=MANAGEMENT_TEST_ADMIN"}
	for name, extra := range map[string][]string{
		"missing explicit": {"--proxy-token-file=" + filepath.Join(t.TempDir(), "missing")},
		"short":            {"--proxy-token-file=" + writeToken(t, "short-synthetic")},
		"same as admin":    {"--proxy-token-env=MANAGEMENT_TEST_ADMIN"},
		"shared":           {"--proxy-token-file=" + writeToken(t, strings.Repeat("s", 40)), "--repositories-connector-token-file=" + writeToken(t, strings.Repeat("s", 40))},
	} {
		s, err := parse(append(base, extra...), func(string) string { return "" }, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if err := run(context.Background(), s, nil); err == nil || strings.Contains(err.Error(), "synthetic") || strings.Contains(err.Error(), "sss") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestRunWithConnector(t *testing.T) {
	m, address, forge := startBroker(t, 0)
	t.Setenv("MANAGEMENT_TEST_ADMIN", "admin-synthetic")
	args := []string{"--token-file=" + writeToken(t, "operator-synthetic"), "--http-listen=127.0.0.1:0", "--grpc-listen=127.0.0.1:0", "--connector-address=" + address, "--connector-insecure=true", "--connector-admin-token-env=MANAGEMENT_TEST_ADMIN"}
	s, err := parse(args, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	a := startManagement(t, s)
	r, err := http.NewRequest("POST", "http://"+a[1].String()+"/v1/connections", strings.NewReader(`{"forge":"github","host":"`+forge+`","credential":"forge-synthetic"}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer operator-synthetic")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.Contains(string(body), "synthetic") {
		t.Fatalf("add: %d %s", resp.StatusCode, body)
	}
	var added pb.Connection
	if err := protojson.Unmarshal(body, &added); err != nil || added.Login != "owner" {
		t.Fatalf("added = %v, %v", &added, err)
	}
	if conns, _ := m.ListConnections(context.Background()); len(conns) != 1 || string(conns[0].ID) != added.Id {
		t.Fatalf("broker connections = %+v", conns)
	}
	if code, body := httpGet(t, a[1], "/connections", true); code != 200 || !strings.Contains(body, added.Id) || strings.Contains(body, "synthetic") {
		t.Fatalf("web: %d %s", code, body)
	}
	t.Setenv("MANAGEMENT_TEST_ADMIN", "operator-synthetic")
	if code, _ := httpGet(t, a[1], "/v1/connections", false); code != 503 {
		t.Fatal("operator token was accepted as the admin token")
	}

	s.tokenFile = writeToken(t, "admin-synthetic")
	t.Setenv("MANAGEMENT_TEST_ADMIN", "admin-synthetic")
	if err := run(context.Background(), s, nil); err == nil || strings.Contains(err.Error(), "synthetic") {
		t.Fatalf("shared operator and admin token = %v", err)
	}
}
