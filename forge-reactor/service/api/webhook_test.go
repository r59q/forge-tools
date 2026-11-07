package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	"github.com/r59q/forge-tools/forge-reactor/library/sources"
	"github.com/r59q/forge-tools/forge-reactor/library/webhook"
)

const (
	githubPush  = `{"ref":"refs/heads/main","before":"1111","after":"2222","repository":{"full_name":"owner/repo"}}`
	forgejoPull = `{"action":"opened","number":3,"repository":{"full_name":"team/app"},
"pull_request":{"number":3,"title":"Fix","head":{"ref":"fix","sha":"5555"},"base":{"ref":"main","sha":"6666"}}}`
)

func mac(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))

	return hex.EncodeToString(m.Sum(nil))
}

// failing is credentials that cannot be read.
type failing struct{}

func (failing) Token(context.Context) (string, error) { return "", errors.New("unreadable") }

func hooks(t *testing.T) (*Webhooks, *reactor.Reactor) {
	t.Helper()
	r := newReactor(t)
	src, err := NewSources(context.Background(), []Source{
		{Source: webhook.Source{ID: "gh", Forge: forgeauth.GitHub, Connection: "github"}, Secret: forgeauth.StaticToken("gh-secret")},
		{Source: webhook.Source{ID: "fj", Forge: forgeauth.Forgejo, Connection: "forgejo", Repositories: []string{"team/app"}},
			Secret: forgeauth.StaticToken("fj-secret")},
		{Source: webhook.Source{ID: "broken", Forge: forgeauth.GitHub}, Secret: failing{}},
	}, manager(t))
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewWebhooks(r, src, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	return h, r
}

func github(event, delivery, body string) http.Header {
	h := http.Header{}
	h.Set("X-GitHub-Event", event)
	h.Set("X-GitHub-Delivery", delivery)
	h.Set("X-Hub-Signature-256", "sha256="+mac("gh-secret", body))

	return h
}

func forgejo(event, body string) http.Header {
	h := http.Header{}
	h.Set("X-Forgejo-Event", event)
	h.Set("X-Forgejo-Delivery", "fj-1")
	h.Set("X-Forgejo-Signature", mac("fj-secret", body))

	return h
}

func deliver(h http.Handler, method, path string, header http.Header, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func TestNewSources(t *testing.T) {
	secret := forgeauth.StaticToken("s")
	for name, configured := range map[string][]Source{
		"duplicate": {{Source: webhook.Source{ID: "a", Forge: forgeauth.GitHub}, Secret: secret},
			{Source: webhook.Source{ID: "a", Forge: forgeauth.Forgejo}, Secret: secret}},
		"bad id":    {{Source: webhook.Source{ID: "A/B", Forge: forgeauth.GitHub}, Secret: secret}},
		"no secret": {{Source: webhook.Source{ID: "a", Forge: forgeauth.GitHub}}},
		"gitlab":    {{Source: webhook.Source{ID: "a", Forge: forgeauth.GitLab}, Secret: secret}},
	} {
		if _, err := NewSources(context.Background(), configured, manager(t)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}

	m := manager(t)
	if _, err := m.Create(context.Background(), sources.Request{ID: "a", Forge: forgeauth.GitHub}); err != nil {
		t.Fatal(err)
	}
	taken := []Source{{Source: webhook.Source{ID: "a", Forge: forgeauth.GitHub}, Secret: secret}}
	if _, err := NewSources(context.Background(), taken, m); !errors.Is(err, ErrInvalid) {
		t.Errorf("managed ID configured: %v", err)
	}
	if _, err := NewWebhooks(newReactor(t), nil, time.Second); !errors.Is(err, ErrInvalid) {
		t.Errorf("no sources: %v", err)
	}
}

func TestWebhookAccepted(t *testing.T) {
	h, r := hooks(t)
	rec := deliver(h, http.MethodPost, "/webhooks/gh", github("push", "d-1", githubPush), githubPush)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"event":"`) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("github: %d %s", rec.Code, rec.Body)
	}
	again := deliver(h, http.MethodPost, "/webhooks/gh", github("push", "d-1", githubPush), githubPush)
	if again.Code != http.StatusAccepted || !strings.Contains(again.Body.String(), `"duplicate":true`) {
		t.Fatalf("duplicate: %d %s", again.Code, again.Body)
	}

	rec = deliver(h, http.MethodPost, "/webhooks/fj", forgejo("pull_request", forgejoPull), forgejoPull)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("forgejo: %d %s", rec.Code, rec.Body)
	}

	events, _, err := r.Events(context.Background(), reactor.EventQuery{})
	if err != nil || len(events) != 2 {
		t.Fatalf("events: %v %v", events, err)
	}
	for _, e := range events {
		if e.Origin != reactor.OriginWebhook || (e.Source != "gh" && e.Source != "fj") {
			t.Errorf("event %+v", e)
		}
	}
}

func TestWebhookRejected(t *testing.T) {
	h, r := hooks(t)
	bad := github("push", "d-2", githubPush)
	bad.Set("X-Hub-Signature-256", "sha256="+mac("wrong", githubPush))
	malformed := `{"ref":`
	mixed := github("push", "d-8", githubPush)
	mixed.Set("X-Forgejo-Event", "push")
	cases := map[string]struct {
		method, path string
		header       http.Header
		body         string
		want         int
	}{
		"bad signature":  {http.MethodPost, "/webhooks/gh", bad, githubPush, http.StatusUnauthorized},
		"no signature":   {http.MethodPost, "/webhooks/gh", http.Header{"X-Github-Event": {"push"}}, githubPush, http.StatusUnauthorized},
		"malformed":      {http.MethodPost, "/webhooks/gh", github("push", "d-3", malformed), malformed, http.StatusBadRequest},
		"other forge":    {http.MethodPost, "/webhooks/gh", mixed, githubPush, http.StatusForbidden},
		"other repo":     {http.MethodPost, "/webhooks/fj", forgejo("push", githubPush), githubPush, http.StatusForbidden},
		"unknown source": {http.MethodPost, "/webhooks/nope", github("push", "d-4", githubPush), githubPush, http.StatusNotFound},
		"get":            {http.MethodGet, "/webhooks/gh", nil, "", http.StatusMethodNotAllowed},
		"secret error":   {http.MethodPost, "/webhooks/broken", github("push", "d-5", githubPush), githubPush, http.StatusInternalServerError},
	}
	for name, tc := range cases {
		rec := deliver(h, tc.method, tc.path, tc.header, tc.body)
		if rec.Code != tc.want || strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}

	big := `{"ref":"refs/heads/main","pad":"` + strings.Repeat("x", MaxWebhookBody) + `"}`
	if rec := deliver(h, http.MethodPost, "/webhooks/gh", github("push", "d-6", big), big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized: %d", rec.Code)
	}

	events, _, err := r.Events(context.Background(), reactor.EventQuery{})
	if err != nil || len(events) != 0 {
		t.Fatalf("rejected deliveries created events: %v %v", events, err)
	}
}

func TestWebhookIgnored(t *testing.T) {
	h, r := hooks(t)
	body := `{"zen":"hi"}`
	rec := deliver(h, http.MethodPost, "/webhooks/gh", github("ping", "d-7", body), body)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"ignored":`) {
		t.Fatalf("ping: %d %s", rec.Code, rec.Body)
	}
	if events, _, _ := r.Events(context.Background(), reactor.EventQuery{}); len(events) != 0 {
		t.Fatalf("ignored delivery created %v", events)
	}
}

func TestRoutes(t *testing.T) {
	h, _ := hooks(t)
	rest := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	routes := Routes(rest, h)
	if rec := deliver(routes, http.MethodGet, "/v1/events", nil, ""); rec.Code != http.StatusTeapot {
		t.Fatalf("rest: %d", rec.Code)
	}
	if rec := deliver(routes, http.MethodGet, "/webhooks/gh", nil, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("webhooks: %d", rec.Code)
	}
}
