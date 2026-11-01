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
	"sync/atomic"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-repositories/library/repositories"
	repoapi "github.com/r59q/forge-tools/forge-repositories/service/api"
	repopb "github.com/r59q/forge-tools/forge-repositories/service/repositoriespb"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestSettings(t *testing.T) {
	values := map[string]string{"FORGE_MANAGEMENT_HTTP_LISTEN": "127.0.0.1:9000", "FORGE_MANAGEMENT_REQUEST_TIMEOUT": "2s"}
	getenv := func(name string) string { return values[name] }
	s, err := parse([]string{"--request-timeout=3s"}, getenv, io.Discard)
	if err != nil || s.httpAddress != values["FORGE_MANAGEMENT_HTTP_LISTEN"] || s.timeout != 3*time.Second || s.tokenEnv != "FORGE_MANAGEMENT_TOKEN" || s.repositoriesAddress != "" {
		t.Fatalf("settings: %+v %v", s, err)
	}
	s, err = parse([]string{"--repositories-address=localhost:9092"}, getenv, io.Discard)
	if err != nil || s.repositoriesTokenEnv != "FORGE_MANAGEMENT_REPOSITORIES_TOKEN" || s.repositoriesInsecure {
		t.Fatalf("backend settings: %+v %v", s, err)
	}
	for _, args := range [][]string{
		{"--token-file=t", "--token-env=T"}, {"--request-timeout=0"}, {"--request-timeout=invalid-secret"}, {"extra"}, {"--unknown=secret"},
		{"--http-listen="}, {"--grpc-listen="}, {"--repositories-insecure=invalid"}, {"--repositories-insecure=true"}, {"--repositories-token-env=T"},
		{"--repositories-address=x", "--repositories-token-file=t", "--repositories-token-env=T"},
	} {
		if _, err := parse(args, getenv, io.Discard); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("accepted or echoed invalid flags: %v %v", args, err)
		}
	}
	var help bytes.Buffer
	if _, err := parse([]string{"--help"}, getenv, &help); !errors.Is(err, flag.ErrHelp) || !strings.Contains(help.String(), "repositories-address") {
		t.Fatal("missing help")
	}
}

func writeToken(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCredentialConfiguration(t *testing.T) {
	t.Setenv("MANAGEMENT_TEST_TOKEN", "operator-synthetic")
	t.Setenv("MANAGEMENT_TEST_BACKEND", "backend-synthetic")
	t.Setenv("MANAGEMENT_TEST_EMPTY", "")
	t.Setenv("MANAGEMENT_TEST_INVALID", "invalid secret\nvalue")
	for _, env := range []string{"MANAGEMENT_TEST_EMPTY", "MANAGEMENT_TEST_INVALID"} {
		if _, err := (settings{tokenEnv: env}).serviceToken(context.Background()); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("accepted or leaked invalid token", err)
		}
	}
	op := forgeauth.EnvToken("MANAGEMENT_TEST_TOKEN")
	for _, env := range []string{"MANAGEMENT_TEST_EMPTY", "MANAGEMENT_TEST_INVALID", "MANAGEMENT_TEST_TOKEN"} {
		_, _, err := (settings{repositoriesAddress: "localhost:1", repositoriesTokenEnv: env}).repositoryReader(context.Background(), op)
		if err == nil || strings.Contains(err.Error(), "synthetic") || strings.Contains(err.Error(), "secret") {
			t.Fatal("accepted or leaked invalid backend token", err)
		}
	}
	file := writeToken(t, "backend-synthetic\n")
	reader, closeBackend, err := (settings{repositoriesAddress: "localhost:1", repositoriesTokenFile: file}).repositoryReader(context.Background(), op)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBackend()
	// No live backend is necessary at startup. Rotating credentials to match
	// the operator must still fail before making a request.
	if err := os.WriteFile(file, []byte("operator-synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.List(context.Background(), repositories.ListOptions{}); err == nil {
		t.Fatal("rotation allowed a shared operator/backend credential")
	}
}

func startManagement(t *testing.T, s settings) [2]net.Addr {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan [2]net.Addr, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, s, func(g, h net.Addr) { ready <- [2]net.Addr{g, h} }) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(7 * time.Second):
			t.Error("shutdown timed out")
		}
	})
	select {
	case addresses := <-ready:
		return addresses
	case err := <-done:
		done <- err
		t.Fatalf("startup: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("startup timed out")
	}
	return [2]net.Addr{}
}

