package rabbitmq

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

func TestNewValidates(t *testing.T) {
	for _, c := range []Config{
		{URL: "http://localhost"},
		{URL: ""},
		{URL: "amqp://localhost", QueuePrefix: "bad prefix"},
		{URL: "amqp://localhost", DialTimeout: -1},
	} {
		if _, err := New(c); !errors.Is(err, reactor.ErrInvalid) {
			t.Errorf("New(%+v) = %v", c, err)
		}
	}

	b, err := New(Config{URL: "amqp://user:hunter2@localhost:5672/"})
	if err != nil {
		t.Fatal(err)
	}
	if q := b.Queue("ci"); q != "forge-reactor.ci" {
		t.Errorf("Queue() = %q", q)
	}
}

func TestNewErrorHidesCredentials(t *testing.T) {
	_, err := New(Config{URL: "amqp://user:hunter2@localhost:notaport/"})
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("New() = %v", err)
	}
}

func TestPublishingCarriesIdentity(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m := reactor.Message{
		Subscription: "ci", DeliveryID: "d1", Attempt: 3, Replay: true,
		Event: reactor.Event{ID: "e1", Type: reactor.TypeBranchUpdated, AcceptedAt: at},
		Body:  []byte(`{"id":"e1"}`),
	}
	p := publishing(m)
	if p.DeliveryMode != amqp.Persistent || p.MessageId != "e1" || p.Type != reactor.TypeBranchUpdated || !p.Timestamp.Equal(at) ||
		p.ContentType != "application/json" || string(p.Body) != `{"id":"e1"}` {
		t.Errorf("publishing = %+v", p)
	}
	if p.Headers[HeaderDelivery] != "d1" || p.Headers[HeaderSubscription] != "ci" || p.Headers[HeaderAttempt] != int32(3) || p.Headers[HeaderReplay] != true {
		t.Errorf("headers = %+v", p.Headers)
	}
	if err := p.Headers.Validate(); err != nil {
		t.Errorf("headers are not valid AMQP: %v", err)
	}
}

func TestDecode(t *testing.T) {
	body, _ := json.Marshal(reactor.Event{ID: "e1", Type: "x", Origin: reactor.OriginPublished})
	e, err := Decode(body)
	if err != nil || e.ID != "e1" {
		t.Errorf("Decode() = %+v, %v", e, err)
	}
	for _, bad := range []string{"", "{}", "[1]", "{"} {
		if _, err := Decode([]byte(bad)); !errors.Is(err, reactor.ErrInvalid) {
			t.Errorf("Decode(%q) = %v", bad, err)
		}
	}
}
