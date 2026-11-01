package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	pb "github.com/r59q/forge-tools/forge-connector/library/brokerpb"
	"github.com/r59q/forge-tools/forge-connector/library/connector"
	"github.com/r59q/forge-tools/forge-connector/proxy/service"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	callerToken = "caller-secret"
	proxyToken  = "proxy-secret"
	forgeToken  = "forge-secret"
	connID      = "conn1"
)

// fakeBroker is a broker gRPC server built from brokerpb.
type fakeBroker struct {
	pb.UnimplementedBrokerServer
	baseURL string

	mu         sync.Mutex
	forgeToken string
	issues     int
	verifies   int
}

func bearerOf(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("authorization"); len(v) == 1 {
		return strings.TrimPrefix(v[0], "Bearer ")
	}
	return ""
}

func (b *fakeBroker) IssueToken(ctx context.Context, r *pb.IssueTokenRequest) (*pb.IssueTokenResponse, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.issues++
	switch {
	case bearerOf(ctx) != proxyToken:
		return nil, status.Error(codes.Unauthenticated, "no")
	case r.GetConnectionId() != connID:
		return nil, status.Error(codes.PermissionDenied, "no")
	}
	return &pb.IssueTokenResponse{Kind: "github", BaseUrl: b.baseURL, Token: b.forgeToken}, nil
}

func (b *fakeBroker) VerifyClient(ctx context.Context, r *pb.VerifyClientRequest) (*pb.Client, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.verifies++
	switch {
	case bearerOf(ctx) != callerToken:
		return nil, status.Error(codes.Unauthenticated, "no")
	case r.GetConnectionId() != connID:
		return nil, status.Error(codes.PermissionDenied, "no")
	}
	return &pb.Client{Id: "client", ConnectionIds: []string{connID}}, nil
}

func (b *fakeBroker) ListClientConnections(ctx context.Context, _ *emptypb.Empty) (*pb.ListClientConnectionsResponse, error) {
	if bearerOf(ctx) != callerToken {
		return nil, status.Error(codes.Unauthenticated, "no")
	}
	return &pb.ListClientConnectionsResponse{Connections: []*pb.ClientConnection{{Id: connID, Name: "me", Kind: "github", Status: "ok"}}}, nil
}

func (b *fakeBroker) set(f func()) { b.mu.Lock(); f(); b.mu.Unlock() }
func (b *fakeBroker) counts() (issues, verifies int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.issues, b.verifies
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	broker  *fakeBroker
	grpc    *grpc.Server
	forge   *httptest.Server
	proxy   *httptest.Server
	adapter *service.Broker
	clock   *clock
	logs    *bytes.Buffer
	// forgeHits records the Authorization header of each forge request.
	forgeHits  atomic.Int32
	forgeAuths sync.Map
	forgeFunc  atomic.Value // func(http.ResponseWriter, *http.Request)
	grpcAddr   string
}

type options struct {
	tokenTTL, verifyTTL time.Duration
}

