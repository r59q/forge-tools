package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	pb "github.com/r59q/forge-tools/forge-reactor/service/reactorpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const token = "test-token"

func newReactor(t *testing.T) *reactor.Reactor {
	t.Helper()
	store, err := reactor.NewJSONStore(nil, func(context.Context, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	r, err := reactor.New(reactor.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}

	return r
}

func server(t *testing.T) *Server {
	t.Helper()
	s, err := New(newReactor(t), forgeauth.StaticToken(token), time.Second)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func authed() context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
}

func TestNew(t *testing.T) {
	r := newReactor(t)
	for name, err := range map[string]error{
		"reactor": func() error { _, err := New(nil, forgeauth.StaticToken(token), time.Second); return err }(),
		"token":   func() error { _, err := New(r, nil, time.Second); return err }(),
		"timeout": func() error { _, err := New(r, forgeauth.StaticToken(token), 0); return err }(),
	} {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAuthentication(t *testing.T) {
	s := server(t)
	wrong := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer nope"))
	for name, ctx := range map[string]context.Context{"missing": context.Background(), "wrong": wrong} {
		calls := map[string]error{
			"publish": second(s.PublishEvent(ctx, &pb.PublishEventRequest{Type: "app.built"})),
			"get":     second(s.GetEvent(ctx, &pb.GetEventRequest{Id: "x"})),
			"list":    second(s.ListEvents(ctx, &pb.ListEventsRequest{})),
			"create":  second(s.CreateSubscription(ctx, &pb.CreateSubscriptionRequest{Subscription: &pb.Subscription{Id: "a"}})),
			"getsub":  second(s.GetSubscription(ctx, &pb.GetSubscriptionRequest{Id: "a"})),
			"subs":    second(s.ListSubscriptions(ctx, &pb.ListSubscriptionsRequest{})),
			"delete":  second(s.DeleteSubscription(ctx, &pb.DeleteSubscriptionRequest{Id: "a"})),
			"history": second(s.ListDeliveries(ctx, &pb.ListDeliveriesRequest{})),
			"replay":  second(s.ReplayEvents(ctx, &pb.ReplayEventsRequest{Subscription: "a"})),
		}
		for call, err := range calls {
			if status.Code(err) != codes.Unauthenticated {
				t.Errorf("%s %s: %v", name, call, err)
			}
		}
	}

	subs, err := s.reactor.Subscriptions(context.Background())
	if err != nil || len(subs) != 0 {
		t.Fatalf("unauthenticated call changed state: %v %v", subs, err)
	}
}

func second[T any](_ T, err error) error { return err }

func TestPublishAndRead(t *testing.T) {
	s := server(t)
	ctx := authed()
	sub, err := s.CreateSubscription(ctx, &pb.CreateSubscriptionRequest{Subscription: &pb.Subscription{
		Id: "builds", Description: "CI", Filter: &pb.Filter{Types: []string{"app.built"}, Origins: []string{"published"}},
	}})
	if err != nil || sub.GetSubscription().GetCreatedAt() == nil || sub.GetSubscription().GetFilter().GetOrigins()[0] != "published" {
		t.Fatalf("create: %v %v", sub, err)
	}

	data, _ := structpb.NewStruct(map[string]any{"build": "42", "ok": true})
	req := &pb.PublishEventRequest{
		Type: "app.built", Repository: "team/app", Subject: "builds/42", Data: data, IdempotencyKey: "build-42",
		OccurredAt: timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	}
	first, err := s.PublishEvent(ctx, req)
	if err != nil || first.GetDuplicate() || first.GetEvent().GetOrigin() != "published" || first.GetEvent().GetData().AsMap()["ok"] != true {
		t.Fatalf("publish: %v %v", first, err)
	}
	again, err := s.PublishEvent(ctx, req)
	if err != nil || !again.GetDuplicate() || again.GetEvent().GetId() != first.GetEvent().GetId() {
		t.Fatalf("duplicate: %v %v", again, err)
	}

	got, err := s.GetEvent(ctx, &pb.GetEventRequest{Id: first.GetEvent().GetId()})
	if err != nil || !proto.Equal(got.GetEvent(), first.GetEvent()) || len(got.GetDeliveries()) != 1 ||
		got.GetDeliveries()[0].GetSubscription() != "builds" || got.GetDeliveries()[0].GetStatus() != "pending" {
		t.Fatalf("get: %v %v", got, err)
	}

	list, err := s.ListEvents(ctx, &pb.ListEventsRequest{Origins: []string{"published"}, Repositories: []string{"team/app"}})
	if err != nil || len(list.GetEvents()) != 1 {
		t.Fatalf("list: %v %v", list, err)
	}
	list, err = s.ListEvents(ctx, &pb.ListEventsRequest{Origins: []string{"webhook"}})
	if err != nil || len(list.GetEvents()) != 0 {
		t.Fatalf("filtered list: %v %v", list, err)
	}

	history, err := s.ListDeliveries(ctx, &pb.ListDeliveriesRequest{Subscription: "builds"})
	if err != nil || len(history.GetDeliveries()) != 1 {
		t.Fatalf("deliveries: %v %v", history, err)
	}

	replayed, err := s.ReplayEvents(ctx, &pb.ReplayEventsRequest{Subscription: "builds", EventIds: []string{first.GetEvent().GetId()}})
	if err != nil || len(replayed.GetDeliveries()) != 1 || !replayed.GetDeliveries()[0].GetReplay() ||
		replayed.GetDeliveries()[0].GetEventId() != first.GetEvent().GetId() {
		t.Fatalf("replay: %v %v", replayed, err)
	}
}

func TestSubscriptions(t *testing.T) {
	s := server(t)
	ctx := authed()
	for _, id := range []string{"b", "a"} {
		if _, err := s.CreateSubscription(ctx, &pb.CreateSubscriptionRequest{Subscription: &pb.Subscription{Id: id}}); err != nil {
			t.Fatal(err)
		}
	}

	list, err := s.ListSubscriptions(ctx, &pb.ListSubscriptionsRequest{})
	if err != nil || len(list.GetSubscriptions()) != 2 || list.GetSubscriptions()[0].GetId() != "a" {
		t.Fatalf("list: %v %v", list, err)
	}
	if got, err := s.GetSubscription(ctx, &pb.GetSubscriptionRequest{Id: "b"}); err != nil || got.GetSubscription().GetId() != "b" {
		t.Fatalf("get: %v %v", got, err)
	}
	if _, err := s.DeleteSubscription(ctx, &pb.DeleteSubscriptionRequest{Id: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSubscription(ctx, &pb.GetSubscriptionRequest{Id: "b"}); status.Code(err) != codes.NotFound {
		t.Fatalf("deleted: %v", err)
	}
}

func TestErrorCodes(t *testing.T) {
	s := server(t)
	ctx := authed()
	if _, err := s.CreateSubscription(ctx, &pb.CreateSubscriptionRequest{Subscription: &pb.Subscription{Id: "a"}}); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		err  error
		want codes.Code
	}{
		"bad type":       {second(s.PublishEvent(ctx, &pb.PublishEventRequest{Type: "Not A Type"})), codes.InvalidArgument},
		"bad forge":      {second(s.PublishEvent(ctx, &pb.PublishEventRequest{Type: "app.built", Forge: "gitlab"})), codes.InvalidArgument},
		"missing event":  {second(s.GetEvent(ctx, &pb.GetEventRequest{Id: "nope"})), codes.NotFound},
		"bad page token": {second(s.ListEvents(ctx, &pb.ListEventsRequest{PageToken: "!!"})), codes.InvalidArgument},
		"exists":         {second(s.CreateSubscription(ctx, &pb.CreateSubscriptionRequest{Subscription: &pb.Subscription{Id: "a"}})), codes.AlreadyExists},
		"bad id":         {second(s.CreateSubscription(ctx, &pb.CreateSubscriptionRequest{Subscription: &pb.Subscription{Id: "A B"}})), codes.InvalidArgument},
		"missing sub":    {second(s.ReplayEvents(ctx, &pb.ReplayEventsRequest{Subscription: "z", EventIds: []string{"x"}})), codes.NotFound},
		"unknown event":  {second(s.ReplayEvents(ctx, &pb.ReplayEventsRequest{Subscription: "a", EventIds: []string{"gone"}})), codes.NotFound},
	}
	for name, tc := range cases {
		if status.Code(tc.err) != tc.want {
			t.Errorf("%s: got %v, want %v", name, tc.err, tc.want)
		}
	}

	for err, want := range map[error]codes.Code{
		reactor.ErrBusy:          codes.Unavailable,
		reactor.ErrOutOfRange:    codes.OutOfRange,
		context.DeadlineExceeded: codes.DeadlineExceeded,
		errors.New("disk"):       codes.Internal,
	} {
		if got := code(err); got != want {
			t.Errorf("%v: got %v, want %v", err, got, want)
		}
	}
}

// rest sends one REST request and returns the status and body.
func rest(t *testing.T, h http.Handler, method, path, auth, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)

	return rec.Code, string(b)
}

func TestRESTParity(t *testing.T) {
	s := server(t)
	h, err := s.REST(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if code, body := rest(t, h, http.MethodGet, "/v1/subscriptions", "", ""); code != http.StatusUnauthorized || strings.Contains(body, token) {
		t.Fatalf("unauthenticated: %d %s", code, body)
	}
	if code, _ := rest(t, h, http.MethodGet, "/v1/subscriptions", "nope", ""); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}

	code, body := rest(t, h, http.MethodPost, "/v1/subscriptions", token, `{"id":"ci","filter":{"types":["app.built"]}}`)
	if code != http.StatusOK || !strings.Contains(body, `"id":"ci"`) {
		t.Fatalf("create: %d %s", code, body)
	}
	code, body = rest(t, h, http.MethodPost, "/v1/events", token, `{"type":"app.built","data":{"n":1}}`)
	if code != http.StatusOK {
		t.Fatalf("publish: %d %s", code, body)
	}
	var published struct {
		Event struct {
			ID string `json:"id"`
		} `json:"event"`
	}
	if err := json.Unmarshal([]byte(body), &published); err != nil || published.Event.ID == "" {
		t.Fatalf("publish body: %s %v", body, err)
	}

	code, body = rest(t, h, http.MethodGet, "/v1/events/"+published.Event.ID, token, "")
	viaGRPC, err := s.GetEvent(authed(), &pb.GetEventRequest{Id: published.Event.ID})
	if err != nil || code != http.StatusOK {
		t.Fatalf("get: %d %s %v", code, body, err)
	}
	viaREST := &pb.GetEventResponse{}
	if err := protojson.Unmarshal([]byte(body), viaREST); err != nil || !proto.Equal(viaREST, viaGRPC) {
		t.Fatalf("REST %s differs from gRPC %v: %v", body, viaGRPC, err)
	}

	code, body = rest(t, h, http.MethodPost, "/v1/subscriptions/ci:replay", token, `{"eventIds":["`+published.Event.ID+`"]}`)
	if code != http.StatusOK || !strings.Contains(body, `"replay":true`) {
		t.Fatalf("replay: %d %s", code, body)
	}
	if code, _ = rest(t, h, http.MethodGet, "/v1/subscriptions/missing", token, ""); code != http.StatusNotFound {
		t.Fatalf("missing: %d", code)
	}
	if code, _ = rest(t, h, http.MethodDelete, "/v1/subscriptions/ci", token, ""); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
}
