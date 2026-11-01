package broker_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

const (
	oldToken forgeauth.StaticToken = "forge-secret-before"
	newToken forgeauth.StaticToken = "forge-secret-after"
)

type fixture struct {
	ctx      context.Context
	store    *broker.MemoryStore
	sealer   *broker.AESGCMSealer
	registry *forgeauth.Registry
	manager  *broker.Manager
	forge    broker.Forge
	status   atomic.Int32
	echo     atomic.Bool
	requests atomic.Int32
}

func setup(t *testing.T, provider forgeauth.Provider) *fixture {
	t.Helper()
	f := &fixture{ctx: context.Background(), store: &broker.MemoryStore{}, registry: &forgeauth.Registry{}}
	f.status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/user" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		token, ok := provider.TokenFromHeader(r.Header)
		status := int(f.status.Load())
		if !ok || (token != string(oldToken) && token != string(newToken)) {
			status = http.StatusUnauthorized
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != http.StatusOK {
			// Deliberately echo the token: the broker must discard forge messages.
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "rejected " + token})
			return
		}
		name := "First identity"
		if token == string(newToken) {
			name = "Rotated identity"
		}
		if f.echo.Load() {
			name = "echo " + token
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 42, "login": "owner", "username": "owner", "name": name, "full_name": name, "email": "owner@example.com",
		})
	}))
	t.Cleanup(srv.Close)
	f.forge = broker.Forge{ID: "forge", Kind: provider.Kind(), BaseURL: srv.URL, Name: "Test forge"}
	if err := f.store.CreateForge(f.ctx, f.forge); err != nil {
		t.Fatal(err)
	}
	if err := f.registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	var err error
	f.sealer, err = broker.NewAESGCMSealer(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f.manager, err = broker.NewManager(f.store, f.sealer, f.registry)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) connection(t *testing.T) broker.Connection {
	t.Helper()
	conn, err := f.manager.CreateConnection(f.ctx, f.forge.ID, "Connection", oldToken)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func (f *fixture) client(t *testing.T, scope []broker.ID, issue bool) (broker.Client, forgeauth.StaticToken) {
	t.Helper()
	client, token, err := f.manager.CreateClient(f.ctx, "Service", scope, issue)
	if err != nil {
		t.Fatal(err)
	}
	if client.ID == "" || token == "" || client.TokenHash != [32]byte{} {
		t.Fatal("CreateClient returned incomplete metadata or a hash")
	}
	stored, err := f.store.GetClient(f.ctx, client.ID)
	if err != nil || stored.TokenHash != sha256.Sum256([]byte(token)) {
		t.Fatal("client token was not hashed correctly")
	}
	return client, token
}

func TestConnectionLifecycle(t *testing.T) {
	for _, provider := range []forgeauth.Provider{github.Provider{}, gitlab.Provider{}, forgejo.Provider{}} {
		t.Run(string(provider.Kind()), func(t *testing.T) {
			f := setup(t, provider)
			conn := f.connection(t)
			if conn.ID == "" || conn.ForgeID != f.forge.ID || conn.Status != broker.StatusOK || conn.Identity.ID != "42" || conn.Identity.Login != "owner" || conn.CreatedAt.IsZero() || conn.CheckedAt != conn.CreatedAt {
				t.Fatalf("CreateConnection = %+v", conn)
			}
			sealed, err := f.store.GetCredential(f.ctx, conn.ID)
			if err != nil || bytes.Contains(sealed, []byte(oldToken)) {
				t.Fatal("stored credential is not sealed")
			}
			tested, err := f.manager.TestConnection(f.ctx, conn.ID)
			if err != nil || tested.Status != broker.StatusOK || tested.CheckedAt.Before(conn.CheckedAt) || tested.CreatedAt != conn.CreatedAt {
				t.Fatalf("TestConnection = %+v, %v", tested, err)
			}
			_, clientToken := f.client(t, []broker.ID{conn.ID}, true)
			issued, err := f.manager.IssueToken(f.ctx, clientToken, conn.ID)
			if err != nil || issued.Kind != provider.Kind() || issued.BaseURL != f.forge.BaseURL || issued.Token != oldToken {
				t.Fatalf("IssueToken failed: %v", err)
			}
			rotated, err := f.manager.RotateCredential(f.ctx, conn.ID, newToken)
			if err != nil || rotated.Status != broker.StatusOK || rotated.Identity.Name != "Rotated identity" || rotated.CreatedAt != conn.CreatedAt || rotated.CheckedAt.Before(tested.CheckedAt) {
				t.Fatalf("RotateCredential = %+v, %v", rotated, err)
			}
			issued, err = f.manager.IssueToken(f.ctx, clientToken, conn.ID)
			if err != nil || issued.Token != newToken {
				t.Fatal("IssueToken did not return replacement")
			}
			if err := f.manager.DeleteConnection(f.ctx, conn.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.GetCredential(f.ctx, conn.ID); !errors.Is(err, broker.ErrNotFound) {
				t.Fatal("delete retained credential")
			}
			if _, err := f.store.GetConnection(f.ctx, conn.ID); !errors.Is(err, broker.ErrNotFound) {
				t.Fatal("delete retained connection")
			}
			if _, err := f.manager.VerifyClient(f.ctx, clientToken, conn.ID); !errors.Is(err, broker.ErrForbidden) {
				t.Fatalf("deleted connection authorized: %v", err)
			}
			if _, err := f.manager.IssueToken(f.ctx, clientToken, conn.ID); !errors.Is(err, broker.ErrForbidden) {
				t.Fatalf("deleted credential issued: %v", err)
			}
		})
	}
}

func TestRejectInvalidCredentials(t *testing.T) {
	f := setup(t, github.Provider{})
	for _, token := range []forgeauth.StaticToken{"", "invalid-secret", "bad\nheader"} {
		conn, err := f.manager.CreateConnection(f.ctx, f.forge.ID, "Bad", token)
		if !errors.Is(err, broker.ErrInvalidCredential) || conn != (broker.Connection{}) {
			t.Fatalf("invalid credential accepted: %v", err)
		}
		assertNoSecret(t, err, string(token))
	}
	connections, err := f.store.ListConnections(f.ctx)
	if err != nil || len(connections) != 0 {
		t.Fatal("failed creation persisted a connection")
	}
	conn := f.connection(t)
	sealed, _ := f.store.GetCredential(f.ctx, conn.ID)
	if result, err := f.manager.RotateCredential(f.ctx, conn.ID, "invalid-secret"); !errors.Is(err, broker.ErrInvalidCredential) || result != (broker.Connection{}) {
		t.Fatalf("invalid rotation accepted: %v", err)
	}
	after, _ := f.store.GetConnection(f.ctx, conn.ID)
	afterSealed, _ := f.store.GetCredential(f.ctx, conn.ID)
	if after != conn || !bytes.Equal(sealed, afterSealed) {
		t.Fatal("failed rotation changed metadata or credential")
	}
}

func TestConnectionStatus(t *testing.T) {
	for _, tc := range []struct {
		code   int
		status broker.Status
		cause  error
	}{
		{401, broker.StatusRevoked, forgehttp.ErrUnauthorized},
		{403, broker.StatusRevoked, forgehttp.ErrForbidden},
		{429, broker.StatusInvalid, forgehttp.ErrRateLimited},
		{500, broker.StatusInvalid, broker.ErrInvalidCredential},
	} {
		t.Run(fmt.Sprint(tc.code), func(t *testing.T) {
			f := setup(t, github.Provider{})
			conn := f.connection(t)
			_, token := f.client(t, []broker.ID{conn.ID}, true)
			f.status.Store(int32(tc.code))
			checked, err := f.manager.TestConnection(f.ctx, conn.ID)
			if !errors.Is(err, tc.cause) || checked.Status != tc.status || checked.CheckedAt.Before(conn.CheckedAt) || checked.Identity != conn.Identity {
				t.Fatalf("TestConnection = %+v, %v", checked, err)
			}
			assertNoSecret(t, err, string(oldToken))
			stored, err := f.store.GetConnection(f.ctx, conn.ID)
			if err != nil || stored != checked {
				t.Fatal("status update not persisted")
			}
			if _, err := f.manager.VerifyClient(f.ctx, token, conn.ID); !errors.Is(err, broker.ErrUnavailable) {
				t.Fatalf("unhealthy connection verified: %v", err)
			}
			if _, err := f.manager.IssueToken(f.ctx, token, conn.ID); !errors.Is(err, broker.ErrUnavailable) {
				t.Fatalf("unhealthy token issued: %v", err)
			}
			f.status.Store(http.StatusOK)
			recovered, err := f.manager.TestConnection(f.ctx, conn.ID)
			if err != nil || recovered.Status != broker.StatusOK {
				t.Fatal("successful test did not restore status")
			}
		})
	}
}

func TestClientAuthorization(t *testing.T) {
	f := setup(t, github.Provider{})
	conn := f.connection(t)
	other := f.connection(t)
	issuer, issueToken := f.client(t, []broker.ID{conn.ID}, true)
	_, verifyToken := f.client(t, []broker.ID{conn.ID}, false)
	_, emptyToken := f.client(t, nil, true)
	if issueToken == verifyToken || issueToken == emptyToken {
		t.Fatal("client tokens are not unique")
	}
	for _, tc := range []struct {
		name   string
		token  forgeauth.StaticToken
		conn   broker.ID
		verify error
		issue  error
	}{
		{"issuer", issueToken, conn.ID, nil, nil},
		{"verify only", verifyToken, conn.ID, nil, broker.ErrForbidden},
		{"empty scope", emptyToken, conn.ID, broker.ErrForbidden, broker.ErrForbidden},
		{"wrong scope", issueToken, other.ID, broker.ErrForbidden, broker.ErrForbidden},
		{"missing connection", issueToken, "missing", broker.ErrForbidden, broker.ErrForbidden},
		{"missing token", "", conn.ID, broker.ErrUnauthorized, broker.ErrUnauthorized},
		{"unknown token", "not-a-client-secret", conn.ID, broker.ErrUnauthorized, broker.ErrUnauthorized},
		{"forge token", oldToken, conn.ID, broker.ErrUnauthorized, broker.ErrUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := f.manager.VerifyClient(f.ctx, tc.token, tc.conn)
			if !errors.Is(err, tc.verify) {
				t.Fatalf("VerifyClient error = %v, want %v", err, tc.verify)
			}
			if tc.verify == nil && (client.ID == "" || client.TokenHash != [32]byte{}) {
				t.Fatal("VerifyClient returned missing metadata or hash")
			}
			issued, err := f.manager.IssueToken(f.ctx, tc.token, tc.conn)
			if !errors.Is(err, tc.issue) {
				t.Fatalf("IssueToken error = %v, want %v", err, tc.issue)
			}
			if err != nil && issued != (broker.IssuedToken{}) {
				t.Fatal("failed issuance returned data")
			}
		})
	}
	before := f.requests.Load()
	if _, err := f.manager.VerifyClient(f.ctx, issueToken, conn.ID); err != nil || f.requests.Load() != before {
		t.Fatal("VerifyClient contacted forge")
	}
	if _, token, err := f.manager.CreateClient(f.ctx, "bad scope", []broker.ID{"missing"}, true); !errors.Is(err, broker.ErrNotFound) || token != "" {
		t.Fatal("client with missing connection created")
	}
	if err := f.manager.DeleteClient(f.ctx, issuer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.VerifyClient(f.ctx, issueToken, conn.ID); !errors.Is(err, broker.ErrUnauthorized) {
		t.Fatal("deleted client authorized")
	}
	if _, err := f.manager.IssueToken(f.ctx, issueToken, conn.ID); !errors.Is(err, broker.ErrUnauthorized) {
		t.Fatal("deleted client issued credential")
	}
}

func TestClientConnections(t *testing.T) {
	f := setup(t, gitlab.Provider{})
	a := f.connection(t)
	b := f.connection(t)
	f.connection(t) // out of scope
	_, token := f.client(t, []broker.ID{b.ID, a.ID}, false)
	f.status.Store(http.StatusUnauthorized)
	if _, err := f.manager.TestConnection(f.ctx, b.ID); err == nil {
		t.Fatal("revocation not detected")
	}
	got, err := f.manager.ClientConnections(f.ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	want := []broker.ClientConnection{
		{ID: a.ID, Name: "Connection", Kind: forgeauth.GitLab, Status: broker.StatusOK},
		{ID: b.ID, Name: "Connection", Kind: forgeauth.GitLab, Status: broker.StatusRevoked},
	}
	if a.ID > b.ID {
		want[0], want[1] = want[1], want[0]
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClientConnections = %+v, want %+v", got, want)
	}
	_, emptyToken := f.client(t, nil, false)
	if got, err := f.manager.ClientConnections(f.ctx, emptyToken); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty scope = %+v, %v", got, err)
	}
	for _, token := range []forgeauth.StaticToken{"", "unknown", oldToken} {
		if _, err := f.manager.ClientConnections(f.ctx, token); !errors.Is(err, broker.ErrUnauthorized) {
			t.Fatalf("token %q: %v", token, err)
		}
	}
	if err := f.manager.DeleteConnection(f.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := f.manager.ClientConnections(f.ctx, token); err != nil || len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("after delete = %+v, %v", got, err)
	}
}

func TestSecretsAreWriteOnly(t *testing.T) {
	f := setup(t, github.Provider{})
	f.echo.Store(true)
	conn := f.connection(t)
	checked, err := f.manager.TestConnection(f.ctx, conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := f.manager.RotateCredential(f.ctx, conn.ID, newToken)
	if err != nil {
		t.Fatal(err)
	}
	client, clientToken := f.client(t, []broker.ID{conn.ID}, true)
	verified, err := f.manager.VerifyClient(f.ctx, clientToken, conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	getConn, _ := f.store.GetConnection(f.ctx, conn.ID)
	listConn, _ := f.store.ListConnections(f.ctx)
	getClient, _ := f.store.GetClient(f.ctx, client.ID)
	listClient, _ := f.store.ListClients(f.ctx)
	forge, _ := f.store.GetForge(f.ctx, f.forge.ID)
	forges, _ := f.store.ListForges(f.ctx)
	sealed, _ := f.store.GetCredential(f.ctx, conn.ID)
	for _, value := range []any{conn, checked, rotated, client, verified, getConn, listConn, getClient, listClient, forge, forges, sealed, f.manager, f.store, f.sealer} {
		assertNoSecret(t, value, string(oldToken), string(newToken), string(clientToken))
	}
	issued, err := f.manager.IssueToken(f.ctx, clientToken, conn.ID)
	if err != nil || issued.Token != newToken {
		t.Fatal("IssueToken did not explicitly return secret")
	}
	// Explicit issuance and one-time bootstrap remain safe to format or log.
	assertFormattedNoSecret(t, issued, string(newToken))
	assertFormattedNoSecret(t, clientToken, string(clientToken))
}

func assertNoSecret(t *testing.T, value any, secrets ...string) {
	t.Helper()
	assertFormattedNoSecret(t, value, secrets...)
	var visit func(reflect.Value)
	visit = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			for _, secret := range secrets {
				if secret != "" && strings.Contains(v.String(), secret) {
					t.Fatal("API returned a secret string")
				}
			}
		case reflect.Struct:
			for i := range v.NumField() {
				if v.Type().Field(i).IsExported() {
					visit(v.Field(i))
				}
			}
		case reflect.Array, reflect.Slice:
			if v.Type().Elem().Kind() == reflect.Uint8 && v.Kind() == reflect.Slice {
				for _, secret := range secrets {
					if secret != "" && bytes.Contains(v.Bytes(), []byte(secret)) {
						t.Fatal("API returned secret bytes")
					}
				}
			}
			for i := range v.Len() {
				visit(v.Index(i))
			}
		}
	}
	visit(reflect.ValueOf(value))
}

func assertFormattedNoSecret(t *testing.T, value any, secrets ...string) {
	t.Helper()
	var output strings.Builder
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x"} {
		fmt.Fprintf(&output, format, value)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	output.Write(encoded)
	slog.New(slog.NewJSONHandler(&output, nil)).Info("value", "data", value)
	slog.New(slog.NewTextHandler(&output, nil)).Info("value", "data", value)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(output.String(), secret) {
			t.Fatal("formatted output leaked a secret")
		}
	}
}

func TestManagerMissingAndCanceled(t *testing.T) {
	f := setup(t, github.Provider{})
	for _, args := range []struct {
		store    broker.Store
		sealer   broker.Sealer
		registry *forgeauth.Registry
	}{{nil, f.sealer, f.registry}, {f.store, nil, f.registry}, {f.store, f.sealer, nil}} {
		if _, err := broker.NewManager(args.store, args.sealer, args.registry); !errors.Is(err, broker.ErrInvalid) {
			t.Fatal("missing dependency accepted")
		}
	}
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	for _, tc := range []struct {
		ctx  context.Context
		want error
	}{{f.ctx, broker.ErrNotFound}, {ctx, context.Canceled}} {
		operations := []func() error{
			func() error { _, err := f.manager.CreateConnection(tc.ctx, "missing", "", oldToken); return err },
			func() error { _, err := f.manager.TestConnection(tc.ctx, "missing"); return err },
			func() error { _, err := f.manager.RotateCredential(tc.ctx, "missing", newToken); return err },
			func() error { return f.manager.DeleteConnection(tc.ctx, "missing") },
			func() error { return f.manager.DeleteClient(tc.ctx, "missing") },
		}
		for i, op := range operations {
			if err := op(); !errors.Is(err, tc.want) {
				t.Errorf("operation %d error = %v, want %v", i, err, tc.want)
			}
		}
	}
	if _, token, err := f.manager.CreateClient(ctx, "", nil, false); !errors.Is(err, context.Canceled) || token != "" {
		t.Fatal("canceled client creation returned token")
	}
}

type failingSealer struct{ broker.Sealer }

func (failingSealer) Seal([]byte) ([]byte, error) {
	return nil, fmt.Errorf("bad secret: %s", string(oldToken))
}

type failingStore struct{ broker.Store }

func (failingStore) ListClients(context.Context) ([]broker.Client, error) {
	return nil, fmt.Errorf("bad secret: %s", string(oldToken))
}

func TestDependencyFailuresAndTampering(t *testing.T) {
	f := setup(t, github.Provider{})
	conn := f.connection(t)
	_, clientToken := f.client(t, []broker.ID{conn.ID}, true)
	badSealer, err := broker.NewManager(f.store, failingSealer{f.sealer}, f.registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badSealer.CreateConnection(f.ctx, f.forge.ID, "", oldToken); !errors.Is(err, broker.ErrSealing) {
		t.Fatal("sealing failure ignored")
	} else {
		assertNoSecret(t, err, string(oldToken))
	}
	if _, err := badSealer.RotateCredential(f.ctx, conn.ID, newToken); !errors.Is(err, broker.ErrSealing) {
		t.Fatal("rotation sealing failure ignored")
	}
	issued, err := f.manager.IssueToken(f.ctx, clientToken, conn.ID)
	if err != nil || issued.Token != oldToken {
		t.Fatal("failed sealing destroyed existing token")
	}
	badStore, err := broker.NewManager(failingStore{f.store}, f.sealer, f.registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badStore.VerifyClient(f.ctx, clientToken, conn.ID); !errors.Is(err, broker.ErrStorage) {
		t.Fatal("storage failure ignored")
	} else {
		assertNoSecret(t, err, string(oldToken))
	}
	sealed, _ := f.store.GetCredential(f.ctx, conn.ID)
	sealed[len(sealed)-1] ^= 1
	if err := f.store.UpdateConnection(f.ctx, conn, sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.VerifyClient(f.ctx, clientToken, conn.ID); err != nil {
		t.Fatal("VerifyClient tried to decrypt credentials")
	}
	if result, err := f.manager.IssueToken(f.ctx, clientToken, conn.ID); !errors.Is(err, broker.ErrSealing) || result != (broker.IssuedToken{}) {
		t.Fatal("tampered token issued")
	}
	if _, err := f.manager.TestConnection(f.ctx, conn.ID); !errors.Is(err, broker.ErrSealing) {
		t.Fatal("tampered credential checked")
	}
	// Rotation can repair an unreadable old credential.
	if _, err := f.manager.RotateCredential(f.ctx, conn.ID, newToken); err != nil {
		t.Fatal(err)
	}
}

// Ensure transport errors containing credentials cannot escape the manager.
type leakingTransport struct{}

func (leakingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New(string(oldToken))
}

func TestTransportErrorRedaction(t *testing.T) {
	// DefaultTransport is used only because built-in providers select it when
	// opening sessions. This test is deliberately not parallel.
	previous := http.DefaultTransport
	http.DefaultTransport = leakingTransport{}
	t.Cleanup(func() { http.DefaultTransport = previous })
	f := setup(t, github.Provider{})
	_, err := f.manager.CreateConnection(f.ctx, f.forge.ID, "", oldToken)
	if !errors.Is(err, broker.ErrInvalidCredential) {
		t.Fatal("transport error ignored")
	}
	assertNoSecret(t, err, string(oldToken))
}
