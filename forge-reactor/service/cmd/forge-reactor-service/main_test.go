package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/servicekit"
)

func TestParse(t *testing.T) {
	none := func(string) string { return "" }
	s, err := parse(nil, none, io.Discard)
	if err != nil || s.TokenEnv != "FORGE_REACTOR_TOKEN" || s.dataDirectory != "./reactor-data" || s.rabbitEnv != "FORGE_REACTOR_RABBITMQ_URL" ||
		s.queuePrefix != "forge-reactor." || s.GRPCAddress != "127.0.0.1:9103" || s.HTTPAddress != "127.0.0.1:8093" {
		t.Fatal(s, err)
	}

	env := map[string]string{
		"FORGE_REACTOR_DATA_DIR": "/data", "FORGE_REACTOR_RABBITMQ_URL_FILE": "/run/url", "FORGE_REACTOR_CONFIG": "/etc/r.json",
		"FORGE_REACTOR_PUBLIC_URL": "https://example.org/reactor",
	}
	s, err = parse(nil, func(k string) string { return env[k] }, io.Discard)
	if err != nil || s.dataDirectory != "/data" || s.rabbitFile != "/run/url" || s.rabbitEnv != "" || s.configFile != "/etc/r.json" ||
		s.publicURL != "https://example.org/reactor" {
		t.Fatal(s, err)
	}

	for _, args := range [][]string{
		{"--data-dir="}, {"--queue-prefix="}, {"--rabbitmq-url-env=X", "--rabbitmq-url-file=x"}, {"--token-env=X", "--token-file=x"}, {"--unknown"},
	} {
		if _, err := parse(args, none, io.Discard); err == nil {
			t.Error("accepted", args)
		}
	}
}

func TestStartupErrors(t *testing.T) {
	t.Setenv("REACTOR_TEST_TOKEN", "fixture-token")
	t.Setenv("REACTOR_TEST_URL", "amqp://guest:guest@127.0.0.1:1/")
	dir := t.TempDir()
	polls := filepath.Join(dir, "polls.json")
	if err := os.WriteFile(polls, []byte(`{"polls":[{"id":"p","connection":"fj","repository":"a/b","branches":["main"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	base := settings{
		Settings:      servicekit.Settings{GRPCAddress: "127.0.0.1:0", HTTPAddress: "127.0.0.1:0", TokenEnv: "REACTOR_TEST_TOKEN", Timeout: time.Second},
		dataDirectory: filepath.Join(dir, "data"),
		rabbitEnv:     "REACTOR_TEST_URL",
		queuePrefix:   "forge-reactor.",
	}

	for name, change := range map[string]func(*settings){
		"no broker URL":     func(s *settings) { s.rabbitEnv = "REACTOR_TEST_UNSET" },
		"bad broker URL":    func(s *settings) { s.rabbitEnv = "REACTOR_TEST_TOKEN" },
		"missing config":    func(s *settings) { s.configFile = filepath.Join(dir, "missing.json") },
		"polls, no connect": func(s *settings) { s.configFile = polls },
		"bad public URL":    func(s *settings) { s.publicURL = "example.org" },
	} {
		s := base
		change(&s)
		err := run(context.Background(), s, nil)
		if err == nil || strings.Contains(err.Error(), "guest") || strings.Contains(err.Error(), "fixture-token") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// service runs the reactor until stop, against a broker that is down.
type service struct {
	t    *testing.T
	base string
	stop func()
}

func start(t *testing.T, s settings) service {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, s, func(_, httpAddr net.Addr) { ready <- "http://" + httpAddr.String() }) }()
	var address string
	select {
	case address = <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("startup timed out")
	}

	return service{t: t, base: address, stop: func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(7 * time.Second):
			t.Error("shutdown timed out")
		}
	}}
}

func (s service) request(method, path string, header http.Header, body string) (int, map[string]any) {
	req, _ := http.NewRequestWithContext(context.Background(), method, s.base+path, strings.NewReader(body))
	req.Header = header
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)

	return resp.StatusCode, out
}

func TestRestartKeepsEvents(t *testing.T) {
	t.Setenv("REACTOR_TEST_TOKEN", "fixture-token")
	t.Setenv("REACTOR_TEST_URL", "amqp://guest:guest@127.0.0.1:1/")
	t.Setenv("REACTOR_TEST_SECRET", "hook-secret")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "reactor.json")
	if err := os.WriteFile(cfg, []byte(`{"webhooks":[{"id":"gh","forge":"github","connection":"github","secretEnv":"REACTOR_TEST_SECRET"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := settings{
		Settings:      servicekit.Settings{GRPCAddress: "127.0.0.1:0", HTTPAddress: "127.0.0.1:0", TokenEnv: "REACTOR_TEST_TOKEN", Timeout: time.Second},
		dataDirectory: filepath.Join(dir, "data"),
		configFile:    cfg,
		rabbitEnv:     "REACTOR_TEST_URL",
		queuePrefix:   "forge-reactor.",
	}
	authed := http.Header{"Authorization": {"Bearer fixture-token"}}

	svc := start(t, s)
	if code, _ := svc.request(http.MethodPost, "/v1/subscriptions", authed, `{"id":"ci"}`); code != http.StatusOK {
		t.Fatalf("create: %d", code)
	}
	body := `{"ref":"refs/heads/main","before":"1111","after":"2222","repository":{"full_name":"owner/repo"}}`
	m := hmac.New(sha256.New, []byte("hook-secret"))
	m.Write([]byte(body))
	hook := http.Header{
		"X-Github-Event":      {"push"},
		"X-Github-Delivery":   {"d-1"},
		"X-Hub-Signature-256": {"sha256=" + hex.EncodeToString(m.Sum(nil))},
	}
	code, accepted := svc.request(http.MethodPost, "/webhooks/gh", hook, body)
	if code != http.StatusAccepted || accepted["event"] == nil {
		t.Fatalf("webhook: %d %v", code, accepted)
	}
	code, managed := svc.request(http.MethodPost, "/v1/webhook-sources", authed, `{"id":"app","forge":"github"}`)
	if code != http.StatusOK || managed["secret"] == nil {
		t.Fatalf("managed source: %d %v", code, managed)
	}
	if code, _ := svc.request(http.MethodGet, "/v1/events", nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list: %d", code)
	}
	svc.stop()

	svc = start(t, s)
	defer svc.stop()
	code, got := svc.request(http.MethodGet, "/v1/events/"+accepted["event"].(string), authed, "")
	deliveries, _ := got["deliveries"].([]any)
	if code != http.StatusOK || len(deliveries) != 1 || deliveries[0].(map[string]any)["status"] != "pending" {
		t.Fatalf("after restart: %d %v", code, got)
	}
	if code, again := svc.request(http.MethodPost, "/webhooks/gh", hook, body); code != http.StatusAccepted || again["duplicate"] != true {
		t.Fatalf("redelivered webhook: %d %v", code, again)
	}

	m = hmac.New(sha256.New, []byte(managed["secret"].(string)))
	m.Write([]byte(body))
	hook.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(m.Sum(nil)))
	if code, got := svc.request(http.MethodPost, "/webhooks/app", hook, body); code != http.StatusAccepted || got["event"] == nil {
		t.Fatalf("managed source after restart: %d %v", code, got)
	}
}
