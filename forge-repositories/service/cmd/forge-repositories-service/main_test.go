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

	pb "github.com/r59q/forge-tools/forge-repositories/service/repositoriespb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestSettings(t *testing.T) {
	values := map[string]string{"FORGE_REPOSITORIES_CONFIG": "env.json", "FORGE_REPOSITORIES_GRPC_LISTEN": "127.0.0.1:9001", "FORGE_REPOSITORIES_HTTP_LISTEN": "127.0.0.1:9002", "FORGE_REPOSITORIES_REQUEST_TIMEOUT": "2s"}
	getenv := func(name string) string { return values[name] }
	s, err := parse([]string{"--config=flag.json", "--request-timeout=3s"}, getenv, io.Discard)
	if err != nil || s.configPath != "flag.json" || s.timeout != 3*time.Second || s.grpcAddress != "127.0.0.1:9001" || s.httpAddress != "127.0.0.1:9002" || s.tokenEnv != "FORGE_REPOSITORIES_TOKEN" {
		t.Fatalf("settings: %+v %v", s, err)
	}
	for _, args := range [][]string{{"--token-file=t", "--token-env=T"}, {"--request-timeout=0"}, {"--config="}, {"extra"}, {"--unexpected=secret-sentinel"}} {
		if _, err := parse(args, getenv, io.Discard); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
			t.Fatalf("parse(%q) accepted or echoed input: %v", args, err)
		}
	}
	values["FORGE_REPOSITORIES_REQUEST_TIMEOUT"] = "invalid-secret-sentinel"
	if _, err := parse(nil, getenv, io.Discard); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("invalid environment timeout accepted or echoed")
	}
	var help bytes.Buffer
	if _, err := parse([]string{"--help"}, getenv, &help); !errors.Is(err, flag.ErrHelp) || !strings.Contains(help.String(), "-config") {
		t.Fatal("missing flag help")
	}
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigErrors(t *testing.T) {
	t.Setenv("TEST_TOKEN", "forge-secret-sentinel")
	for name, content := range map[string]string{
		"not json":            `{"sources":`,
		"unknown field":       `{"sources":[{"id":"a","kind":"github","tokenEnv":"TEST_TOKEN","token":"forge-secret-sentinel"}]}`,
		"no sources":          `{}`,
		"no id":               `{"sources":[{"kind":"github","tokenEnv":"TEST_TOKEN"}]}`,
		"no kind":             `{"sources":[{"id":"a","tokenEnv":"TEST_TOKEN"}]}`,
		"unknown kind":        `{"sources":[{"id":"a","kind":"svn","tokenEnv":"TEST_TOKEN"}]}`,
		"no token":            `{"sources":[{"id":"a","kind":"github"}]}`,
		"two tokens":          `{"sources":[{"id":"a","kind":"github","tokenEnv":"TEST_TOKEN","tokenFile":"/x"}]}`,
		"unset token":         `{"sources":[{"id":"a","kind":"github","tokenEnv":"TEST_UNSET_TOKEN"}]}`,
		"connection no proxy": `{"sources":[{"kind":"github","connection":"c1"}]}`,
		"connection token":    `{"proxy":{"url":"http://p","tokenEnv":"TEST_TOKEN"},"sources":[{"kind":"github","connection":"c1","tokenEnv":"TEST_TOKEN"}]}`,
		"bad proxy url":       `{"proxy":{"url":"ftp://p","tokenEnv":"TEST_TOKEN"},"sources":[{"kind":"github","connection":"c1"}]}`,
		"proxy no token":      `{"proxy":{"url":"http://p"},"sources":[{"kind":"github","connection":"c1"}]}`,
		"duplicate id":        `{"proxy":{"url":"http://p","tokenEnv":"TEST_TOKEN"},"sources":[{"kind":"github","connection":"c1"},{"id":"c1","kind":"gitlab","connection":"c2"}]}`,
	} {
		cfg, err := readConfig(writeFile(t, "config.json", content))
		if err == nil {
			_, err = cfg.client(context.Background())
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "sentinel") {
			t.Errorf("%s: error leaks the token: %v", name, err)
		}
	}
	if _, err := readConfig(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestConfigSources(t *testing.T) {
	t.Setenv("TEST_PROXY_TOKEN", "client")
	tokenFile := writeFile(t, "gitlab-token", "forge\n")
	cfg, err := readConfig(writeFile(t, "config.json", fmt.Sprintf(`{
		"proxy": {"url": "http://proxy:8081/", "tokenEnv": "TEST_PROXY_TOKEN"},
		"sources": [
			{"kind": "github", "connection": "c/1"},
			{"id": "work", "kind": "gitlab", "baseUrl": "https://gitlab.example/api/v4", "tokenFile": %q}
		]
	}`, tokenFile)))
	if err != nil {
		t.Fatal(err)
	}
	client, err := cfg.client(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range client.Sources() {
		got = append(got, s.ID+" "+string(s.Session.Kind)+" "+s.Session.BaseURL)
	}
	want := "c/1 github http://proxy:8081/c/c%2F1, work gitlab https://gitlab.example/api/v4"
	if strings.Join(got, ", ") != want {
		t.Fatalf("sources = %q, want %q", got, want)
	}
}

func TestRun(t *testing.T) {
	// A fake forge-connector proxy connection in front of GitHub.
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer client" || r.URL.Path != "/c/conn/user/repos" {
			http.Error(w, `{"message":"no"}`, http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `[{"id":1,"full_name":"mrbean/teddy","html_url":"https://github.com/mrbean/teddy"}]`)
	}))
	defer forge.Close()
	t.Setenv("TEST_PROXY_TOKEN", "client")
	configPath := writeFile(t, "config.json", fmt.Sprintf(`{"proxy":{"url":%q,"tokenEnv":"TEST_PROXY_TOKEN"},"sources":[{"kind":"github","connection":"conn"}]}`, forge.URL))
	tokenFile := writeFile(t, "token", "service\n")
	s, err := parse([]string{"--config=" + configPath, "--token-file=" + tokenFile, "--grpc-listen=127.0.0.1:0", "--http-listen=127.0.0.1:0"}, func(string) string { return "" }, io.Discard)
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

	req, _ := http.NewRequest("GET", "http://"+a[1].String()+"/v1/repositories", nil)
	req.Header.Set("Authorization", "Bearer service")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"mrbean/teddy"`) {
		t.Fatalf("REST: %d %s", resp.StatusCode, body)
	}

	conn, err := grpc.NewClient(a[0].String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	rpcCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer service")
	sources, err := pb.NewRepositoriesClient(conn).ListSources(rpcCtx, &emptypb.Empty{})
	if err != nil || len(sources.GetSources()) != 1 || sources.GetSources()[0].GetId() != "conn" {
		t.Fatalf("gRPC: %v %v", sources, err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestRunRequiresServiceToken(t *testing.T) {
	configPath := writeFile(t, "config.json", `{"sources":[]}`)
	for _, token := range []string{"", "two words"} {
		tokenFile := writeFile(t, "token", token)
		s := settings{configPath: configPath, tokenFile: tokenFile, grpcAddress: "127.0.0.1:0", httpAddress: "127.0.0.1:0", timeout: time.Second}
		if err := run(context.Background(), s, nil); err == nil || !strings.Contains(err.Error(), "service token") {
			t.Errorf("token %q: %v", token, err)
		}
	}
}
