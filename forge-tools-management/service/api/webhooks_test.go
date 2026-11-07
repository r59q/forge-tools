package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeReactor keeps webhook sources in memory, like forge-reactor.
type fakeReactor struct {
	sources   map[string]management.WebhookSource
	last      management.NewWebhook
	serial    int
	leftovers []string
}

func (f *fakeReactor) ListWebhooks(context.Context) ([]management.WebhookSource, error) {
	out := []management.WebhookSource{{ID: "static", Forge: forgeauth.GitHub}}
	for _, src := range f.sources {
		out = append(out, src)
	}

	return out, nil
}

func (f *fakeReactor) secret() string {
	f.serial++

	return "generated-secret-" + strconv.Itoa(f.serial)
}

func (f *fakeReactor) CreateWebhook(_ context.Context, w management.NewWebhook) (management.WebhookSecret, error) {
	if _, ok := f.sources[w.ID]; ok {
		return management.WebhookSecret{}, management.ErrExists
	}
	f.last = w
	src := management.WebhookSource{
		ID: w.ID, Forge: w.Forge, Connection: w.Connection, Repositories: w.Repositories,
		Managed: true, Registered: w.Register, URL: "https://example.org/reactor/webhooks/" + w.ID,
	}
	f.sources[w.ID] = src

	return management.WebhookSecret{Source: src, Secret: f.secret()}, nil
}

func (f *fakeReactor) RotateWebhook(_ context.Context, id string) (management.WebhookSecret, error) {
	src, ok := f.sources[id]
	if !ok {
		return management.WebhookSecret{}, management.ErrNotFound
	}

	return management.WebhookSecret{Source: src, Secret: f.secret(), Failed: f.leftovers}, nil
}

func (f *fakeReactor) DeleteWebhook(_ context.Context, id string) ([]string, error) {
	if _, ok := f.sources[id]; !ok {
		return nil, management.ErrNotFound
	}
	delete(f.sources, id)

	return f.leftovers, nil
}

// webPage reads a web page as the operator.
func webPage(h http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.SetBasicAuth("operator", operatorToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	return w
}

// githubConnector runs a real broker with one GitHub connection.
func githubConnector(t *testing.T) (*broker.Manager, string) {
	t.Helper()
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "login": "owner"})
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

	c, err := management.New(management.Config{Connector: m}).AddConnection(context.Background(), management.NewConnection{
		Kind: forgeauth.GitHub, Host: forge.URL, Credential: forgeToken,
	})
	if err != nil {
		t.Fatal(err)
	}

	return m, c.ID
}

func TestWebhooksUnconfigured(t *testing.T) {
	_, h, client := setupConfig(t, management.Config{}, 5*time.Second)
	if w := webPage(h, "/webhooks"); w.Code != 200 || !strings.Contains(w.Body.String(), "No forge-reactor configured") {
		t.Fatalf("page: %d %s", w.Code, w.Body)
	}

	ctx, cancel := rpcContext()
	defer cancel()
	if _, err := client.AddWebhook(ctx, &pb.AddWebhookRequest{Id: "x", Forge: "github"}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("add = %v", err)
	}
}

