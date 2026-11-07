# forge-reactor library

Go module: `github.com/r59q/forge-tools/forge-reactor/library`.
It depends on forge-core's `forgeapi` and on `github.com/rabbitmq/amqp091-go`
for the `rabbitmq` package only.

## Packages

| Package    | Use                                                                         |
| ---------- | --------------------------------------------------------------------------- |
| `reactor`  | Event envelope, subscriptions, fan-out, dedup, history, replay, dispatcher. |
| `webhook`  | GitHub and Forgejo signature checks and normalization. No I/O, no state.    |
| `poll`     | Forgejo branch-head polling through sessions the caller supplies.           |
| `sources`  | Managed webhook sources: generated secrets and forge webhook registration.  |
| `rabbitmq` | The production `reactor.Broker`: quorum queues and publisher confirms.     |

`reactor` knows nothing about RabbitMQ. A broker is three calls:

```go
type Broker interface {
	Prepare(ctx context.Context, subscription string) error // declare the queue
	Publish(ctx context.Context, m Message) error           // nil only once the broker took responsibility
	Remove(ctx context.Context, subscription string) error  // delete the queue
}
```

Another broker, including one of our own, plugs in by implementing it.

## Use

```go
store := &reactor.JSONStore{Save: writeFileAtomically} // or reactor.NewJSONStore(previous, save)
r, err := reactor.New(reactor.Config{Store: store})

_, err = r.CreateSubscription(ctx, reactor.Subscription{
	ID:     "ci",
	Filter: reactor.Filter{Types: []string{reactor.TypeBranchUpdated}, Repositories: []string{"team/app"}},
})

// Webhooks: verify against the raw body, then accept.
res, err := webhook.Parse(webhook.Source{ID: "forgejo", Forge: forgeauth.Forgejo, Connection: "fj", Secret: secret}, req.Header, body)
if err == nil && res.Input != nil {
	event, duplicate, err := r.Accept(ctx, *res.Input)
}

// Managed sources: a generated secret, and the webhook created on the forge.
m, err := sources.New(&sources.Manager{Store: &sources.MemoryStore{}, Sessions: open, PublicURL: "https://example.org/reactor"})
src, err := m.Create(ctx, sources.Request{
	ID: "app", Forge: forgeauth.GitHub, Connection: "gh", Repositories: []string{"team/app"}, Register: true,
})
res, err = webhook.Parse(src.Webhook(), req.Header, body)

// Polling, with sessions from forge-connector.
p := &poll.Poller{Reactor: r, Sessions: sessions, Targets: targets}
go p.Run(ctx)

// Hand deliveries to RabbitMQ until ctx ends.
b, err := rabbitmq.New(rabbitmq.Config{URL: amqpURL})
go r.Run(ctx, b)
```

`Accept`, `Publish`, `Observe`, `CreateSubscription`, `DeleteSubscription`
and `Replay` return only after the store committed. A `Store` must commit
each update durably and atomically; `JSONStore` does so when its `Save`
does. Errors wrap `reactor.ErrInvalid`, `ErrNotFound`, `ErrExists`,
`ErrOutOfRange` or `ErrBusy`.

See the [tool README](../README.md) for event types, delivery semantics
and limits.

## Build and test

Requires Go 1.27 or newer. From this directory:

```sh
go vet ./...
go test ./...
```

The RabbitMQ integration tests need a server and are skipped otherwise:

```sh
docker run -d --name rabbit -p 5672:5672 rabbitmq:4
FORGE_REACTOR_RABBITMQ_URL=amqp://guest:guest@localhost:5672/ go test ./rabbitmq/

# Persistence across a broker restart, in two phases:
FORGE_REACTOR_RABBITMQ_URL=... FORGE_REACTOR_RABBITMQ_PHASE=publish go test -run Persistence ./rabbitmq/
docker restart rabbit
FORGE_REACTOR_RABBITMQ_URL=... FORGE_REACTOR_RABBITMQ_PHASE=consume go test -run Persistence ./rabbitmq/
```

## License

[MIT](../../LICENSE)
