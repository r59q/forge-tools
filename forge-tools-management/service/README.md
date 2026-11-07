# forge-tools-management service

Forge connection and webhook management, and repository and issue
inspection, over HTTP and gRPC.
The browser UI is server-rendered HTML with CSS and plain forms; no
JavaScript, browser storage or frontend build is needed. It uses the
[management library](../library/), the
[forge-connector broker admin API](../../forge-connector/broker/), the
[forge-repositories gRPC API](../../forge-repositories/service/), the
[forge-issues gRPC API](../../forge-issues/service/) and the
[forge-reactor gRPC API](../../forge-reactor/service/).

## Build and test

From this folder, with Go 1.27 or newer:

```sh
go build -o /tmp/forge-tools-management-service ./cmd/forge-tools-management-service
gofmt -l .
go vet ./...
go test -race ./...
```

Keep the repository layout for relative module replacements. Tests need no
live credentials. They run the real broker, repositories and issues APIs against
fake forges, and cover both transports, the web UI and its forms,
service client registration, listing states, credential separation, rotation, access and
cross-origin protection, and shutdown.

## Configure and run

For a complete stack, use the [Compose example](../../examples/fullstack/).

Generate a management operator token and start without any connected tools:

```sh
umask 077
openssl rand -hex 32 > /tmp/management-operator-token
/tmp/forge-tools-management-service --token-file /tmp/management-operator-token
```

Open `http://127.0.0.1:8083/`. At the browser authentication prompt, enter
username `operator` and the management token as password. Basic
authentication is kept by the browser; close the browser session to sign
out. Use a trusted browser profile.

To manage connections, point management at a broker with its admin token.
To list repositories or issues, also point it at a repositories or issues
service. The proxy, repositories and issues client tokens are registered in
the broker; see below:

```sh
/tmp/forge-tools-management-service \
  --token-file /tmp/management-operator-token \
  --connector-address 127.0.0.1:9090 \
  --connector-admin-token-file /run/secrets/broker-admin-token \
  --connector-insecure=true \
  --proxy-token-file /run/secrets/proxy-token \
  --repositories-connector-token-file /run/secrets/repositories-connector-token \
  --repositories-address 127.0.0.1:9092 \
  --repositories-token-file /run/secrets/repositories-token \
  --repositories-insecure=true \
  --issues-connector-token-file /run/secrets/issues-connector-token \
  --issues-address 127.0.0.1:9094 \
  --issues-token-file /run/secrets/issues-token \
  --issues-insecure=true
```

### Register service tokens

The proxy, the repositories service and the issues service each need a
broker client token. You
choose these tokens and give them to the services and to management.
Management registers them in the broker at startup, each granted every
connection, including ones added later:

| Setting                            | Broker client                                   |
| ---------------------------------- | ----------------------------------------------- |
| `--proxy-token-*`                  | `forge-connector-proxy`, may issue forge tokens |
| `--repositories-connector-token-*` | `forge-repositories`                            |
| `--issues-connector-token-*`       | `forge-issues`                                  |
| `--checkouts-connector-token-*`    | `forge-checkouts`                               |
| `--actions-connector-token-*`      | `forge-actions`                                 |
| `--reactor-connector-token-*`      | `forge-reactor`                                 |
| `--code-connector-token-*`         | `forge-code`                                    |
| `--changes-connector-token-*`      | `forge-changes`                                 |
| `--review-connector-token-*`       | `forge-review`                                  |

If the broker does not yet accept a token, management replaces the client of
that name with one using the token, so changing a token and restarting
management rotates it. Tokens need at least 32 printable characters and
must differ from each other and from the operator and admin tokens. Startup
fails otherwise. Registration retries in the background until the broker
answers, while the UI is already served; until then, those services return
errors. Without a token setting, nothing is registered.

### Settings

