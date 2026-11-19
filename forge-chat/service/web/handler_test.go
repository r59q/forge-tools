package web_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/r59q/forge-tools/forge-chat/service/api"
	pb "github.com/r59q/forge-tools/forge-chat/service/chatpb"
	"github.com/r59q/forge-tools/forge-chat/service/store"
	"github.com/r59q/forge-tools/forge-chat/service/web"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	browserToken  = "chat-test-token"
	upstreamToken = "agents-test-token"
)

func handler(t *testing.T, upstream string) (http.Handler, *api.Metadata) {
	t.Helper()
	target, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	data, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })

	token := forgeauth.StaticToken(browserToken)
	chats := api.NewMetadata(data, token, time.Second)
	rest, err := chats.REST(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	proxy := web.NewAgentProxy(target, forgeauth.StaticToken(upstreamToken), time.Second, 3*time.Second)
	return web.Handler(fstest.MapFS{
		"index.html":  {Data: []byte("<p>forge-chat</p>")},
		"src/main.js": {Data: []byte("export {};")},
	}, rest, proxy, token), chats
}

func TestSharedMetadataRESTAndRPC(t *testing.T) {
	h, chats := handler(t, "http://127.0.0.1")
	w := call(h, "PATCH", "/v1/chat-metadata/one", `{"archived":true,"links":{"ids":["two"]},"islandPosition":{"x":9}}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized PATCH = %d", w.Code)
	}
	w = call(h, "PATCH", "/v1/chat-metadata/one", `{"archived":true,"links":{"ids":["two"]},"islandPosition":{"x":9}}`, browserToken)
	record := &pb.ChatMetadata{}
	if w.Code != http.StatusOK || protojson.Unmarshal(w.Body.Bytes(), record) != nil || !record.GetArchived() {
		t.Fatalf("PATCH = %d %s", w.Code, w.Body)
	}
	if _, err := chats.ListChatMetadata(t.Context(), &pb.ListChatMetadataRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthorized RPC = %v", err)
	}
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+browserToken))
	got, err := chats.ListChatMetadata(ctx, &pb.ListChatMetadataRequest{})
	if err != nil || len(got.GetChats()) != 1 {
		t.Fatalf("ListChatMetadata = %v, %v", got, err)
	}
	record = got.GetChats()[0]
	ids := record.GetLinks().GetIds()
	if record.GetIslandPosition().GetX() != 9 || len(ids) != 1 || ids[0] != "two" {
		t.Fatalf("unexpected stored metadata %v", record)
	}
	w = call(h, "GET", "/v1/chat-metadata", "", browserToken)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"archived":true`) {
		t.Fatalf("GET metadata = %d %s", w.Code, w.Body)
	}
}

func TestAssetsAndRouteScope(t *testing.T) {
	h, _ := handler(t, "http://127.0.0.1")
	for _, path := range []string{"/", "/src/main.js"} {
		w := call(h, "GET", path, "", "")
		if w.Code != http.StatusOK || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("GET %s = %d %v", path, w.Code, w.Header())
		}
	}
	for _, path := range []string{"/src/", "/src/missing.js", "/v1/unknown", "/v1/agents/bad%2Fid"} {
		if w := call(h, "GET", path, "", browserToken); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d", path, w.Code)
		}
	}
	if w := call(h, "DELETE", "/v1/agents/one", "", browserToken); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE routed through chat: %d", w.Code)
	}
}

func TestProxyUsesOnlyUpstreamToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+upstreamToken ||
			r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-For") != "" {
			t.Error("incorrect forwarding headers")
		}
		var body map[string]string
		if r.Method != "POST" || r.URL.Path != "/v1/agents" ||
			json.NewDecoder(r.Body).Decode(&body) != nil || body["client"] != "codex" {
			t.Error("unexpected forwarded request")
		}
		w.Header().Set("Set-Cookie", "a=b")
		_, _ = io.WriteString(w, `{"id":"new-chat"}`)
	}))
	defer upstream.Close()
	h, _ := handler(t, upstream.URL)

	if w := call(h, "POST", "/v1/agents", `{"client":"codex"}`, upstreamToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("accepted upstream token as browser token: %d", w.Code)
	}
	r := request("POST", "/v1/agents", `{"client":"codex"}`, browserToken)
	r.Header.Set("Cookie", "session=local")
	r.Header.Set("X-Forwarded-For", "192.0.2.1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != `{"id":"new-chat"}` || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("POST = %d %s, %v", w.Code, w.Body, w.Header())
	}
}

func TestProxyFlushesEventsAndForwardsCursor(t *testing.T) {
	next := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "after=42" || r.URL.Path != "/v1/agents/one/stream" {
			t.Errorf("upstream path = %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintln(w, `{"event":{"seq":"43"}}`)
		_ = http.NewResponseController(w).Flush()
		select {
		case <-next:
			_, _ = fmt.Fprintln(w, `{"event":{"seq":"44"}}`)
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	h, _ := handler(t, upstream.URL)
	chat := httptest.NewUnstartedServer(h)
	chat.Config.WriteTimeout = time.Second
	chat.Start()
	defer chat.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "GET", chat.URL+"/v1/agents/one/stream?after=42", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+browserToken)
	response, err := chat.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != "{\"event\":{\"seq\":\"43\"}}\n" {
		t.Fatalf("first live record = %q, %v", line, err)
	}
	close(next)
	line, err = reader.ReadString('\n')
	if err != nil || line != "{\"event\":{\"seq\":\"44\"}}\n" {
		t.Fatalf("second live record = %q, %v", line, err)
	}
}

func call(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(method, path, body, token))
	return w
}

func request(method, path, body, token string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}
