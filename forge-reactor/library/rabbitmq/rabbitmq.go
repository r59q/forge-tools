// Package rabbitmq hands reactor deliveries to RabbitMQ. Each subscription
// has a durable quorum queue; messages are persistent and published through
// the default exchange with the mandatory flag and publisher confirms, so a
// hand-off succeeds only once RabbitMQ has stored the message in the queue.
//
// Consumers read the queue with manual acknowledgements. An ack removes the
// message; a reject or nack with requeue, a closed channel or a lost
// consumer connection returns it for redelivery with the redelivered flag
// set. A reject without requeue drops it, unless the consumer's operator
// configured a dead-letter policy.
package rabbitmq

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

// Defaults for Config fields left empty.
const (
	DefaultPrefix      = "forge-reactor."
	DefaultDialTimeout = 10 * time.Second
)

// Message headers set on every delivery, beside the AMQP properties:
// message-id is the event ID, type is the event type and timestamp is the
// acceptance time.
const (
	// HeaderDelivery is the reactor delivery ID.
	HeaderDelivery = "forge-reactor-delivery"
	// HeaderSubscription is the receiving subscription.
	HeaderSubscription = "forge-reactor-subscription"
	// HeaderAttempt counts reactor hand-off attempts, starting at 1.
	HeaderAttempt = "forge-reactor-attempt"
	// HeaderReplay is true for a replayed delivery.
	HeaderReplay = "forge-reactor-replay"
)

var (
	// ErrUnroutable means RabbitMQ returned a message because its queue
	// did not exist; the queue is declared again on the next attempt.
	ErrUnroutable = errors.New("rabbitmq: message returned as unroutable")
	// ErrNacked means RabbitMQ refused to take responsibility for a message.
	ErrNacked = errors.New("rabbitmq: message not confirmed")
)

