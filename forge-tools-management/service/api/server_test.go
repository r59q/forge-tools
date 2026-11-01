package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-repositories/library/repositories"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

const operatorToken = "operator-synthetic-token"

type readerFunc func(context.Context, repositories.ListOptions) ([]repositories.Repository, error)

func (f readerFunc) List(ctx context.Context, o repositories.ListOptions) ([]repositories.Repository, error) {
	return f(ctx, o)
}

func setup(t *testing.T, reader management.RepositoryReader, timeout time.Duration) (*Server, http.Handler, pb.ManagementClient) {
	t.Helper()
	return setupConfig(t, management.Config{Repositories: reader}, timeout)
}

func setupConfig(t *testing.T, config management.Config, timeout time.Duration) (*Server, http.Handler, pb.ManagementClient) {
	t.Helper()
	s, err := New(management.New(config), forgeauth.StaticToken(operatorToken), timeout)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.HTTP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	l := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	pb.RegisterManagementServer(g, s)
	go func() { _ = g.Serve(l) }()
	conn, err := grpc.NewClient("passthrough:///management", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return l.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); g.Stop(); _ = l.Close() })
	return s, h, pb.NewManagementClient(conn)
}

func rpcContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+operatorToken)), cancel
}

func request(h http.Handler, path, auth string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if path == "/" || path == "/style.css" {
		r.SetBasicAuth("operator", auth)
	} else {
		r.Header.Set("Authorization", "Bearer "+auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestListingStates(t *testing.T) {
	repos := []repositories.Repository{
		{Source: repositories.SourceInfo{ID: "personal", Kind: forgeauth.GitHub, Host: "github.com"}, Path: "group/sub/repo", Visibility: repositories.Private},
		{Source: repositories.SourceInfo{ID: "work", Kind: forgeauth.GitLab, Host: "gitlab.example"}, Path: "group/sub/repo", Visibility: repositories.Internal},
	}
	for _, tc := range []struct {
		name       string
		reader     management.RepositoryReader
		configured bool
		count      int
		code       codes.Code
		httpCode   int
		message    string
	}{
		{name: "unconfigured", httpCode: 200, message: "No repository capability configured"},
		{name: "empty", reader: readerFunc(func(context.Context, repositories.ListOptions) ([]repositories.Repository, error) { return nil, nil }), configured: true, httpCode: 200, message: "No repositories found"},
		{name: "repositories", reader: readerFunc(func(context.Context, repositories.ListOptions) ([]repositories.Repository, error) { return repos, nil }), configured: true, count: 2, httpCode: 200, message: "Complete repository listing"},
		{name: "failure", reader: readerFunc(func(context.Context, repositories.ListOptions) ([]repositories.Repository, error) {
			return repos, errors.New("backend-secret-sentinel")
		}), code: codes.Unavailable, httpCode: 503, message: "Repository listing failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, h, client := setup(t, tc.reader, time.Second)
			ctx, cancel := rpcContext()
			defer cancel()
			got, err := client.ListRepositories(ctx, &emptypb.Empty{})
			if status.Code(err) != tc.code {
				t.Fatalf("gRPC: %v", err)
			}
			w := request(h, "/v1/repositories", operatorToken)
			if w.Code != tc.httpCode {
				t.Fatalf("REST: %d %s", w.Code, w.Body)
			}
			if err == nil {
				var rest pb.ListRepositoriesResponse
				if err := protojson.Unmarshal(w.Body.Bytes(), &rest); err != nil {
					t.Fatal(err)
				}
				if !proto.Equal(got, &rest) || got.Configured != tc.configured || len(got.Repositories) != tc.count {
					t.Fatalf("transport mismatch: %v / %v", got, &rest)
				}
				if tc.count == 2 && (got.Repositories[0].SourceId != "personal" || got.Repositories[1].SourceId != "work" || got.Repositories[1].Path != "group/sub/repo" || got.Repositories[0].Visibility != "private") {
					t.Fatal(got)
				}
			} else if strings.Contains(w.Body.String(), "group/sub/repo") || strings.Contains(w.Body.String(), "sentinel") {
				t.Fatal("failure exposed partial results or error details")
			}
			web := request(h, "/", operatorToken)
			if web.Code != tc.httpCode || !strings.Contains(web.Body.String(), tc.message) || strings.Contains(web.Body.String(), "sentinel") {
				t.Fatalf("web: %d %s", web.Code, web.Body)
			}
			if tc.code != codes.OK && strings.Contains(web.Body.String(), "group/sub/repo") {
				t.Fatal("web exposed partial results")
			}
			for _, response := range []*httptest.ResponseRecorder{w, web} {
				if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatal("missing protective headers")
				}
			}
		})
	}
}

func TestAccessProtection(t *testing.T) {
	var calls atomic.Int32
	_, h, client := setup(t, readerFunc(func(context.Context, repositories.ListOptions) ([]repositories.Repository, error) {
		calls.Add(1)
		return nil, nil
	}), time.Second)
	for _, token := range []string{"", "wrong", "backend-secret"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
		_, err := client.ListRepositories(ctx, &emptypb.Empty{})
		cancel()
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("gRPC accepted %q: %v", token, err)
		}
		for _, path := range []string{"/", "/style.css", "/v1/repositories"} {
			w := request(h, path, token)
			if w.Code != 401 {
				t.Fatalf("%s accepted %q: %d", path, token, w.Code)
			}
			if path == "/" && w.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("no browser authentication challenge")
			}
		}
	}
	for _, path := range []string{"/", "/v1/repositories?token=" + operatorToken} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Grpc-Metadata-Authorization", "Bearer "+operatorToken)
		r.Header.Set("X-Forwarded-User", "operator")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("trusted injected credentials")
		}
	}
	ctx, cancel := rpcContext()
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+operatorToken)
	if _, err := client.ListRepositories(ctx, nil); status.Code(err) != codes.Unauthenticated {
		t.Fatal("accepted duplicate credentials", err)
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized request reached the backend")
	}
	if w := request(h, "/v1/repositories", operatorToken); w.Code != 200 || calls.Load() != 1 {
		t.Fatal("operator cannot list")
	}
}