| Flag                                  | Environment variable                                 | Default                                                                 |
| ------------------------------------- | ---------------------------------------------------- | ----------------------------------------------------------------------- |
| `--http-listen`                       | `FORGE_MANAGEMENT_HTTP_LISTEN`                       | `127.0.0.1:8083`                                                        |
| `--grpc-listen`                       | `FORGE_MANAGEMENT_GRPC_LISTEN`                       | `127.0.0.1:9093`                                                        |
| `--token-file`                        | `FORGE_MANAGEMENT_TOKEN_FILE`                        | none                                                                    |
| `--token-env`                         | `FORGE_MANAGEMENT_TOKEN_ENV`                         | `FORGE_MANAGEMENT_TOKEN`                                                |
| `--connector-address`                 | `FORGE_MANAGEMENT_CONNECTOR_ADDRESS`                 | none                                                                    |
| `--connector-admin-token-file`        | `FORGE_MANAGEMENT_CONNECTOR_ADMIN_TOKEN_FILE`        | none                                                                    |
| `--connector-admin-token-env`         | `FORGE_MANAGEMENT_CONNECTOR_ADMIN_TOKEN_ENV`         | `FORGE_MANAGEMENT_CONNECTOR_ADMIN_TOKEN` when connected                 |
| `--connector-insecure`                | `FORGE_MANAGEMENT_CONNECTOR_INSECURE`                | `false`                                                                 |
| `--proxy-token-file`                  | `FORGE_MANAGEMENT_PROXY_TOKEN_FILE`                  | none                                                                    |
| `--proxy-token-env`                   | `FORGE_MANAGEMENT_PROXY_TOKEN_ENV`                   | `FORGE_MANAGEMENT_PROXY_TOKEN`, if set, when connected                  |
| `--repositories-connector-token-file` | `FORGE_MANAGEMENT_REPOSITORIES_CONNECTOR_TOKEN_FILE` | none                                                                    |
| `--repositories-connector-token-env`  | `FORGE_MANAGEMENT_REPOSITORIES_CONNECTOR_TOKEN_ENV`  | `FORGE_MANAGEMENT_REPOSITORIES_CONNECTOR_TOKEN`, if set, when connected |
| `--repositories-address`              | `FORGE_MANAGEMENT_REPOSITORIES_ADDRESS`              | none                                                                    |
| `--repositories-token-file`           | `FORGE_MANAGEMENT_REPOSITORIES_TOKEN_FILE`           | none                                                                    |
| `--repositories-token-env`            | `FORGE_MANAGEMENT_REPOSITORIES_TOKEN_ENV`            | `FORGE_MANAGEMENT_REPOSITORIES_TOKEN` when connected                    |
| `--repositories-insecure`             | `FORGE_MANAGEMENT_REPOSITORIES_INSECURE`             | `false`                                                                 |
| `--issues-connector-token-file`       | `FORGE_MANAGEMENT_ISSUES_CONNECTOR_TOKEN_FILE`       | none                                                                    |
| `--issues-connector-token-env`        | `FORGE_MANAGEMENT_ISSUES_CONNECTOR_TOKEN_ENV`        | `FORGE_MANAGEMENT_ISSUES_CONNECTOR_TOKEN`, if set, when connected       |
| `--checkouts-connector-token-file`    | `FORGE_MANAGEMENT_CHECKOUTS_CONNECTOR_TOKEN_FILE`    | none                                                                    |
| `--checkouts-connector-token-env`     | `FORGE_MANAGEMENT_CHECKOUTS_CONNECTOR_TOKEN_ENV`     | `FORGE_MANAGEMENT_CHECKOUTS_CONNECTOR_TOKEN`, if set, when connected    |
| `--reactor-connector-token-file`      | `FORGE_MANAGEMENT_REACTOR_CONNECTOR_TOKEN_FILE`      | none                                                                    |
| `--reactor-connector-token-env`       | `FORGE_MANAGEMENT_REACTOR_CONNECTOR_TOKEN_ENV`       | `FORGE_MANAGEMENT_REACTOR_CONNECTOR_TOKEN`, if set, when connected      |
| `--code-connector-token-file`         | `FORGE_MANAGEMENT_CODE_CONNECTOR_TOKEN_FILE`         | none                                                                    |
| `--code-connector-token-env`          | `FORGE_MANAGEMENT_CODE_CONNECTOR_TOKEN_ENV`          | `FORGE_MANAGEMENT_CODE_CONNECTOR_TOKEN`, if set, when connected         |
| `--changes-connector-token-file`      | `FORGE_MANAGEMENT_CHANGES_CONNECTOR_TOKEN_FILE`      | none                                                                    |
| `--changes-connector-token-env`       | `FORGE_MANAGEMENT_CHANGES_CONNECTOR_TOKEN_ENV`       | `FORGE_MANAGEMENT_CHANGES_CONNECTOR_TOKEN`, if set, when connected      |
| `--review-connector-token-file`       | `FORGE_MANAGEMENT_REVIEW_CONNECTOR_TOKEN_FILE`       | none                                                                    |
| `--review-connector-token-env`        | `FORGE_MANAGEMENT_REVIEW_CONNECTOR_TOKEN_ENV`        | `FORGE_MANAGEMENT_REVIEW_CONNECTOR_TOKEN`, if set, when connected       |
| `--issues-address`                    | `FORGE_MANAGEMENT_ISSUES_ADDRESS`                    | none                                                                    |
| `--issues-token-file`                 | `FORGE_MANAGEMENT_ISSUES_TOKEN_FILE`                 | none                                                                    |
| `--issues-token-env`                  | `FORGE_MANAGEMENT_ISSUES_TOKEN_ENV`                  | `FORGE_MANAGEMENT_ISSUES_TOKEN` when connected                          |
| `--issues-insecure`                   | `FORGE_MANAGEMENT_ISSUES_INSECURE`                   | `false`                                                                 |
| `--reactor-address`                   | `FORGE_MANAGEMENT_REACTOR_ADDRESS`                   | none                                                                    |
| `--reactor-token-file`                | `FORGE_MANAGEMENT_REACTOR_TOKEN_FILE`                | none                                                                    |
| `--reactor-token-env`                 | `FORGE_MANAGEMENT_REACTOR_TOKEN_ENV`                 | `FORGE_MANAGEMENT_REACTOR_TOKEN` when connected                         |
| `--reactor-insecure`                  | `FORGE_MANAGEMENT_REACTOR_INSECURE`                  | `false`                                                                 |
| `--request-timeout`                   | `FORGE_MANAGEMENT_REQUEST_TIMEOUT`                   | `30s`                                                                   |