func TestWebhooksAPI(t *testing.T) {
	fake := &fakeReactor{sources: map[string]management.WebhookSource{}}
	_, h, client := setupConfig(t, management.Config{Webhooks: fake}, 5*time.Second)

	w := rest(h, "POST", "/v1/webhooks", `{"id":"app","forge":"github","repositories":["team/app"]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "generated-secret-1") {
		t.Fatalf("REST add: %d %s", w.Code, w.Body)
	}
	if w := rest(h, "GET", "/v1/webhooks", ""); w.Code != 200 || strings.Contains(w.Body.String(), "secret") ||
		!strings.Contains(w.Body.String(), `"managed":true`) {
		t.Fatalf("REST list: %d %s", w.Code, w.Body)
	}
	if w := rest(h, "POST", "/v1/webhooks/app:rotate", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), "generated-secret-2") {
		t.Fatalf("REST rotate: %d %s", w.Code, w.Body)
	}

	ctx, cancel := rpcContext()
	defer cancel()
	if _, err := client.AddWebhook(ctx, &pb.AddWebhookRequest{Id: "app", Forge: "github"}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate = %v", err)
	}
	if _, err := client.RemoveWebhook(ctx, &pb.RemoveWebhookRequest{Id: "app"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RotateWebhookSecret(ctx, &pb.RotateWebhookSecretRequest{Id: "app"}); status.Code(err) != codes.NotFound {
		t.Fatalf("rotate removed = %v", err)
	}
	if code(management.ErrRegistration) != codes.FailedPrecondition {
		t.Fatal("registration failure is not FailedPrecondition")
	}
}

func TestWebhooksPage(t *testing.T) {
	m, connection := githubConnector(t)
	fake := &fakeReactor{sources: map[string]management.WebhookSource{}}
	_, h, _ := setupConfig(t, management.Config{Connector: m, Webhooks: fake}, 5*time.Second)

	list := webPage(h, "/webhooks")
	if list.Code != 200 || !strings.Contains(list.Body.String(), `value="`+connection+`"`) || !strings.Contains(list.Body.String(), "Configuration file") {
		t.Fatalf("page: %d %s", list.Code, list.Body)
	}

	form := url.Values{"id": {"app"}, "connection": {connection}, "repositories": {"team/app, team/lib"}, "register": {"on"}}
	added := post(h, "/webhooks", form, nil)
	body := added.Body.String()
	if added.Code != 200 || !strings.Contains(body, "generated-secret-1") || !strings.Contains(body, "https://example.org/reactor/webhooks/app") {
		t.Fatalf("add: %d %s", added.Code, body)
	}
	if fake.last.Forge != forgeauth.GitHub || len(fake.last.Repositories) != 2 || !fake.last.Register {
		t.Fatalf("request = %+v", fake.last)
	}
	if again := webPage(h, "/webhooks"); strings.Contains(again.Body.String(), "generated-secret") {
		t.Fatal("secret shown again")
	}

	if dup := post(h, "/webhooks", form, nil); dup.Code != http.StatusConflict || !strings.Contains(dup.Body.String(), "already exists") ||
		!strings.Contains(dup.Body.String(), `value="team/app, team/lib"`) {
		t.Fatalf("duplicate: %d %s", dup.Code, dup.Body)
	}

	fake.leftovers = []string{"team/lib"}
	rotated := post(h, "/webhooks/app/rotate", nil, nil)
	if rotated.Code != 200 || !strings.Contains(rotated.Body.String(), "generated-secret-2") || !strings.Contains(rotated.Body.String(), "old secret: team/lib") {
		t.Fatalf("rotate: %d %s", rotated.Code, rotated.Body)
	}
	removed := post(h, "/webhooks/app/delete", nil, nil)
	if removed.Code != 200 || !strings.Contains(removed.Body.String(), "could not be removed from team/lib") {
		t.Fatalf("remove with leftovers: %d %s", removed.Code, removed.Body)
	}
}

func TestWebhookRemoveRedirects(t *testing.T) {
	fake := &fakeReactor{sources: map[string]management.WebhookSource{"app": {ID: "app", Managed: true}}}
	_, h, _ := setupConfig(t, management.Config{Webhooks: fake}, 5*time.Second)

	w := post(h, "/webhooks/app/delete", nil, nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/webhooks?done=removed" {
		t.Fatalf("remove: %d %v", w.Code, w.Header())
	}
	if notice := webPage(h, "/webhooks?done=removed"); !strings.Contains(notice.Body.String(), "Webhook source removed") {
		t.Fatalf("notice: %s", notice.Body)
	}
	if missing := post(h, "/webhooks/app/delete", nil, nil); missing.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", missing.Code)
	}
	for _, c := range []codes.Code{codes.InvalidArgument, codes.FailedPrecondition, codes.Unimplemented, codes.DeadlineExceeded, codes.Internal} {
		if webhookProblem(c) == "" {
			t.Errorf("no problem text for %v", c)
		}
	}
}
