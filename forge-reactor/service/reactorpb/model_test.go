package reactorpb

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

func TestEventRoundTrip(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	in := reactor.Event{
		ID: "e1", Type: reactor.TypeBranchUpdated, Origin: reactor.OriginWebhook, Source: "gh", Forge: forgeauth.GitHub,
		Connection: "c", Repository: "team/app", Subject: "refs/heads/main", OccurredAt: at, AcceptedAt: at,
		Data:   json.RawMessage(`{"branch":"main"}`),
		Native: &reactor.Native{Event: "push", Delivery: "d1", Payload: json.RawMessage(`{"ref":"refs/heads/main"}`)},
	}

	pb, err := FromEvent(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ToEvent(pb)
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != in.ID || out.Forge != in.Forge || !out.AcceptedAt.Equal(at) || string(out.Data) != `{"branch":"main"}` ||
		out.Native == nil || string(out.Native.Payload) != `{"ref":"refs/heads/main"}` {
		t.Fatalf("round trip = %+v", out)
	}

	empty, err := ToEvent(&Event{Id: "e2"})
	if err != nil || empty.Data != nil || empty.Native != nil || !empty.AcceptedAt.IsZero() {
		t.Fatalf("empty = %+v, %v", empty, err)
	}
	if _, err := FromEvent(reactor.Event{Data: json.RawMessage(`[1]`)}); err == nil {
		t.Fatal("non-object data accepted")
	}
}

func TestDeliveriesRoundTrip(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	in := []reactor.Delivery{{
		ID: "d1", EventID: "e1", Subscription: "ci", Status: reactor.StatusAccepted, CreatedAt: at, AcceptedAt: at,
		Attempts: []reactor.Attempt{{At: at, Error: "down"}, {At: at}},
	}}

	out := ToDeliveries(FromDeliveries(in))
	if len(out) != 1 || out[0].Status != reactor.StatusAccepted || len(out[0].Attempts) != 2 ||
		out[0].Attempts[0].Error != "down" || !out[0].NextAttemptAt.IsZero() {
		t.Fatalf("round trip = %+v", out)
	}
}