Flags override environment settings. Choose one file or environment token
source for each credential. Token environment settings name a variable;
they do not contain a token value. Service client tokens are read at
startup; all other tokens are reread for every request.
The operator token must differ from the backend tokens; matching values are
rejected at startup and after rotation. Invalid settings fail startup
without echoing input. Backends are contacted per request, so a stopped
backend shows a failure rather than an unconfigured state.

Backend gRPC uses TLS and system trust roots by default. Set the
`--*-insecure=true` flags only on a trusted local network, because the
broker, repositories and issues listeners are plaintext. For remote use, expose them
through TLS endpoints. Management's listeners are plaintext on loopback; put
remote HTTP and gRPC access behind TLS endpoints preserving Authorization.

## Security

The broker admin token can read and change every connection, so treat the
management operator token as equally sensitive. All operators see what the
connected accounts can see; management grants no forge permissions.

Forge tokens are write-only: they are sent to the broker and never returned,
rendered or logged. Webhook secrets are shown once, in the response to adding
or rotating a source; listings never include them. Forms that change state reject cross-origin browser
requests. Successful changes redirect, and failures show fixed messages
without upstream details. Page text is escaped by `html/template`.
Responses are `no-store` with a restrictive Content Security Policy. The
service does not log requests, responses or backend errors; configure any
reverse proxy to omit credentials and form bodies too. SIGINT/SIGTERM drain
requests for up to five seconds.

## API

The [proto](proto/forge/management/v1/management.proto) defines
`forge.management.v1.ManagementService` and is the source of truth. Every RPC
needs `Authorization: Bearer <operator token>`.