func newEnv(t *testing.T, o options) *env {
	t.Helper()
	e := &env{clock: &clock{t: time.Unix(1_700_000_000, 0)}, logs: &bytes.Buffer{}}
	e.forge = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := e.forgeHits.Add(1)
		e.forgeAuths.Store(int(n), r.Header.Get("Authorization"))
		if f, _ := e.forgeFunc.Load().(func(http.ResponseWriter, *http.Request)); f != nil {
			f(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"login":"octo"}`)
	}))
	t.Cleanup(e.forge.Close)

	e.broker = &fakeBroker{baseURL: e.forge.URL, forgeToken: forgeToken}
	e.serveBroker(t, "127.0.0.1:0")
	return e.finish(t, o, e.grpcAddr)
}

func (e *env) serveBroker(t *testing.T, addr string) {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	e.grpcAddr = l.Addr().String()
	e.grpc = grpc.NewServer()
	pb.RegisterBrokerServer(e.grpc, e.broker)
	go e.grpc.Serve(l)
	t.Cleanup(e.grpc.Stop)
}

func (e *env) finish(t *testing.T, o options, addr string) *env {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	logger := slog.New(slog.NewTextHandler(e.logs, nil))
	e.adapter, err = service.NewBroker(service.BrokerConfig{
		Client: pb.NewBrokerClient(conn), Credentials: forgeauth.StaticToken(proxyToken),
		TokenTTL: o.tokenTTL, VerifyTTL: o.verifyTTL, Timeout: 2 * time.Second,
		Logger: logger, Now: e.clock.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := service.New(service.Config{Broker: e.adapter, Registry: registry(t), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	e.proxy = httptest.NewServer(server)
	t.Cleanup(e.proxy.Close)
	return e
}

func registry(t *testing.T) *forgeauth.Registry {
	t.Helper()
	var reg forgeauth.Registry
	for _, p := range []forgeauth.Provider{github.Provider{}, gitlab.Provider{}, forgejo.Provider{}} {
		if err := reg.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	return &reg
}

// session opens a forgeauth session pointed at the proxy.
func (e *env) session(t *testing.T, token string) *forgeauth.Session {
	t.Helper()
	s, err := registry(t).Open(context.Background(), forgeauth.Connection{
		Kind: forgeauth.GitHub, BaseURL: e.proxy.URL + "/c/" + connID,
	}, forgeauth.StaticToken(token))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *env) get(t *testing.T, token, path string) (int, string) {
	t.Helper()
	s := e.session(t, token)
	req, err := s.NewRequest(context.Background(), http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (e *env) forgeAuth(n int) string {
	v, _ := e.forgeAuths.Load(n)
	s, _ := v.(string)
	return s
}

func TestFullRequest(t *testing.T) {
	e := newEnv(t, options{tokenTTL: time.Minute, verifyTTL: time.Minute})
	s := e.session(t, callerToken)
	var got struct{ Login string }
	if err := s.GetJSON(context.Background(), "/user", &got); err != nil {
		t.Fatal(err)
	}
	if got.Login != "octo" {
		t.Errorf("login = %q", got.Login)
	}
	if auth := e.forgeAuth(1); auth != "Bearer "+forgeToken {
		t.Errorf("forge saw Authorization %q", auth)
	}
	if issues, verifies := e.broker.counts(); issues != 1 || verifies != 1 {
		t.Errorf("broker calls: issue=%d verify=%d", issues, verifies)
	}
}

func TestConnectorClient(t *testing.T) {
	e := newEnv(t, options{tokenTTL: time.Minute, verifyTTL: time.Minute})
	c, err := connector.New(connector.Config{URL: e.proxy.URL, Credentials: forgeauth.StaticToken(callerToken)})
	if err != nil {
		t.Fatal(err)
	}
	conns, err := c.Connections(context.Background())
	if err != nil || len(conns) != 1 || conns[0].ID != connID || conns[0].Name != "me" || conns[0].Kind != forgeauth.GitHub {
		t.Fatalf("Connections = %+v, %v", conns, err)
	}
	var got struct{ Login string }
	if err := conns[0].Session.GetJSON(context.Background(), "/user", &got); err != nil || got.Login != "octo" {
		t.Fatalf("session request: %v %q", err, got.Login)
	}
	c, _ = connector.New(connector.Config{URL: e.proxy.URL, Credentials: forgeauth.StaticToken(proxyToken)})
	if _, err := c.Connections(context.Background()); !errors.Is(err, forgehttp.ErrUnauthorized) {
		t.Fatalf("proxy token listed connections: %v", err)
	}
}

func TestRejectedCallersNeverReachForge(t *testing.T) {
	e := newEnv(t, options{tokenTTL: time.Minute, verifyTTL: time.Minute})
	if code, body := e.get(t, "wrong", "/user"); code != http.StatusUnauthorized || !strings.HasPrefix(body, "proxy:") {
		t.Errorf("bad token: %d %q", code, body)
	}
	resp, err := http.Get(e.proxy.URL + "/c/other/user")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown connection = %d", resp.StatusCode)
	}
	if e.forgeHits.Load() != 0 {
		t.Error("forge was contacted")
	}
}

func TestTokenCache(t *testing.T) {
	e := newEnv(t, options{tokenTTL: time.Minute, verifyTTL: time.Hour})
	for range 3 {
		if code, _ := e.get(t, callerToken, "/user"); code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
	}
	if issues, _ := e.broker.counts(); issues != 1 {
		t.Errorf("issues within TTL = %d, want 1", issues)
	}
	// A rotated token is picked up once the cache entry expires.
	e.broker.set(func() { e.broker.forgeToken = "rotated" })
	e.clock.advance(time.Minute + time.Second)
	e.get(t, callerToken, "/user")
	if issues, _ := e.broker.counts(); issues != 2 {
		t.Errorf("issues after TTL = %d, want 2", issues)
	}
	if auth := e.forgeAuth(4); auth != "Bearer rotated" {
		t.Errorf("forge saw %q after expiry", auth)
	}
}

func TestTokenCacheDisabled(t *testing.T) {
	e := newEnv(t, options{})
	e.get(t, callerToken, "/user")
	e.get(t, callerToken, "/user")
	// Connection and IssueToken each ask the broker without a cache.
	if issues, verifies := e.broker.counts(); issues != 4 || verifies != 2 {
		t.Errorf("issues=%d verifies=%d", issues, verifies)
	}
}

func TestDropsTokenOn401(t *testing.T) {
	e := newEnv(t, options{tokenTTL: time.Hour, verifyTTL: time.Hour})
	e.broker.set(func() { e.broker.forgeToken = "stale" })
	e.forgeFunc.Store(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer stale" {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"login":"octo"}`)
	})
	if code, body := e.get(t, callerToken, "/user"); code != http.StatusUnauthorized || !strings.Contains(body, "bad credentials") {
		t.Fatalf("first request: %d %q", code, body)
	}
	if hits := e.forgeHits.Load(); hits != 1 {
		t.Fatalf("forge hits = %d, the request must not be retried", hits)
	}
	e.broker.set(func() { e.broker.forgeToken = "fresh" })
	if code, _ := e.get(t, callerToken, "/user"); code != http.StatusOK {
		t.Fatalf("second request = %d", code)
	}
	if auth := e.forgeAuth(2); auth != "Bearer fresh" {
		t.Errorf("second request used %q", auth)
	}
	if issues, _ := e.broker.counts(); issues != 2 {
		t.Errorf("issues = %d, want 2", issues)
	}
}