func TestSafeRendering(t *testing.T) {
	const attack = `<img src=x onerror="alert(1)">`
	_, h, _ := setup(t, readerFunc(func(context.Context, repositories.ListOptions) ([]repositories.Repository, error) {
		return []repositories.Repository{{Source: repositories.SourceInfo{ID: attack, Kind: forgeauth.Kind(attack), Host: attack}, Path: attack, Description: attack, Visibility: repositories.Visibility(attack), WebURL: "javascript:alert(1)", CloneURL: "https://user:backend-secret@example.com"}}, nil
	}), time.Second)
	w := request(h, "/", operatorToken)
	if w.Code != 200 || strings.Contains(w.Body.String(), attack) || !strings.Contains(w.Body.String(), "&lt;img") || strings.Contains(w.Body.String(), "backend-secret") || strings.Contains(w.Body.String(), "javascript:") || strings.Contains(w.Body.String(), operatorToken) {
		t.Fatalf("unsafe web content: %s", w.Body)
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("missing CSP")
	}
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		for _, path := range []string{"/", "/v1/repositories"} {
			r := httptest.NewRequest(method, path, nil)
			if path == "/" {
				r.SetBasicAuth("operator", operatorToken)
			} else {
				r.Header.Set("Authorization", "Bearer "+operatorToken)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 405 {
				t.Fatalf("%s %s = %d", method, path, w.Code)
			}
		}
	}
}

func TestDeadlineAndTokenRotation(t *testing.T) {
	s, _, client := setup(t, readerFunc(func(ctx context.Context, _ repositories.ListOptions) ([]repositories.Repository, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}), 10*time.Millisecond)
	ctx, cancel := rpcContext()
	defer cancel()
	if _, err := client.ListRepositories(ctx, nil); status.Code(err) != codes.DeadlineExceeded {
		t.Fatal(err)
	}
	t.Setenv("MANAGEMENT_TEST_OPERATOR", operatorToken)
	s.token = forgeauth.EnvToken("MANAGEMENT_TEST_OPERATOR")
	h, err := s.HTTP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MANAGEMENT_TEST_OPERATOR", "rotated")
	if w := request(h, "/", operatorToken); w.Code != 401 {
		t.Fatal("accepted old operator token")
	}
	if w := request(h, "/style.css", "rotated"); w.Code != 200 {
		t.Fatal("rejected rotated operator token")
	}
	t.Setenv("MANAGEMENT_TEST_OPERATOR", "")
	if w := request(h, "/style.css", "rotated"); w.Code != 401 {
		t.Fatal("missing token failed open")
	}
}

func TestInvalidServer(t *testing.T) {
	if _, err := New(nil, forgeauth.StaticToken(operatorToken), time.Second); err == nil {
		t.Fatal("nil client accepted")
	}
	if _, err := New(management.New(management.Config{}), nil, time.Second); err == nil {
		t.Fatal("nil credentials accepted")
	}
	if _, err := New(management.New(management.Config{}), forgeauth.StaticToken(operatorToken), 0); err == nil {
		t.Fatal("invalid timeout accepted")
	}
}
