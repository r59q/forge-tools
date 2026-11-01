package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"
)

const forgeToken = "forge-synthetic-token"

// connector runs a real broker against a fake GitLab that accepts forgeToken
// while accept is true.
func connector(t *testing.T) (*broker.Manager, string, *atomic.Bool) {
	t.Helper()
	var accept atomic.Bool
	accept.Store(true)
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := gitlab.Provider{}.TokenFromHeader(r.Header)
		if r.URL.Path != "/api/v4/user" || token != forgeToken || !accept.Load() {
			http.Error(w, "rejected "+token, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "username": "owner"})
	}))
	t.Cleanup(forge.Close)
	registry := &forgeauth.Registry{}
	if err := registry.Register(gitlab.Provider{}); err != nil {
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
	return m, forge.URL, &accept
}

func post(h http.Handler, path string, form url.Values, header http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth("operator", operatorToken)
	for k, v := range header {
		r.Header[k] = v
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func rest(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+operatorToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestConnectionsAPI(t *testing.T) {
	m, host, accept := connector(t)
	_, h, client := setupConfig(t, management.Config{Connector: m}, 5*time.Second)
	ctx, cancel := rpcContext()
	defer cancel()

	if got, err := client.ListConnections(ctx, &emptypb.Empty{}); err != nil || !got.Configured || len(got.Connections) != 0 {
		t.Fatalf("empty list = %v, %v", got, err)
	}
	w := rest(h, "POST", "/v1/connections", `{"forge":"gitlab","host":"`+host+`","name":"work","credential":"`+forgeToken+`"}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), forgeToken) {
		t.Fatalf("REST add: %d %s", w.Code, w.Body)
	}
	var added pb.Connection
	if err := protojson.Unmarshal(w.Body.Bytes(), &added); err != nil {
		t.Fatal(err)
	}
	if added.Id == "" || added.Name != "work" || added.Forge != "gitlab" || added.BaseUrl != host+"/api/v4" || added.Login != "owner" || added.Status != "ok" || added.CheckedAt == nil {
		t.Fatalf("added = %v", &added)
	}
	if _, err := client.AddConnection(ctx, &pb.AddConnectionRequest{Forge: "gitlab", Host: host, Credential: "wrong-" + forgeToken}); status.Code(err) != codes.InvalidArgument || strings.Contains(err.Error(), forgeToken) {
		t.Fatalf("rejected add = %v", err)
	}
	if w := rest(h, "GET", "/v1/connections", ""); w.Code != 200 || !strings.Contains(w.Body.String(), added.Id) || strings.Contains(w.Body.String(), forgeToken) {
		t.Fatalf("REST list: %d %s", w.Code, w.Body)
	}
	if _, err := client.RotateCredential(ctx, &pb.RotateCredentialRequest{Id: added.Id, Credential: forgeToken}); err != nil {
		t.Fatal(err)
	}
	if w := rest(h, "POST", "/v1/connections/"+added.Id+"/test", "{}"); w.Code != 200 {
		t.Fatalf("REST test: %d", w.Code)
	}
	accept.Store(false)
	if _, err := client.TestConnection(ctx, &pb.ConnectionRequest{Id: added.Id}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("failed test = %v", err)
	}
	if _, err := client.TestConnection(ctx, &pb.ConnectionRequest{Id: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing test = %v", err)
	}
	if w := rest(h, "DELETE", "/v1/connections/"+added.Id, ""); w.Code != 200 {
		t.Fatalf("REST remove: %d", w.Code)
	}
	if w := rest(h, "DELETE", "/v1/connections/"+added.Id, ""); w.Code != 404 {
		t.Fatalf("REST remove twice: %d", w.Code)
	}

	_, unconfigured, plain := setup(t, nil, time.Second)
	if got, err := plain.ListConnections(ctx, &emptypb.Empty{}); err != nil || got.Configured {
		t.Fatalf("unconfigured list = %v, %v", got, err)
	}
	if _, err := plain.AddConnection(ctx, &pb.AddConnectionRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("unconfigured add = %v", err)
	}
	if w := request(unconfigured, "/v1/connections", "wrong"); w.Code != 401 {
		t.Fatalf("unauthenticated list: %d", w.Code)
	}
}

func TestConnectionsPage(t *testing.T) {
	m, host, accept := connector(t)
	_, h, _ := setupConfig(t, management.Config{Connector: m}, 5*time.Second)
	page := func(path string) string {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.SetBasicAuth("operator", operatorToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("GET %s: %d", path, w.Code)
		}
		return w.Body.String()
	}
	if body := page("/connections"); !strings.Contains(body, "No connections yet") || !strings.Contains(body, `aria-current="page">Connections`) {
		t.Fatal(body)
	}
	if body := page("/connections?done=<script>"); strings.Contains(body, "<script>") || strings.Contains(body, `role="status"`) {
		t.Fatal("rendered an unknown notice")
	}

	form := url.Values{"forge": {"gitlab"}, "host": {host}, "name": {"<b>work</b>"}, "credential": {"wrong-" + forgeToken}}
	w := post(h, "/connections", form, nil)
	if body := w.Body.String(); w.Code != 400 || !strings.Contains(body, "The forge rejected the token") || strings.Contains(body, forgeToken) || !strings.Contains(body, "&lt;b&gt;work&lt;/b&gt;") {
		t.Fatalf("rejected add: %d %s", w.Code, body)
	}
	form.Set("credential", forgeToken)
	w = post(h, "/connections", form, nil)
	if w.Code != 303 || w.Header().Get("Location") != "/connections?done=added" {
		t.Fatalf("add: %d %s", w.Code, w.Header())
	}
	body := page("/connections?done=added")
	if !strings.Contains(body, "Connection added") || !strings.Contains(body, "Working") || strings.Contains(body, forgeToken) {
		t.Fatal(body)
	}
	conns, err := m.ListConnections(context.Background())
	if err != nil || len(conns) != 1 {
		t.Fatalf("connections = %v, %v", conns, err)
	}
	id := string(conns[0].ID)

	for _, cross := range []http.Header{{"Sec-Fetch-Site": {"cross-site"}}, {"Origin": {"https://attacker.example"}}} {
		if w := post(h, "/connections/"+id+"/delete", nil, cross); w.Code != 403 {
			t.Fatalf("cross-origin delete: %d", w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/connections/"+id+"/delete", nil)
	r.SetBasicAuth("operator", "wrong")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("unauthenticated delete")
	}

	if w := post(h, "/connections/"+id+"/test", nil, nil); w.Code != 303 || w.Header().Get("Location") != "/connections?done=tested" {
		t.Fatalf("test: %d", w.Code)
	}
	if w := post(h, "/connections/"+id+"/credential", url.Values{"credential": {forgeToken}}, nil); w.Code != 303 {
		t.Fatalf("rotate: %d %s", w.Code, w.Body)
	}
	accept.Store(false)
	if w := post(h, "/connections/"+id+"/test", nil, nil); w.Code != 400 || !strings.Contains(w.Body.String(), "Token rejected") {
		t.Fatalf("failed test: %d %s", w.Code, w.Body)
	}
	if w := post(h, "/connections/"+id+"/delete", nil, nil); w.Code != 303 || w.Header().Get("Location") != "/connections?done=removed" {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := post(h, "/connections/"+id+"/delete", nil, nil); w.Code != 404 || !strings.Contains(w.Body.String(), "no longer exists") {
		t.Fatalf("delete twice: %d", w.Code)
	}
	if conns, _ := m.ListConnections(context.Background()); len(conns) != 0 {
		t.Fatal("connection not removed")
	}

	_, unconfigured, _ := setup(t, nil, time.Second)
	r = httptest.NewRequest("GET", "/connections", nil)
	r.SetBasicAuth("operator", operatorToken)
	w = httptest.NewRecorder()
	unconfigured.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "No connector configured") {
		t.Fatalf("unconfigured page: %d %s", w.Code, w.Body)
	}
}
