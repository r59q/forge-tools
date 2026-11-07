# forge-reactor service

Forge events over REST and gRPC, generated from
[one protobuf definition](proto/forge/reactor/v1/reactor.proto), plus native
GitHub and Forgejo webhook ingress over HTTP. Deliveries go to RabbitMQ;
subscribers consume and acknowledge there. Requires Go 1.27 and a Unix host.
From this directory:

```sh
go build -o /tmp/forge-reactor-service ./cmd/forge-reactor-service
gofmt -l .
go vet ./...
go test -race ./...
```

The module uses relative replacements for its library, forge-connector/library
and forge-core; keep the repository layout. Unit tests need no broker or forge.
RabbitMQ integration tests live in the [library](../library/).

## Run

```sh
openssl rand -hex 32 > service-token
chmod 600 service-token
export FORGE_REACTOR_RABBITMQ_URL=amqp://reactor:password@127.0.0.1:5672/
/tmp/forge-reactor-service --token-file service-token --data-dir ./reactor-data \
  --config reactor.json
```

All environment names below have the prefix `FORGE_REACTOR_`. Flags override
environment values. Token and URL environment settings name variables, not
values. The service token is reread for rotation; the RabbitMQ URL is read once
at startup. Choose one source per secret.

| Flag                     | Environment suffix     | Default                                  |
| ------------------------ | ---------------------- | ---------------------------------------- |
| `--data-dir`             | `DATA_DIR`             | `./reactor-data`                         |
| `--config`               | `CONFIG`               | none: no configured webhooks or polls    |
| `--rabbitmq-url-file`    | `RABBITMQ_URL_FILE`    | none                                     |
| `--rabbitmq-url-env`     | `RABBITMQ_URL_ENV`     | variable `FORGE_REACTOR_RABBITMQ_URL`    |
| `--queue-prefix`         | `QUEUE_PREFIX`         | `forge-reactor.`                         |
| `--http-listen`          | `HTTP_LISTEN`          | `127.0.0.1:8093`                         |
| `--grpc-listen`          | `GRPC_LISTEN`          | `127.0.0.1:9103`                         |
| `--token-file`           | `TOKEN_FILE`           | none                                     |
| `--token-env`            | `TOKEN_ENV`            | variable `FORGE_REACTOR_TOKEN`           |
| `--connector-url`        | `CONNECTOR_URL`        | none; required for polls and registering |
| `--connector-token-file` | `CONNECTOR_TOKEN_FILE` | none                                     |
| `--connector-token-env`  | `CONNECTOR_TOKEN_ENV`  | variable `FORGE_REACTOR_CONNECTOR_TOKEN` |
| `--public-url`           | `PUBLIC_URL`           | none; required for registering webhooks  |
| `--request-timeout`      | `REQUEST_TIMEOUT`      | `30s`                                    |

The RabbitMQ user needs configure, write and read permission on queues
starting with the prefix. Subscribers need only read on their own queue.
The service declares and deletes queues as subscriptions come and go.

The data directory holds `reactor.json`, `sources.json` and a lock. Every change is written
to a private temporary file, fsynced and renamed. Use a local filesystem
supporting flock; one service owns the directory, and a second fails to start.
Back the directory up; it holds subscriptions, pending deliveries, poll
baselines, the deduplication window and managed webhook sources with their
secrets.

### Configuration

The configuration file is trusted operator input. Unknown fields are refused.

```json
{
  "webhooks": [
    {"id": "github", "forge": "github", "connection": "gh", "secretFile": "/run/secrets/github-hook"},
    {"id": "forgejo", "forge": "forgejo", "connection": "fj", "secretEnv": "FORGEJO_HOOK_SECRET",
     "repositories": ["team/app"]}
  ],
  "polls": [
    {"id": "fj-app", "connection": "fj", "repository": "team/app", "branches": ["main", "release/1"]}
  ],
  "pollInterval": "1m"
}
```

Each webhook source needs exactly one of `secretFile` or `secretEnv`. The
secret is reread for every delivery, so rotation needs no restart. `forge`
and `connection` are stamped on every event the source produces; the payload
cannot change them. `repositories`, if present, is the only repositories the
source accepts.

Poll targets are read with sessions from forge-connector, using
`--connector-url` and a connector client token with read access to the named
connections. Only Forgejo connections can be polled. The interval is at least
one second. A failed read is logged to stderr by target and branch, without
forge responses, and retried on the next round.

## Webhooks

Sources come from the configuration file or from the API. Point the forge at `POST /webhooks/{id}` on the HTTP listener, with
content type `application/json` and the source's secret. GitHub must sign with
`X-Hub-Signature-256`; Forgejo with `X-Forgejo-Signature`. Webhooks are not
authenticated by the service token: the signature over the raw body is the
authentication.

| Response | Meaning                                                              |
| -------- | -------------------------------------------------------------------- |
| 202      | `{"event":"…"}` stored, or `{"event":"…","duplicate":true}`          |
| 202      | `{"ignored":"…"}`: verified, but not an event forge-reactor emits    |
| 400      | verified but malformed: missing delivery ID, invalid JSON            |
| 401      | missing or invalid signature                                         |
| 403      | signed for this source but from another forge or repository          |
| 404      | unknown source                                                       |
| 405      | not `POST`                                                           |
| 413      | body over 2 MiB                                                      |
| 503      | delivery backlog full; the forge should redeliver later              |
| 500      | secret unreadable or storage failure; nothing was stored             |

