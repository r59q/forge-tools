package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// eventReactor returns an in-memory reactor with one webhook event from
// source gh, delivered to subscription ci.
func eventReactor(t *testing.T) (*reactor.Reactor, reactor.Event) {
	t.Helper()
	r, err := reactor.New(reactor.Config{Store: &reactor.JSONStore{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSubscription(context.Background(), reactor.Subscription{ID: "ci"}); err != nil {
		t.Fatal(err)
	}
	e, _, err := r.Accept(context.Background(), reactor.Input{Key: "k", Event: reactor.Event{
		Type: reactor.TypeBranchUpdated, Origin: reactor.OriginWebhook, Source: "gh", Forge: forgeauth.GitHub,
		Repository: "team/app", Subject: "refs/heads/main", Data: json.RawMessage(`{"branch":"main"}`),
		Native: &reactor.Native{Event: "push", Delivery: "d-1", Payload: json.RawMessage(`{"ref":"<b>refs/heads/main</b>"}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}

	return r, e
}

func TestEventsAPI(t *testing.T) {
	r, e := eventReactor(t)
	_, h, client := setupConfig(t, management.Config{Events: r}, 5*time.Second)
	ctx, cancel := rpcContext()
	defer cancel()

	list, err := client.ListEvents(ctx, &pb.ListEventsRequest{Source: "gh"})
	if err != nil || !list.GetConfigured() || len(list.GetEvents()) != 1 || list.GetEvents()[0].GetNative().GetPayload() != nil {
		t.Fatalf("list = %v, %v", list, err)
	}
	got, err := client.GetEvent(ctx, &pb.GetEventRequest{Id: e.ID})
	if err != nil || got.GetEvent().GetNative().GetPayload() == nil || len(got.GetDeliveries()) != 1 {
		t.Fatalf("get = %v, %v", got, err)
	}
	if _, err := client.GetEvent(ctx, &pb.GetEventRequest{Id: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing = %v", err)
	}
	if _, err := client.ListEvents(ctx, &pb.ListEventsRequest{Type: "Bad Type"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad type = %v", err)
	}

	if w := rest(h, "GET", "/v1/events?source=gh", ""); w.Code != 200 || !strings.Contains(w.Body.String(), e.ID) {
		t.Fatalf("REST list: %d %s", w.Code, w.Body)
	}
	if w := rest(h, "GET", "/v1/events/"+e.ID, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"deliveries"`) {
		t.Fatalf("REST get: %d %s", w.Code, w.Body)
	}
}

func TestEventPages(t *testing.T) {
	r, e := eventReactor(t)
	_, h, _ := setupConfig(t, management.Config{Events: r}, 5*time.Second)

	list := webPage(h, "/events?source=gh")
	if list.Code != 200 || !strings.Contains(list.Body.String(), `href="/events/`+e.ID+`"`) || !strings.Contains(list.Body.String(), "team/app") {
		t.Fatalf("list: %d %s", list.Code, list.Body)
	}
	if empty := webPage(h, "/events?source=other"); !strings.Contains(empty.Body.String(), "No retained event matches") {
		t.Fatalf("empty: %s", empty.Body)
	}
	if bad := webPage(h, "/events?type=Bad+Type"); bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "Check the filters") {
		t.Fatalf("bad: %d %s", bad.Code, bad.Body)
	}

	detail := webPage(h, "/events/"+e.ID)
	body := detail.Body.String()
	if detail.Code != 200 || !strings.Contains(body, "forge.branch.updated") || !strings.Contains(body, "&#34;branch&#34;: &#34;main&#34;") ||
		!strings.Contains(body, "ci") || strings.Contains(body, "<b>refs") {
		t.Fatalf("detail: %d %s", detail.Code, body)
	}
	if missing := webPage(h, "/events/missing"); missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "no longer retained") {
		t.Fatalf("missing: %d %s", missing.Code, missing.Body)
	}
	if none := webPage(h, "/events"); !strings.Contains(none.Body.String(), "Events") {
		t.Fatal("no events page")
	}
}

func TestEventsUnconfiguredPage(t *testing.T) {
	_, h, _ := setupConfig(t, management.Config{}, 5*time.Second)
	if w := webPage(h, "/events"); w.Code != 200 || !strings.Contains(w.Body.String(), "No forge-reactor configured") {
		t.Fatalf("page: %d %s", w.Code, w.Body)
	}
	if w := webPage(h, "/events/x"); w.Code != http.StatusNotImplemented {
		t.Fatalf("detail: %d", w.Code)
	}
}

func TestEventsURL(t *testing.T) {
	if eventsURL(&pb.ListEventsRequest{Source: "gh"}, "") != "" {
		t.Fatal("link at the end")
	}
	if got := eventsURL(&pb.ListEventsRequest{Source: "gh", Type: "a.b"}, "t&1"); got != "/events?page_token=t%261&source=gh&type=a.b" {
		t.Fatalf("url = %q", got)
	}
}

func TestWebhooksShowLastEvent(t *testing.T) {
	r, _ := eventReactor(t)
	fake := &fakeReactor{sources: map[string]management.WebhookSource{"gh": {ID: "gh", Managed: true}}}
	_, h, _ := setupConfig(t, management.Config{Webhooks: fake, Events: r}, 5*time.Second)

	w := webPage(h, "/webhooks")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `href="/events?source=gh"`) || !strings.Contains(w.Body.String(), "None retained") {
		t.Fatalf("page: %d %s", w.Code, w.Body)
	}
}
