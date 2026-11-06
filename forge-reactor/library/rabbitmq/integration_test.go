package rabbitmq

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

// Integration tests need a RabbitMQ server. They run when
// FORGE_REACTOR_RABBITMQ_URL is set, such as in CI:
//
//	docker run -d -p 5672:5672 rabbitmq:4
//	FORGE_REACTOR_RABBITMQ_URL=amqp://guest:guest@localhost:5672/ go test ./rabbitmq/
//
// TestIntegrationPersistence additionally runs in two phases around a broker
// restart, selected by FORGE_REACTOR_RABBITMQ_PHASE=publish and then
// FORGE_REACTOR_RABBITMQ_PHASE=consume.

const persistPrefix = "forge-reactor-it-persist."

func rabbitURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("FORGE_REACTOR_RABBITMQ_URL")
	if u == "" {
		t.Skip("FORGE_REACTOR_RABBITMQ_URL is not set")
	}

	return u
}

func unique(t *testing.T) string {
	t.Helper()
	var b [6]byte
	_, _ = rand.Read(b[:])

	return "forge-reactor-it-" + hex.EncodeToString(b[:]) + "."
}

func newBroker(t *testing.T, rawURL, prefix string) *Broker {
	t.Helper()
	b, err := New(Config{URL: rawURL, QueuePrefix: prefix, DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })

	return b
}

// consumer opens a separate connection reading one queue with manual acks.
func consumer(t *testing.T, rawURL, queue string) (*amqp.Connection, <-chan amqp.Delivery) {
	t.Helper()
	conn, err := amqp.Dial(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ch, err := conn.Channel()
	if err == nil {
		err = ch.Qos(1, 0, false)
	}
	if err != nil {
		t.Fatal(err)
	}
	deliveries, err := ch.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}

	return conn, deliveries
}

func next(t *testing.T, deliveries <-chan amqp.Delivery) amqp.Delivery {
	t.Helper()
	select {
	case d, ok := <-deliveries:
		if !ok {
			t.Fatal("consumer closed")
		}

		return d
	case <-time.After(10 * time.Second):
		t.Fatal("no message within 10s")
	}

	return amqp.Delivery{}
}

func message(id string) reactor.Message {
	e := reactor.Event{ID: id, Type: reactor.TypeBranchUpdated, Origin: reactor.OriginPublished, AcceptedAt: time.Now().UTC()}

	return reactor.Message{Subscription: "sub", DeliveryID: "d-" + id, Attempt: 1, Event: e, Body: []byte(`{"id":"` + id + `"}`)}
}

func TestIntegrationAckAndRedelivery(t *testing.T) {
	rawURL := rabbitURL(t)
	ctx := context.Background()
	b := newBroker(t, rawURL, unique(t))
	t.Cleanup(func() { _ = b.Remove(context.Background(), "sub") })

	if err := b.Prepare(ctx, "sub"); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(ctx, message("e1")); err != nil {
		t.Fatal(err)
	}

	conn, deliveries := consumer(t, rawURL, b.Queue("sub"))
	d := next(t, deliveries)
	if d.MessageId != "e1" || d.Redelivered || d.Headers[HeaderDelivery] != "d-e1" || d.DeliveryMode != amqp.Persistent {
		t.Fatalf("first delivery = %+v", d)
	}

	// A nack with requeue returns the message with the redelivered flag.
	if err := d.Nack(false, true); err != nil {
		t.Fatal(err)
	}
	d = next(t, deliveries)
	if d.MessageId != "e1" || !d.Redelivered {
		t.Fatalf("after nack = %+v", d)
	}

	// A consumer that disconnects without acking gets it again later.
	_ = conn.Close()
	_, deliveries = consumer(t, rawURL, b.Queue("sub"))
	d = next(t, deliveries)
	if d.MessageId != "e1" || !d.Redelivered {
		t.Fatalf("after disconnect = %+v", d)
	}
	if err := d.Ack(false); err != nil {
		t.Fatal(err)
	}

	// Acked messages are gone.
	if err := b.Publish(ctx, message("e2")); err != nil {
		t.Fatal(err)
	}
	if d := next(t, deliveries); d.MessageId != "e2" || d.Redelivered {
		t.Fatalf("after ack = %+v", d)
	}
}

func TestIntegrationUnroutableAndRemove(t *testing.T) {
	rawURL := rabbitURL(t)
	ctx := context.Background()
	b := newBroker(t, rawURL, unique(t))
	if err := b.Prepare(ctx, "sub"); err != nil {
		t.Fatal(err)
	}

	// Deleting the queue behind the broker's back makes the next publish
	// unroutable; it is not confirmed as accepted.
	other := newBroker(t, rawURL, b.cfg.QueuePrefix)
	if err := other.Remove(ctx, "sub"); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(ctx, message("e1")); !errors.Is(err, ErrUnroutable) {
		t.Fatalf("Publish() to a deleted queue = %v", err)
	}
	if err := b.Publish(ctx, message("e1")); err != nil {
		t.Fatalf("retry after redeclare = %v", err)
	}

	if err := b.Remove(ctx, "sub"); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(ctx, "sub"); err != nil {
		t.Fatalf("removing a missing queue = %v", err)
	}
}