Only a 202 means the event is durable. No other response stores an event.
Responses never echo the request. Which events are emitted and which are
ignored is listed in the [tool README](../README.md#events).

## API

Every request requires `Authorization: Bearer <service token>` (gRPC metadata
for gRPC). Responses are not cached.

| RPC                   | REST endpoint                                  |
| --------------------- | ---------------------------------------------- |
| `PublishEvent`        | `POST /v1/events`                              |
| `GetEvent`            | `GET /v1/events/{id}`                          |
| `ListEvents`          | `GET /v1/events?types=…&origins=…&pageSize=…`  |
| `CreateSubscription`  | `POST /v1/subscriptions`                       |
| `GetSubscription`     | `GET /v1/subscriptions/{id}`                   |
| `ListSubscriptions`   | `GET /v1/subscriptions`                        |
| `DeleteSubscription`  | `DELETE /v1/subscriptions/{id}`                |
| `ListDeliveries`      | `GET /v1/deliveries?subscription=…&status=…`   |
| `ReplayEvents`        | `POST /v1/subscriptions/{subscription}:replay` |
| `CreateWebhookSource` | `POST /v1/webhook-sources`                     |
| `ListWebhookSources`  | `GET /v1/webhook-sources`                      |
| `RotateWebhookSecret` | `POST /v1/webhook-sources/{id}:rotate`         |
| `DeleteWebhookSource` | `DELETE /v1/webhook-sources/{id}`              |

Create a subscription, then consume `forge-reactor.<id>` from RabbitMQ:

```json
{"id":"deploy","description":"Deploys main","filter":{"types":["forge.branch.updated"],"repositories":["team/app"]}}
```

Filter fields are `types`, `connections`, `repositories` and `origins`. An empty
field matches everything; values within a field are alternatives. Every
subscription whose filter matches gets its own delivery.

Publish an event from another tool. It has origin `published` and needs no
forge. Repeating a request with the same `idempotencyKey` within 72 hours
returns the first event with `duplicate: true`:

```json
{"type":"builds.finished","repository":"team/app","subject":"builds/42","data":{"ok":true},"idempotencyKey":"build-42"}
```

Replay retained events to one subscription by ID or by acceptance period:

```json
{"eventIds":["0b7c6f4e-…"]}
{"since":"2026-01-02T00:00:00Z","until":"2026-01-03T00:00:00Z"}
```

### Managed webhook sources

Create a webhook source at runtime. The reactor generates its secret and
returns it only from create and rotate; listings never include it. IDs used
in the configuration file are refused, and configured sources are listed as
`managed: false`.

```json
{"id":"app","forge":"github","connection":"gh","repositories":["team/app"],"register":true}
```

With `register`, the reactor also creates the webhook on every repository,
for pushes and pull requests, at `<public-url>/webhooks/<id>`. It uses a
forge-connector session for the connection, so the connection's token must
be allowed to manage repository webhooks. If any repository fails, the hooks
already made and the source are removed again, and the call fails with
`FAILED_PRECONDITION`; so does a missing public URL or connector.

Rotating sends the new secret to every registered webhook, and deleting
removes them. Repositories where that failed are returned in
`failedRepositories`: after a rotation they still have the old secret, so
rotate again; after a delete, remove the webhook on the forge by hand.

Delivery statuses are `pending`, `broker_accepted`, `failed` and `cancelled`.
Each delivery lists its hand-off attempts. Consumer processing is tracked only
by RabbitMQ.

`data` and `native.payload` are `google.protobuf.Struct`, so numbers are
doubles over REST and gRPC. RabbitMQ messages carry the stored JSON exactly.

Errors expose a status class, never raw input: invalid arguments, filters or
page tokens → `INVALID_ARGUMENT`/400; unknown events, subscriptions or
sources → `NOT_FOUND`/404; an existing subscription or source ID →
`ALREADY_EXISTS`/409; failed or unconfigured webhook registration →
`FAILED_PRECONDITION`/400; replay periods before retained history →
`OUT_OF_RANGE`/400; a full delivery backlog → `UNAVAILABLE`/503; missing or
wrong service token → `UNAUTHENTICATED`/401; storage failure →
`INTERNAL`/500.

REST request bodies are capped at 1 MiB. SIGINT/SIGTERM stop the poller and
dispatcher and drain the servers for five seconds. Listeners use plaintext; use
TLS endpoints for remote access, and expose only `/webhooks/` to forges.

## Docker

Build from the repository root:

```sh
docker build -f forge-reactor/service/Dockerfile -t forge-reactor-service .
docker run -p 127.0.0.1:8093:8080 -p 127.0.0.1:9103:9090 \
  -v reactor-data:/data -e FORGE_REACTOR_TOKEN -e FORGE_REACTOR_RABBITMQ_URL \
  forge-reactor-service
```

The image runs as UID 65532. Persist `/data`. The
[full-stack example](../../examples/fullstack/) runs it with RabbitMQ and
forge-connector.

## Regenerate

Generated files are committed in `reactorpb/`. To regenerate, install
[buf](https://buf.build/docs/installation) 1.74.0 and Go, then run from
this folder:

```sh
buf generate
go vet ./...
go test ./...
```

CI fails when the generated code is out of date.