// prefixPattern restricts queue name prefixes.
var prefixPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{0,64}$`)

// Config configures a Broker.
type Config struct {
	// URL is an amqp:// or amqps:// URL. It may hold credentials, which
	// never appear in errors.
	URL string
	// QueuePrefix starts every queue name; empty uses DefaultPrefix.
	QueuePrefix string
	// DialTimeout caps connecting; zero uses DefaultDialTimeout.
	DialTimeout time.Duration
}

// Broker is a reactor.Broker for RabbitMQ. It connects on first use and
// reconnects after any failure. It is safe for concurrent use.
type Broker struct {
	cfg Config

	mu       sync.Mutex
	conn     *amqp.Connection
	ch       *amqp.Channel
	returns  chan amqp.Return
	declared map[string]bool
}

// New validates c and returns a Broker. It does not connect.
func New(c Config) (*Broker, error) {
	if _, err := amqp.ParseURI(c.URL); err != nil {
		return nil, fmt.Errorf("%w: rabbitmq URL", reactor.ErrInvalid)
	}
	if !prefixPattern.MatchString(c.QueuePrefix) || c.DialTimeout < 0 {
		return nil, reactor.ErrInvalid
	}
	c.QueuePrefix = cmp.Or(c.QueuePrefix, DefaultPrefix)
	c.DialTimeout = cmp.Or(c.DialTimeout, DefaultDialTimeout)

	return &Broker{cfg: c, declared: map[string]bool{}}, nil
}

// Queue returns a subscription's queue name.
func (b *Broker) Queue(subscription string) string { return b.cfg.QueuePrefix + subscription }

// Prepare declares the subscription's queue.
func (b *Broker) Prepare(ctx context.Context, subscription string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.guard(ctx, func(ch *amqp.Channel) error { return b.declare(ch, subscription) })
}

// Remove deletes the subscription's queue and every message in it.
func (b *Broker) Remove(ctx context.Context, subscription string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.guard(ctx, func(ch *amqp.Channel) error {
		delete(b.declared, subscription)
		_, err := ch.QueueDelete(b.Queue(subscription), false, false, false)
		var amqpErr *amqp.Error
		if errors.As(err, &amqpErr) && amqpErr.Code == amqp.NotFound {
			return nil
		}

		return err
	})
}

// Publish sends m as a persistent message and waits for the confirm.
func (b *Broker) Publish(ctx context.Context, m reactor.Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.guard(ctx, func(ch *amqp.Channel) error {
		if err := b.declare(ch, m.Subscription); err != nil {
			return err
		}
		confirm, err := ch.PublishWithDeferredConfirmWithContext(ctx, "", b.Queue(m.Subscription), true, false, publishing(m))
		if err != nil {
			return err
		}
		acked, err := confirm.WaitContext(ctx)
		if err != nil {
			return err
		}

		return b.confirmed(acked, m)
	})
}

// confirmed checks the confirm and any return of m.
func (b *Broker) confirmed(acked bool, m reactor.Message) error {
	for {
		select {
		case r := <-b.returns:
			if r.Headers[HeaderDelivery] == m.DeliveryID {
				delete(b.declared, m.Subscription)

				return ErrUnroutable
			}
		default:
			if !acked {
				return ErrNacked
			}

			return nil
		}
	}
}

// Close closes the connection.
func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reset()

	return nil
}

// publishing builds the AMQP message for m.
func publishing(m reactor.Message) amqp.Publishing {
	return amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    m.Event.ID,
		Type:         m.Event.Type,
		Timestamp:    m.Event.AcceptedAt,
		AppId:        "forge-reactor",
		Headers: amqp.Table{
			HeaderDelivery:     m.DeliveryID,
			HeaderSubscription: m.Subscription,
			HeaderAttempt:      int32(min(m.Attempt, 1<<30)), //nolint:gosec // bounded above
			HeaderReplay:       m.Replay,
		},
		Body: m.Body,
	}
}

// declare declares the subscription's quorum queue once per connection.
func (b *Broker) declare(ch *amqp.Channel, subscription string) error {
	if b.declared[subscription] {
		return nil
	}
	if !reactor.ValidID(subscription) {
		return reactor.ErrInvalid
	}
	_, err := ch.QueueDeclare(b.Queue(subscription), true, false, false, false, amqp.Table{amqp.QueueTypeArg: amqp.QueueTypeQuorum})
	if err == nil {
		b.declared[subscription] = true
	}

	return err
}

// guard runs fn on an open channel. Any failure drops the connection, so
// the next call starts clean. Errors never contain the URL.
func (b *Broker) guard(ctx context.Context, fn func(*amqp.Channel) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ch, err := b.channel()
	if err == nil {
		err = fn(ch)
	}
	switch {
	case err == nil, errors.Is(err, ErrUnroutable), errors.Is(err, reactor.ErrInvalid):
		return err
	case errors.Is(err, ErrNacked):
		b.reset()

		return err
	default:
		b.reset()

		return fmt.Errorf("rabbitmq: %w", err)
	}
}

// channel returns the open confirming channel, connecting if needed.
func (b *Broker) channel() (*amqp.Channel, error) {
	if b.ch != nil && !b.ch.IsClosed() {
		return b.ch, nil
	}
	b.reset()

	conn, err := amqp.DialConfig(b.cfg.URL, amqp.Config{Dial: amqp.DefaultDial(b.cfg.DialTimeout)})
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	ch, err := conn.Channel()
	if err == nil {
		err = ch.Confirm(false)
	}
	if err != nil {
		_ = conn.Close()

		return nil, err
	}

	b.conn, b.ch = conn, ch
	b.returns = ch.NotifyReturn(make(chan amqp.Return, 64))

	return ch, nil
}

// reset closes and forgets the connection.
func (b *Broker) reset() {
	if b.conn != nil {
		_ = b.conn.Close()
	}
	b.conn, b.ch, b.returns = nil, nil, nil
	b.declared = map[string]bool{}
}

// Decode reads the event from a delivered message body.
func Decode(body []byte) (reactor.Event, error) {
	var e reactor.Event
	if err := json.Unmarshal(body, &e); err != nil || e.ID == "" {
		return reactor.Event{}, fmt.Errorf("%w: event message", reactor.ErrInvalid)
	}

	return e, nil
}