| RPC                   | REST endpoint                                                            |
| --------------------- | ------------------------------------------------------------------------ |
| `ListRepositories`    | `GET /v1/repositories`                                                   |
| `ListIssues`          | `GET /v1/issues?scope=&state=&source=&repository=`                       |
| `ListConnections`     | `GET /v1/connections`                                                    |
| `AddConnection`       | `POST /v1/connections` with `{"forge","host","name","credential"}`       |
| `TestConnection`      | `POST /v1/connections/{id}/test`                                         |
| `RotateCredential`    | `POST /v1/connections/{id}/credential` with `{"credential"}`             |
| `RemoveConnection`    | `DELETE /v1/connections/{id}`                                            |
| `ListWebhooks`        | `GET /v1/webhooks`                                                       |
| `AddWebhook`          | `POST /v1/webhooks` with `{"id","connection","repositories","register"}` |
| `RotateWebhookSecret` | `POST /v1/webhooks/{id}:rotate`                                          |
| `RemoveWebhook`       | `DELETE /v1/webhooks/{id}`                                               |
| `ListEvents`          | `GET /v1/events?source=&type=&repository=&pageToken=`                    |
| `GetEvent`            | `GET /v1/events/{id}`                                                    |

The browser routes use HTTP Basic authentication: `GET /`, `GET /issues`
with the same query as `ListIssues`, `GET /connections`,
`POST /connections`, and `POST /connections/{id}/test`, `/credential` and
`/delete`, `GET /webhooks`, `POST /webhooks`, `POST /webhooks/{id}/rotate`
and `/delete`, and `GET /events` and `GET /events/{id}`. REST and gRPC ignore
forwarded identity and `Grpc-Metadata-*` headers. The operator token is never rendered in HTML.

```sh
api() { curl -fsS -H "Authorization: Bearer $(cat /tmp/management-operator-token)" "$@"; }
read -rs TOKEN
api localhost:8083/v1/connections \
  -d "{\"forge\":\"gitlab\",\"host\":\"gitlab.example.com\",\"credential\":\"$TOKEN\"}"
unset TOKEN
api localhost:8083/v1/repositories
api 'localhost:8083/v1/issues?state=all&repository=group/sub/project'
```

JSON uses protobuf lowerCamelCase names and omits default values.
`{"configured":true}` means a successful empty listing; `{}` means that
capability is not configured. Repository entries contain `sourceId`, `forge`,
`host`, `path`, `description` and `visibility`; `sourceId` is the connection
ID, and with `path` identifies a repository. `ListIssues` lists the issues
assigned to (`scope=assigned`, the default) or created by (`scope=created`)
each connected account, or one repository's issues when `repository` is set;
`scope` must then be empty. `state` is `open` (default), `closed` or `all`.
Issue entries contain `sourceId`, `forge`, `host`, `repository`, `number`,
`title`, `state`, `author`, `assignees`, `labels`, `comments` and
`updatedAt`, but no bodies or URLs. Connections contain `id`,
`name`, `forge`, `baseUrl`, `login`, `status` (`ok`, `invalid` or `revoked`)
and `checkedAt`. There is no pagination. The backend gRPC receive limit is
4 MiB; larger listings fail visibly rather than truncating results.

| Cause                                                                        | gRPC                  | HTTP |
| ---------------------------------------------------------------------------- | --------------------- | ---- |
| Missing, wrong or unreadable operator credential                             | `UNAUTHENTICATED`     | 401  |
| Bad input, such as an unknown issue scope, or the forge rejected a new token | `INVALID_ARGUMENT`    | 400  |
| `TestConnection`: the forge rejected the stored token or was unreachable     | `FAILED_PRECONDITION` | 400  |
| Connection not found                                                         | `NOT_FOUND`           | 404  |
| No connector configured                                                      | `UNIMPLEMENTED`       | 501  |
| Backend failure, rejected backend credential or oversized result             | `UNAVAILABLE`         | 503  |
| Request deadline                                                             | `DEADLINE_EXCEEDED`   | 504  |
| Canceled request                                                             | `CANCELED`            | 499  |

