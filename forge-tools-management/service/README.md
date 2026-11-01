# forge-tools-management service

Read-only repository inspection over HTTP and gRPC. The browser UI is
server-rendered HTML with CSS; no JavaScript, browser storage or frontend
build is needed. It uses the [management library](../library/) and the existing
[forge-repositories gRPC API](../../forge-repositories/service/).

## Build and test

From this folder, with Go 1.27 or newer:

```sh
go build -o /tmp/forge-tools-management-service ./cmd/forge-tools-management-service
gofmt -l .
go vet ./...
go test -race ./...
```

Keep the repository layout for relative module replacements. Tests need no
live credentials. They use local fake forges and the real repositories API,
and cover both transports, the web UI, source identity, listing states,
credential separation, rotation, access protection and shutdown.

## Configure and run

Generate a management operator token and start without any connected tools:

```sh
umask 077
openssl rand -hex 32 > /tmp/management-operator-token
/tmp/forge-tools-management-service --token-file /tmp/management-operator-token
```

Open `http://127.0.0.1:8083/`. At the browser authentication prompt, enter
username `operator` and the management token as password. The page shows
**No repository capability configured**. Basic authentication is kept by the
browser; close the browser session to sign out. Use a trusted browser profile.

To connect an existing repositories service on the same machine:

```sh
/tmp/forge-tools-management-service \
  --token-file /tmp/management-operator-token \
  --repositories-address 127.0.0.1:9092 \
  --repositories-token-file /run/secrets/repositories-service-token \
  --repositories-insecure=true
```

The backend token is the repositories service's caller token, never a forge
token. Management must use a distinct operator token; matching values are
rejected at startup and after rotation. Both token sources are reread for
every request. Do not pass token values in flags, URLs or configuration files.

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--http-listen` | `FORGE_MANAGEMENT_HTTP_LISTEN` | `127.0.0.1:8083` |
| `--grpc-listen` | `FORGE_MANAGEMENT_GRPC_LISTEN` | `127.0.0.1:9093` |
| `--token-file` | `FORGE_MANAGEMENT_TOKEN_FILE` | none |
| `--token-env` | `FORGE_MANAGEMENT_TOKEN_ENV` | `FORGE_MANAGEMENT_TOKEN` |
| `--repositories-address` | `FORGE_MANAGEMENT_REPOSITORIES_ADDRESS` | none |
| `--repositories-token-file` | `FORGE_MANAGEMENT_REPOSITORIES_TOKEN_FILE` | none |
| `--repositories-token-env` | `FORGE_MANAGEMENT_REPOSITORIES_TOKEN_ENV` | `FORGE_MANAGEMENT_REPOSITORIES_TOKEN` when connected |
| `--repositories-insecure` | `FORGE_MANAGEMENT_REPOSITORIES_INSECURE` | `false` |
| `--request-timeout` | `FORGE_MANAGEMENT_REQUEST_TIMEOUT` | `30s` |

Flags override environment settings. Choose one file or environment token
source for each credential. Token environment settings name a variable;
they do not contain a token value. Invalid settings fail startup without
echoing input. Backend reachability is checked when listing, so a disconnected
backend starts and displays a failure rather than an unconfigured state.

Backend gRPC uses TLS and system trust roots by default. Set
`--repositories-insecure=true` only for a trusted local network, because the
repositories service's own listener is plaintext. For remote use, expose it
through a TLS endpoint. Management's listeners are plaintext on loopback;
put remote HTTP and gRPC access behind TLS endpoints preserving Authorization.

All authenticated management operators see the union visible to the backend's
configured credentials. There is no per-operator forge identity or additional
permission grant. Choose upstream credentials and operator access accordingly.
No mutations or unrelated tools are invoked. Repository text is escaped by
`html/template`; URLs are excluded from management results. Responses are
`no-store` with a restrictive Content Security Policy. The service does not
log requests, responses or backend errors; configure any reverse proxy to
omit credentials too. SIGINT/SIGTERM drain requests for up to five seconds.

## API

The [proto](proto/management.proto) defines the sole capability:

| RPC | REST endpoint | Authentication |
| --- | --- | --- |
| `Management.ListRepositories(Empty)` | `GET /v1/repositories` | `Authorization: Bearer <operator token>` |

The browser routes `GET /` and `GET /style.css` require Basic authentication.
REST and gRPC require Bearer authentication; they ignore forwarded identity
and `Grpc-Metadata-*` headers. The operator token is never rendered in HTML.

```sh
curl -fsS -H "Authorization: Bearer $(cat /tmp/management-operator-token)" \
  http://127.0.0.1:8083/v1/repositories
```

JSON uses protobuf lowerCamelCase names and omits default values: `{}` means
unconfigured; `{"configured":true}` means a successful empty listing.
Successful nonempty responses add `repositories`, whose entries contain
`sourceId`, `forge`, `host`, `path`, `description` and `visibility`.
`sourceId` plus `path` is the repository identity. There is no pagination in
management; the existing repository capability supplies the complete listing.
The backend gRPC receive limit is 4 MiB; larger listings fail visibly rather
than truncating results.

| Cause | gRPC | HTTP |
| --- | --- | --- |
| Missing, wrong or unreadable operator credential | `UNAUTHENTICATED` | 401 |
| Repository service failure, access denial, rejected backend/forge credential, malformed identity or oversized result | `UNAVAILABLE` | 503 |
| Request deadline | `DEADLINE_EXCEEDED` | 504 |
| Canceled request | `CANCELED` | 499 |

Failures expose no backend error details or partial results. The browser uses
a failed-listing page with HTTP 503 for listing errors. Refresh reruns the
listing; stale data is not retained.

## Docker

Build from the repository root:

```sh
docker build -f forge-tools-management/service/Dockerfile -t forge-tools-management-service .
docker run --rm -p 127.0.0.1:8083:8080 -p 127.0.0.1:9093:9090 \
  -e FORGE_MANAGEMENT_TOKEN forge-tools-management-service
```

Export a generated `FORGE_MANAGEMENT_TOKEN` first, or mount a token file and
set `FORGE_MANAGEMENT_TOKEN_FILE`. The image runs as non-root. See the
[Compose example](../../examples/management/) for a complete setup.

## Regenerate

Generated files are committed in `managementpb/`. Install protoc 33.0 and:

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.10
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.27.3
```

Check out `googleapis/googleapis` at
`ff8fd20f3840386e12f4f69750eff6dc99f6e1b7` and set `GOOGLEAPIS_DIR` to its
root. From this folder, with protoc's bundled includes available:

```sh
protoc -I proto -I "$GOOGLEAPIS_DIR" \
  --go_out=managementpb --go_opt=paths=source_relative \
  --go-grpc_out=managementpb --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=managementpb --grpc-gateway_opt=paths=source_relative \
  proto/management.proto
go vet ./...
go test ./...
```
