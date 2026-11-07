package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/sources"
	"github.com/r59q/forge-tools/forge-reactor/library/webhook"
	pb "github.com/r59q/forge-tools/forge-reactor/service/reactorpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// sourceServer returns a server with one configured source.
func sourceServer(t *testing.T) (*Server, *Webhooks) {
	t.Helper()
	configured := []Source{{Source: webhook.Source{ID: "static", Forge: forgeauth.GitHub}, Secret: forgeauth.StaticToken("s")}}
	src, err := NewSources(context.Background(), configured, manager(t))
	if err != nil {
		t.Fatal(err)
	}
	r := newReactor(t)
	s, err := New(r, src, forgeauth.StaticToken(token), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewWebhooks(r, src, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	return s, h
}

func TestManagedSourceLifecycle(t *testing.T) {
	s, h := sourceServer(t)
	ctx := authed()

	created, err := s.CreateWebhookSource(ctx, &pb.CreateWebhookSourceRequest{Id: "app", Forge: "github", Connection: "gh"})
	if err != nil || len(created.GetSecret()) != 64 || !created.GetSource().GetManaged() {
		t.Fatalf("create: %v %v", created, err)
	}
	if created.GetSource().GetUrl() != "https://example.org/reactor/webhooks/app" {
		t.Fatalf("url = %q", created.GetSource().GetUrl())
	}

	header := github("push", "d-1", githubPush)
	header.Set("X-Hub-Signature-256", "sha256="+mac(created.GetSecret(), githubPush))
	if rec := deliver(h, http.MethodPost, "/webhooks/app", header, githubPush); rec.Code != http.StatusAccepted {
		t.Fatalf("delivery: %d %s", rec.Code, rec.Body)
	}

	for source, want := range map[string]int{"app": 1, "static": 0} {
		events, err := s.ListEvents(ctx, &pb.ListEventsRequest{Sources: []string{source}})
		if err != nil || len(events.GetEvents()) != want {
			t.Fatalf("events from %s: %v %v", source, events, err)
		}
	}

	list, err := s.ListWebhookSources(ctx, &pb.ListWebhookSourcesRequest{})
	ids := []string{}
	for _, src := range list.GetSources() {
		ids = append(ids, src.GetId())
	}
	if err != nil || strings.Join(ids, ",") != "static,app" || list.GetSources()[0].GetManaged() {
		t.Fatalf("list: %v %v", list, err)
	}

	rotated, err := s.RotateWebhookSecret(ctx, &pb.RotateWebhookSecretRequest{Id: "app"})
	if err != nil || rotated.GetSecret() == created.GetSecret() || len(rotated.GetFailedRepositories()) != 0 {
		t.Fatalf("rotate: %v %v", rotated, err)
	}
	header.Set("X-GitHub-Delivery", "d-2")
	if rec := deliver(h, http.MethodPost, "/webhooks/app", header, githubPush); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old secret: %d", rec.Code)
	}

	if _, err := s.DeleteWebhookSource(ctx, &pb.DeleteWebhookSourceRequest{Id: "app"}); err != nil {
		t.Fatal(err)
	}
	if rec := deliver(h, http.MethodPost, "/webhooks/app", header, githubPush); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted: %d", rec.Code)
	}
}

func TestSourceErrorCodes(t *testing.T) {
	s, _ := sourceServer(t)
	ctx := authed()
	register := &pb.CreateWebhookSourceRequest{Id: "x", Forge: "github", Connection: "gh", Repositories: []string{"a/b"}, Register: true}

	for name, tc := range map[string]struct {
		err  error
		want codes.Code
	}{
		"configured id":  {second(s.CreateWebhookSource(ctx, &pb.CreateWebhookSourceRequest{Id: "static", Forge: "github"})), codes.InvalidArgument},
		"bad forge":      {second(s.CreateWebhookSource(ctx, &pb.CreateWebhookSourceRequest{Id: "x", Forge: "gitlab"})), codes.InvalidArgument},
		"no connector":   {second(s.CreateWebhookSource(ctx, register)), codes.FailedPrecondition},
		"rotate static":  {second(s.RotateWebhookSecret(ctx, &pb.RotateWebhookSecretRequest{Id: "static"})), codes.NotFound},
		"delete missing": {second(s.DeleteWebhookSource(ctx, &pb.DeleteWebhookSourceRequest{Id: "nope"})), codes.NotFound},
		"no token":       {second(s.ListWebhookSources(context.Background(), &pb.ListWebhookSourcesRequest{})), codes.Unauthenticated},
	} {
		if status.Code(tc.err) != tc.want {
			t.Errorf("%s: got %v, want %v", name, tc.err, tc.want)
		}
	}
	if code(sources.ErrRegistration) != codes.FailedPrecondition {
		t.Error("registration failure is not FailedPrecondition")
	}
}

func TestSourceREST(t *testing.T) {
	s, _ := sourceServer(t)
	h, err := s.REST(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	code, body := rest(t, h, http.MethodPost, "/v1/webhook-sources", token, `{"id":"app","forge":"forgejo","repositories":["team/app"]}`)
	if code != http.StatusOK || !strings.Contains(body, `"secret":"`) {
		t.Fatalf("create: %d %s", code, body)
	}
	code, body = rest(t, h, http.MethodGet, "/v1/webhook-sources", token, "")
	if code != http.StatusOK || strings.Contains(body, "secret") || !strings.Contains(body, `"managed":true`) {
		t.Fatalf("list: %d %s", code, body)
	}
	if code, body = rest(t, h, http.MethodPost, "/v1/webhook-sources/app:rotate", token, `{}`); code != http.StatusOK {
		t.Fatalf("rotate: %d %s", code, body)
	}
	if code, body = rest(t, h, http.MethodDelete, "/v1/webhook-sources/app", token, ""); code != http.StatusOK {
		t.Fatalf("delete: %d %s", code, body)
	}
}