Failures expose no backend error details or partial results.

## Docker

Build from the repository root:

```sh
docker build -f forge-tools-management/service/Dockerfile -t forge-tools-management-service .
docker run --rm -p 127.0.0.1:8083:8080 -p 127.0.0.1:9093:9090 \
  -e FORGE_MANAGEMENT_TOKEN forge-tools-management-service
```

Export a generated `FORGE_MANAGEMENT_TOKEN` first, or mount a token file and
set `FORGE_MANAGEMENT_TOKEN_FILE`. The image runs as non-root. See the
[Compose example](../../examples/fullstack/) for a complete setup.

## Regenerate

Generated files are committed in `managementpb/`. To regenerate, install
[buf](https://buf.build/docs/installation) 1.74.0 and Go, then run from
this folder:

```sh
buf generate
go vet ./...
go test ./...
```

CI fails when the generated code is out of date.

## Webhooks

Configure `--reactor-address` / `FORGE_MANAGEMENT_REACTOR_ADDRESS` with a
forge-reactor gRPC endpoint, and its service token with `--reactor-token-file`
or `--reactor-token-env`. The default credential variable is
`FORGE_MANAGEMENT_REACTOR_TOKEN`. `--reactor-insecure=true` enables plaintext
on a trusted network; TLS is the default.

The Webhooks page lists every source of the reactor. Configured sources are
read-only. Add a source for a GitHub or Forgejo connection, optionally limited
to repositories; the page then shows its payload URL and secret once. With
**Register**, the reactor also creates the webhook on each repository; it
needs `--public-url` and a connector, and the connection's token must be
allowed to manage repository webhooks. Rotate replaces the secret on the
reactor and on registered webhooks. Remove deletes the source and its
registered webhooks. Repositories the forge did not update are listed. Each
row shows the source's latest retained event.

The Events page lists the reactor's retained events, 50 per page, newest
first, filtered by source, type or repository. An event's page shows its
fields, data, native payload and deliveries to subscriptions. Listings over
REST and gRPC leave out native payloads; `GetEvent` includes them.

## Pipeline browsing

Configure `--actions-address` / `FORGE_MANAGEMENT_ACTIONS_ADDRESS` with a
forge-actions gRPC endpoint. Set `--actions-token-file` or `--actions-token-env`
(`FORGE_MANAGEMENT_ACTIONS_TOKEN_FILE` / `FORGE_MANAGEMENT_ACTIONS_TOKEN_ENV`).
The default credential variable is `FORGE_MANAGEMENT_ACTIONS_TOKEN`.
`--actions-insecure=true` / `FORGE_MANAGEMENT_ACTIONS_INSECURE=true` enables
plaintext on a trusted network; TLS is the default.

`--actions-connector-token-file` / `--actions-connector-token-env` registers
the actions service's connector client when the broker is configured. Their
variables are `FORGE_MANAGEMENT_ACTIONS_CONNECTOR_TOKEN_FILE` and
`FORGE_MANAGEMENT_ACTIONS_CONNECTOR_TOKEN_ENV`; the default credential variable
is `FORGE_MANAGEMENT_ACTIONS_CONNECTOR_TOKEN`. It is optional unless explicitly
configured. Operator and backend credentials must differ.

The Pipelines link under a repository opens `/pipelines?source=&repository=`.
Browse executions, open a job, and read its log. Logs are escaped text;
missing/expired logs and truncation are explicit. Refresh to read current state.

REST and gRPC expose `ListPipelines`, `GetPipeline`, `ListJobs`, `GetJob` and
`ReadJobLog` on the same routes as [forge-actions](../../forge-actions/service/).
List responses include `configured`. Detail operations return `UNIMPLEMENTED`
when the capability is absent. These routes accept the operator bearer token;
the browser uses the existing Basic authentication. Backend failures never
become empty successful listings.