func TestProxyRejectionDoesNotDropToken(t *testing.T) {
	e := newEnv(t, options{tokenTTL: time.Hour, verifyTTL: time.Hour})
	e.get(t, callerToken, "/user")
	e.get(t, "wrong", "/user") // 401 from the proxy itself
	e.get(t, callerToken, "/user")
	if issues, _ := e.broker.counts(); issues != 1 {
		t.Errorf("issues = %d, want 1", issues)
	}
}

func TestVerifyCache(t *testing.T) {
	e := newEnv(t, options{tokenTTL: time.Hour, verifyTTL: 30 * time.Second})
	for range 3 {
		e.get(t, callerToken, "/user")
	}
	if _, verifies := e.broker.counts(); verifies != 1 {
		t.Errorf("verifies within TTL = %d, want 1", verifies)
	}
	e.clock.advance(31 * time.Second)
	e.get(t, callerToken, "/user")
	if _, verifies := e.broker.counts(); verifies != 2 {
		t.Errorf("verifies after TTL = %d, want 2", verifies)
	}
}

func TestDenialsAreCachedBriefly(t *testing.T) {
	ctx := context.Background()
	t.Run("never longer than an approval", func(t *testing.T) {
		e := newEnv(t, options{tokenTTL: time.Hour, verifyTTL: time.Hour})
		for range 3 {
			if _, err := e.adapter.VerifyClient(ctx, "wrong", connID); !errors.Is(err, broker.ErrUnauthorized) {
				t.Fatalf("err = %v", err)
			}
		}
		if _, v := e.broker.counts(); v != 1 {
			t.Errorf("verifies = %d, want 1", v)
		}
		e.clock.advance(6 * time.Second)
		e.adapter.VerifyClient(ctx, "wrong", connID)
		if _, v := e.broker.counts(); v != 2 {
			t.Errorf("denial outlived its cap: verifies = %d", v)
		}
		// An approval lives much longer than that.
		e.adapter.VerifyClient(ctx, callerToken, connID)
		e.clock.advance(6 * time.Second)
		e.adapter.VerifyClient(ctx, callerToken, connID)
		if _, v := e.broker.counts(); v != 3 {
			t.Errorf("approval expired early: verifies = %d", v)
		}
	})
	t.Run("capped by a short approval", func(t *testing.T) {
		e := newEnv(t, options{verifyTTL: time.Second})
		e.adapter.VerifyClient(ctx, "wrong", connID)
		e.clock.advance(2 * time.Second)
		e.adapter.VerifyClient(ctx, "wrong", connID)
		if _, v := e.broker.counts(); v != 2 {
			t.Errorf("verifies = %d, want 2", v)
		}
	})
	t.Run("forbidden is cached too", func(t *testing.T) {
		e := newEnv(t, options{verifyTTL: time.Hour})
		for range 2 {
			if _, err := e.adapter.VerifyClient(ctx, callerToken, "other"); !errors.Is(err, broker.ErrForbidden) {
				t.Fatalf("err = %v", err)
			}
		}
		if _, v := e.broker.counts(); v != 1 {
			t.Errorf("verifies = %d, want 1", v)
		}
	})
}