func TestIntegrationOutageRecovery(t *testing.T) {
	rawURL := rabbitURL(t)
	ctx := context.Background()
	p := newProxy(t, rawURL)
	b := newBroker(t, p.url, unique(t))
	t.Cleanup(func() { _ = newBroker(t, rawURL, b.cfg.QueuePrefix).Remove(context.Background(), "sub") })

	r, err := reactor.New(reactor.Config{Store: &reactor.JSONStore{}, Retry: reactor.Retry{Delay: 50 * time.Millisecond, MaxDelay: 200 * time.Millisecond, Timeout: 2 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSubscription(ctx, reactor.Subscription{ID: "sub"}); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = r.Run(runCtx, b) }()

	_, deliveries := consumer(t, rawURL, waitQueue(t, rawURL, b.Queue("sub")))
	first, _, err := r.Publish(ctx, reactor.Event{Type: "test.outage"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if d := next(t, deliveries); d.MessageId != first.ID {
		t.Fatalf("before outage got %q", d.MessageId)
	}

	// During the outage the event is stored and its delivery retried.
	p.cut()
	during, _, err := r.Publish(ctx, reactor.Event{Type: "test.outage"}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		ds, _, _ := r.Deliveries(ctx, reactor.DeliveryQuery{EventID: during.ID})

		return len(ds) == 1 && len(ds[0].Attempts) > 0
	})
	if ds, _, _ := r.Deliveries(ctx, reactor.DeliveryQuery{EventID: during.ID}); ds[0].Status != reactor.StatusPending {
		t.Fatalf("delivery during outage = %+v", ds[0])
	}

	p.restore()
	d := next(t, deliveries)
	if d.MessageId != during.ID {
		t.Fatalf("after outage got %q, want %q", d.MessageId, during.ID)
	}
	_ = d.Ack(false)
	waitFor(t, func() bool {
		ds, _, _ := r.Deliveries(ctx, reactor.DeliveryQuery{EventID: during.ID, Status: reactor.StatusAccepted})

		return len(ds) == 1
	})
}

func TestIntegrationPersistence(t *testing.T) {
	rawURL := rabbitURL(t)
	phase := os.Getenv("FORGE_REACTOR_RABBITMQ_PHASE")
	if phase == "" {
		t.Skip("FORGE_REACTOR_RABBITMQ_PHASE is not set")
	}
	ctx := context.Background()
	b := newBroker(t, rawURL, persistPrefix)

	switch phase {
	case "publish":
		if err := b.Publish(ctx, message("persisted")); err != nil {
			t.Fatal(err)
		}
	case "consume":
		_, deliveries := consumer(t, rawURL, b.Queue("sub"))
		d := next(t, deliveries)
		if d.MessageId != "persisted" {
			t.Fatalf("after restart got %+v", d)
		}
		_ = d.Ack(false)
		if err := b.Remove(ctx, "sub"); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown phase %q", phase)
	}
}

// waitQueue waits until the dispatcher has declared queue.
func waitQueue(t *testing.T, rawURL, queue string) string {
	t.Helper()
	conn, err := amqp.Dial(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitFor(t, func() bool {
		ch, err := conn.Channel()
		if err != nil {
			return false
		}
		defer ch.Close()
		_, err = ch.QueueDeclarePassive(queue, true, false, false, false, amqp.Table{amqp.QueueTypeArg: amqp.QueueTypeQuorum})

		return err == nil
	})

	return queue
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 15s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// proxy forwards TCP to RabbitMQ and can cut every connection.
type proxy struct {
	url    string
	target string
	ln     net.Listener

	mu    sync.Mutex
	down  bool
	conns []net.Conn
}

func newProxy(t *testing.T, rawURL string) *proxy {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{target: u.Host, ln: ln}
	u.Host = ln.Addr().String()
	p.url = u.String()
	t.Cleanup(func() { _ = ln.Close(); p.cut() })
	go p.serve()

	return p
}

func (p *proxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		down := p.down
		if !down {
			p.conns = append(p.conns, c)
		}
		p.mu.Unlock()
		if down {
			_ = c.Close()

			continue
		}
		go p.forward(c)
	}
}

func (p *proxy) forward(c net.Conn) {
	var d net.Dialer
	up, err := d.DialContext(context.Background(), "tcp", p.target)
	if err != nil {
		_ = c.Close()

		return
	}
	p.mu.Lock()
	p.conns = append(p.conns, up)
	p.mu.Unlock()
	go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
	_, _ = io.Copy(c, up)
	_ = c.Close()
}

// cut closes every connection and refuses new ones until restore.
func (p *proxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = true
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}

func (p *proxy) restore() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = false
}