func httpGet(t *testing.T, address net.Addr, path string, basic bool) (int, string) {
	t.Helper()
	r, err := http.NewRequest("GET", "http://"+address.String()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if basic {
		r.SetBasicAuth("operator", "operator-synthetic")
	} else {
		r.Header.Set("Authorization", "Bearer operator-synthetic")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestRunWithoutTools(t *testing.T) {
	s, err := parse([]string{"--token-file=" + writeToken(t, "operator-synthetic"), "--http-listen=127.0.0.1:0", "--grpc-listen=127.0.0.1:0"}, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	a := startManagement(t, s)
	if code, body := httpGet(t, a[1], "/", true); code != 200 || !strings.Contains(body, "No repository capability configured") {
		t.Fatalf("web: %d %s", code, body)
	}
	conn, err := grpc.NewClient(a[0].String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer operator-synthetic"))
	listing, err := pb.NewManagementClient(conn).ListRepositories(ctx, &emptypb.Empty{})
	if err != nil || listing.GetConfigured() || len(listing.GetRepositories()) != 0 {
		t.Fatalf("gRPC: %v %v", listing, err)
	}
}

func TestRunWithNativeRepositories(t *testing.T) {
	var mode atomic.Int32
	var calls atomic.Int32
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer forge-synthetic" || r.URL.Path != "/user/repos" {
			t.Error("wrong forge request")
			http.Error(w, "denied", 401)
			return
		}
		switch mode.Load() {
		case 1:
			fmt.Fprint(w, `[]`)
		case 2:
			http.Error(w, "forge-synthetic backend-synthetic", 500)
		case 3:
			http.Error(w, "forge-synthetic backend-synthetic", 401)
		case 4:
			http.Error(w, "forge-synthetic backend-synthetic", 403)
		default:
			fmt.Fprint(w, `[{"id":1,"full_name":"group/sub/private","visibility":"private","description":"<script>alert(1)</script>","html_url":"https://github.example/group/sub/private","clone_url":"https://forge-synthetic@github.example/group/sub/private"}]`)
		}
	}))
	defer forge.Close()
	session, err := github.Provider{}.Open(context.Background(), forgeauth.Connection{BaseURL: forge.URL}, forgeauth.StaticToken("forge-synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	// A second source always succeeds. A failure in the first source must
	// never make the second source look like a complete listing.
	otherForge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode.Load() == 1 {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, `[{"id":2,"full_name":"group/sub/private","visibility":"public","html_url":"https://other.example/group/sub/private"}]`)
	}))
	defer otherForge.Close()
	otherSession, err := github.Provider{}.Open(context.Background(), forgeauth.Connection{BaseURL: otherForge.URL}, forgeauth.StaticToken("other-forge-synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := repositories.New(repositories.Source{ID: "personal", Session: session}, repositories.Source{ID: "work", Session: otherSession})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := repoapi.New(client, forgeauth.StaticToken("backend-synthetic"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	repopb.RegisterRepositoriesServer(g, backend)
	go func() { _ = g.Serve(l) }()
	defer g.Stop()
	t.Setenv("MANAGEMENT_TEST_BACKEND", "backend-synthetic")
	s, err := parse([]string{"--token-file=" + writeToken(t, "operator-synthetic"), "--http-listen=127.0.0.1:0", "--grpc-listen=127.0.0.1:0", "--repositories-address=" + l.Addr().String(), "--repositories-token-env=MANAGEMENT_TEST_BACKEND", "--repositories-insecure=true"}, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	a := startManagement(t, s)
	for _, tc := range []struct {
		name    string
		mode    int32
		code    int
		message string
	}{
		{"listing", 0, 200, "Complete repository listing"}, {"empty", 1, 200, "No repositories found"},
		{"upstream failure", 2, 503, "Repository listing failed"}, {"upstream credential rejection", 3, 503, "Repository listing failed"}, {"upstream access denial", 4, 503, "Repository listing failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode.Store(tc.mode)
			code, body := httpGet(t, a[1], "/v1/repositories", false)
			if code != tc.code {
				t.Fatalf("REST: %d %s", code, body)
			}
			for _, secret := range []string{"forge-synthetic", "backend-synthetic", "operator-synthetic"} {
				if strings.Contains(body, secret) {
					t.Fatal("credential leaked in REST")
				}
			}
			if code == 200 {
				var result pb.ListRepositoriesResponse
				if err := protojson.Unmarshal([]byte(body), &result); err != nil {
					t.Fatal(err)
				}
				if !result.Configured {
					t.Fatal("configured backend reported absent")
				}
				if tc.mode == 0 && (len(result.Repositories) != 2 || result.Repositories[0].SourceId != "personal" || result.Repositories[1].SourceId != "work" || result.Repositories[0].Path != result.Repositories[1].Path || result.Repositories[0].Visibility != "private") {
					t.Fatal(&result)
				}
				if tc.mode == 1 && len(result.Repositories) != 0 {
					t.Fatal(&result)
				}
			} else if strings.Contains(body, "group/sub/private") {
				t.Fatal("partial listing leaked")
			}
			code, body = httpGet(t, a[1], "/", true)
			if code != tc.code || !strings.Contains(body, tc.message) || strings.Contains(body, "<script>") {
				t.Fatalf("web: %d %s", code, body)
			}
			for _, secret := range []string{"forge-synthetic", "backend-synthetic", "operator-synthetic"} {
				if strings.Contains(body, secret) {
					t.Fatal("credential leaked in HTML")
				}
			}
		})
	}
	before := calls.Load()
	t.Setenv("MANAGEMENT_TEST_BACKEND", "rejected-backend-synthetic")
	if code, _ := httpGet(t, a[1], "/v1/repositories", false); code != 503 {
		t.Fatal("backend rejection was not unavailable")
	}
	if calls.Load() != before {
		t.Fatal("invalid backend credential reached forge")
	}
	g.Stop()
	t.Setenv("MANAGEMENT_TEST_BACKEND", "backend-synthetic")
	if code, body := httpGet(t, a[1], "/", true); code != 503 || !strings.Contains(body, "Repository listing failed") {
		t.Fatal("disconnected backend reported success")
	}
}
