# forge-reactor

Forge events for other tools. forge-reactor receives GitHub and Forgejo
webhooks, polls Forgejo branches and accepts events published by other
tools. It stores each event once, fans it out to every matching
subscription and hands each delivery to RabbitMQ. Subscribers consume
their queue and acknowledge to RabbitMQ.

forge-reactor holds no business logic. It reports that something happened;
deciding what to do about it is up to the consumers.

- [library](library/): Go envelope, verification, polling, fan-out, replay and the RabbitMQ broker.
- [service](service/): REST and gRPC from one protobuf definition, plus native webhook ingress.

Forge credentials stay in [forge-connector](../forge-connector/); polling
asks it for sessions. forge-reactor owns only the webhook secrets.

## Events

| Type                        | Webhook (GitHub, Forgejo)                       | Poll (Forgejo) |
| --------------------------- | ----------------------------------------------- | -------------- |
| `forge.branch.updated`      | `push` that moves an existing branch            | head changed   |
| `forge.pull_request.opened` | `pull_request` with action `opened`             | —              |

Published events may have any type matching `[a-z0-9][a-z0-9._-]*`.

Verified deliveries of anything else are acknowledged and ignored, with the
reason in the response: pings, tag pushes, branch creation or deletion,
other pull request actions and other events. They create no event and are
not deduplicated. GitLab, schedules, derived events, outgoing webhooks and
reaction rules are not part of the first version.

### Envelope

Every event, whatever its origin, has the same shape:

```json
{
  "id": "0b7c6f4e-3d0e-4a8e-9f6c-2f1d5c3b7a10",
  "type": "forge.branch.updated",
  "origin": "webhook",
  "source": "forgejo",
  "forge": "forgejo",
  "connection": "fj",
  "repository": "team/app",
  "subject": "refs/heads/main",
  "occurredAt": "2026-01-02T03:04:05Z",
  "acceptedAt": "2026-01-02T03:04:06.123Z",
  "data": {"branch": "main", "before": "1a2b…", "after": "3c4d…", "forced": false, "pusher": "dev"},
  "native": {"event": "push", "delivery": "4f1c…", "payload": {"ref": "refs/heads/main", "…": "…"}}
}
```

- `origin` is `webhook`, `poll` or `published`. `source` is the webhook
  source or poll target ID.
- `forge` and `connection` come from trusted configuration, never from the
  payload. Published events may leave them, and `repository`, empty.
- `data` is the normalized body: `BranchUpdate` or `PullRequest` in the
  library. `native` keeps the delivery's event name, delivery ID and payload,
  minus any top-level `secret` field. Headers, signatures and tokens are
  never kept.
- `occurredAt` is the forge's time when the payload has one; `acceptedAt`
  is when forge-reactor stored the event.

## Delivery

**At least once.** A delivery is created, durably, in the same store
commit as its event. The dispatcher publishes it to the subscription's
RabbitMQ queue as a persistent message with the mandatory flag and waits for
the publisher confirm. Only the confirm marks it `broker_accepted`.

**Consumers acknowledge to RabbitMQ.** Each subscription has a durable
quorum queue, `forge-reactor.<subscription>`. Consume it with manual acks:

- `basic.ack` removes the message.
- `basic.nack` or `basic.reject` with requeue, a closed channel or a lost
  connection returns the message, which is redelivered with `redelivered`
  set.
- A reject without requeue drops it, unless you configured a dead-letter
  policy on the queue.

A message's AMQP `message-id` is the event ID, `type` is the event type and
the body is the envelope. Headers carry `forge-reactor-delivery`,
`forge-reactor-subscription`, `forge-reactor-attempt` and
`forge-reactor-replay`. The event ID is the same in every attempt and every
replay, so consumers deduplicate on it. Consumer processing is tracked only
by RabbitMQ; forge-reactor's history ends at broker acceptance.

**Retries.** A failed hand-off (broker down, refused, unroutable, timed out)
is retried with backoff doubling from 1 s to 5 min. After 20 attempts the
delivery is `failed` and stays in history with every attempt's error; a
replay can try again. Deliveries waiting for retry survive restarts.

**Order.** Within one subscription, deliveries are handed off in creation
order, but a retry can be overtaken by a later delivery and RabbitMQ may
redeliver out of order. There is no global order and no exactly-once
promise.

**Queue limits.** Messages stay queued while a subscriber is away, up to
RabbitMQ's limits for the queue. Deleting a subscription deletes its queue
and anything still in it, and cancels its pending deliveries.

## Deduplication

Webhook deliveries are deduplicated by source and native delivery ID
(`X-GitHub-Delivery`, `X-Forgejo-Delivery`) for 72 hours, persistently. A
duplicate returns the original event ID and creates nothing. Published
events are deduplicated the same way when the caller sends an idempotency
key. A webhook push and a poll observation of the same change are separate
events: they are not assumed to share an identity.

## Polling

A poll target names a forge-connector connection, a repository and
branches. The first reading of a branch is stored as its baseline and emits
nothing. An unchanged head emits nothing; a new head emits
`forge.branch.updated` with the previous and new commit, in the same store
commit that moves the baseline. A failed read, including a missing branch,
emits nothing and keeps the baseline, so a store failure or restart can at
worst repeat a change, never lose it.

Polling reports observations, not pushes: several pushes between two
readings are one change, a push undone before the next reading is not seen,
and `forced` and `pusher` are unknown.

## History and replay

Events and their deliveries, with every attempt, are kept for 7 days,
capped at the newest 1000 events; an event with a pending delivery stays
until it finishes. List them through the API.

A replay delivers retained events to one subscription again, either named
by ID or by acceptance period (at most 1000 events). Every event must match
the subscription's filter. A replay creates new deliveries for the same
events; it never creates an event. Unknown or expired event IDs fail with
not found, and periods starting before the retained history fail with out of
range rather than returning part of it. Replays are handed off in
acceptance order, subject to the order rules above.

## Limits

forge-reactor keeps its state in one JSON document, rewritten on every
change. It suits a few thousand retained events, which is what the
defaults allow. Beyond 10000 pending deliveries, new events and replays are
refused as busy until the broker catches up.

## License

[MIT](../LICENSE)