func TestBrokerDown(t *testing.T) {
	e := newEnv(t, options{tokenTTL: time.Minute, verifyTTL: time.Minute})
	e.grpc.Stop()
	code, body := e.get(t, callerToken, "/user")
	if code != http.StatusServiceUnavailable || !strings.HasPrefix(body, "proxy:") {
		t.Errorf("broker down: %d %q", code, body)
	}
	if e.forgeHits.Load() != 0 {
		t.Error("forge was contacted without authorization")
	}
	// Failures are not cached: the proxy recovers when the broker returns.
	e.serveBrokerAgain(t)
	if code, _ := e.get(t, callerToken, "/user"); code != http.StatusOK {
		t.Errorf("after recovery: %d", code)
	}
}

// serveBrokerAgain restarts the fake broker on its previous address.
func (e *env) serveBrokerAgain(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l, err := net.Listen("tcp", e.grpcAddr)
		if err == nil {
			e.grpc = grpc.NewServer()
			pb.RegisterBrokerServer(e.grpc, e.broker)
			go e.grpc.Serve(l)
			t.Cleanup(e.grpc.Stop)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// gRPC reconnects with backoff; allow it some time.
	for i := 0; i < 100; i++ {
		if _, err := e.adapter.VerifyClient(context.Background(), callerToken, connID); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestBrokerDeadline(t *testing.T) {
	e := newEnv(t, options{})
	block := make(chan struct{})
	defer close(block)
	// A broker that never answers yields a gateway timeout.
	slow := grpc.NewServer()
	pb.RegisterBrokerServer(slow, blockingBroker{block: block})
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go slow.Serve(l)
	defer slow.Stop()
	conn, _ := grpc.NewClient(l.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer conn.Close()
	b, err := service.NewBroker(service.BrokerConfig{
		Client: pb.NewBrokerClient(conn), Credentials: forgeauth.StaticToken(proxyToken), Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.IssueToken(context.Background(), connID); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	_ = e
}

type blockingBroker struct {
	pb.UnimplementedBrokerServer
	block chan struct{}
}

func (b blockingBroker) IssueToken(ctx context.Context, _ *pb.IssueTokenRequest) (*pb.IssueTokenResponse, error) {
	select {
	case <-ctx.Done():
	case <-b.block:
	}
	return nil, status.Error(codes.Unavailable, "x")
}

func TestProxyTokenMisconfigured(t *testing.T) {
	e := newEnv(t, options{})
	conn, err := grpc.NewClient(e.grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	b, _ := service.NewBroker(service.BrokerConfig{Client: pb.NewBrokerClient(conn), Credentials: forgeauth.StaticToken("wrong")})
	// The broker rejecting the proxy's own token is not the caller's fault.
	if _, err := b.IssueToken(context.Background(), connID); !errors.Is(err, broker.ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
	if _, err := b.Connection(context.Background(), "other"); !errors.Is(err, broker.ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
}

func TestOutOfScopeConnectionIsNotFound(t *testing.T) {
	e := newEnv(t, options{})
	if _, err := e.adapter.Connection(context.Background(), "other"); !errors.Is(err, broker.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestPaginationEndToEnd(t *testing.T) {
	e := newEnv(t, options{tokenTTL: time.Minute, verifyTTL: time.Minute})
	const pages = 3
	e.forgeFunc.Store(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		if page < pages {
			w.Header().Add("Link", fmt.Sprintf(`<%s/repos?page=%d>; rel="next"`, e.forge.URL, page+1))
		}
		json.NewEncoder(w).Encode([]string{fmt.Sprintf("repo-%d", page)})
	})
	s := e.session(t, callerToken)
	var all []string
	next := "/repos"
	for next != "" {
		req, err := s.NewRequest(context.Background(), http.MethodGet, next, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := s.Client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var items []string
		if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		all = append(all, items...)
		next, _ = forgehttp.NextPage(resp.Header)
		if next != "" && !strings.HasPrefix(next, e.proxy.URL+"/c/"+connID+"/") {
			t.Fatalf("next link %q does not point at the proxy", next)
		}
	}
	if strings.Join(all, ",") != "repo-1,repo-2,repo-3" {
		t.Errorf("items = %v", all)
	}
	for i := 1; i <= pages; i++ {
		if auth := e.forgeAuth(i); auth != "Bearer "+forgeToken {
			t.Errorf("page %d: forge saw %q", i, auth)
		}
	}
	if issues, verifies := e.broker.counts(); issues != 1 || verifies != 1 {
		t.Errorf("broker calls: issue=%d verify=%d", issues, verifies)
	}
}

func TestLogsAndSecrets(t *testing.T) {
	e := newEnv(t, options{tokenTTL: time.Minute, verifyTTL: time.Minute})
	e.get(t, callerToken, "/user?private_token=query-secret")
	e.get(t, "wrong", "/user")
	logs := e.logs.String()
	for _, secret := range []string{callerToken, proxyToken, forgeToken, "query-secret"} {
		if strings.Contains(logs, secret) {
			t.Errorf("logs contain %q:\n%s", secret, logs)
		}
	}
	for _, want := range []string{"connection=conn1", "method=GET", "path=/c/conn1/user", "status=200", "status=401", "duration="} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
}

func TestHealthz(t *testing.T) {
	e := newEnv(t, options{})
	resp, err := http.Get(e.proxy.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" {
		t.Errorf("healthz = %d %q", resp.StatusCode, body)
	}
	resp, err = http.Post(e.proxy.URL+"/healthz", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST healthz = %d", resp.StatusCode)
	}
	if e.forgeHits.Load() != 0 {
		t.Error("health check reached the forge")
	}
}

func TestHealthzWhileStopping(t *testing.T) {
	srv, err := service.New(service.Config{Broker: nil})
	if err == nil || srv != nil {
		t.Fatal("New without a broker must fail")
	}
	e := newEnv(t, options{})
	server, _ := service.New(service.Config{Broker: e.adapter, Registry: registry(t)})
	server.SetStopping()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("healthz = %d", rec.Code)
	}
}

func TestInvalidBrokerConfig(t *testing.T) {
	if _, err := service.NewBroker(service.BrokerConfig{}); !errors.Is(err, broker.ErrInvalid) {
		t.Errorf("err = %v", err)
	}
}
