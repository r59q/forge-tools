# forge-checkouts service

Durable host-local working copies over REST and gRPC, generated from
[one protobuf definition](proto/checkouts.proto). Requires Go 1.27, Git
and a Unix host. From this directory:

```sh
go build -o /tmp/forge-checkouts-service ./cmd/forge-checkouts-service
gofmt -l .
go vet ./...
go test -race ./...
```

The module uses relative replacements for its library, forge-connector/library
and forge-core/forgeapi; keep the repository layout. Tests use local repositories
and a real connector proxy with a local Git HTTP server; no forge token is needed.

## Run

```sh
openssl rand -hex 32 > service-token
chmod 600 service-token
/tmp/forge-checkouts-service --token-file service-token --data-dir ./checkout-data
```

Connected creation additionally needs `--connector-url http://127.0.0.1:8081`
and `--connector-token-file connector-token`. Register that client with access
to the desired connections in the broker. It needs no token-issuance or push
permission. Local operations need no connector.

All environment names below have the prefix `FORGE_CHECKOUTS_`. Flags override
environment values. Token environment settings name variables, not token values;
token files/variables are reread for rotation. Choose one source per token.

| Flag | Environment suffix | Default |
| --- | --- | --- |
| `--data-dir` | `DATA_DIR` | `./checkout-data` |
| `--http-listen` | `HTTP_LISTEN` | `127.0.0.1:8088` |
| `--grpc-listen` | `GRPC_LISTEN` | `127.0.0.1:9098` |
| `--token-file` | `TOKEN_FILE` | none |
| `--token-env` | `TOKEN_ENV` | variable `FORGE_CHECKOUTS_TOKEN` |
| `--connector-url` | `CONNECTOR_URL` | none |
| `--connector-token-file` | `CONNECTOR_TOKEN_FILE` | none |
| `--connector-token-env` | `CONNECTOR_TOKEN_ENV` | variable `FORGE_CHECKOUTS_CONNECTOR_TOKEN` |
| `--request-timeout` | `REQUEST_TIMEOUT` | `5m` |

The data directory holds `checkouts.json`, process locks, worktrees in `copies/`
and connected bare caches in `repositories/`. Metadata updates use private files,
fsync and atomic rename. Use a local filesystem supporting flock; one service
owns the directory. Restart preserves IDs, locations, retention and revisions.
Interrupted operations become failed and keep their files. Keep this directory
private and back it up together with its files; moving it is not supported.

Removal deletes the selected worktree and registration only. Existing local
repositories remain caller-owned; managed caches remain for reuse. Do not delete
the data directory while local worktrees are registered: remove those checkouts
through the API first. Local paths refer to the service host/container, and
consumers must execute on a host that can access those paths. Local repositories
and their Git configuration must be trusted. A bearer token grants access to all
checkouts and readable local repositories on that service; use separate services
and OS isolation for separate trust boundaries.

## API

Every request requires `Authorization: Bearer <service token>` (gRPC metadata
for gRPC). REST ignores `Grpc-Metadata-*` authentication headers. Responses are
not cached. List/get read stored metadata; status reads the live checkout.

| RPC | REST endpoint |
| --- | --- |
| `CreateCheckout` | `POST /v1/checkouts` |
| `ListCheckouts` | `GET /v1/checkouts` |
| `GetCheckout` | `GET /v1/checkouts/{id}` |
| `RetainCheckout` | `POST /v1/checkouts/{id}/retain` with `{}` |
| `RemoveCheckout` | `DELETE /v1/checkouts/{id}?discard=false` |
| `InspectCheckout` | `GET /v1/checkouts/{id}/status` |
| `ReadDiff` | `GET /v1/checkouts/{id}/diff` |

Create from a local repository:

```json
{"repository":{"localPath":"/home/me/project"},"reference":"main"}
```

Or from a connector connection:

```json
{"repository":{"source":"connection-id","path":"owner/repo"},"reference":"v1.0"}
```

Creation returns a stable ID, repository identity, requested reference, resolved
commit, lifecycle state, RFC 3339 creation time and absolute location. Checkouts
start detached. Status reports current revision/branch and paths with separate
Git porcelain `index` and `worktree` codes; `??` means untracked, `!!` ignored.
Diff returns `staged` and `unstaged` bytes, base64 in JSON, each capped at 8 MiB.
gRPC clients reading large diffs must raise their receive limit to 17 MiB.

Retain marks an intent to keep the checkout; all checkouts persist until
explicit removal. Removal refuses uncommitted, untracked and ignored files
unless `discard=true`. Stop concurrent writers first. No automatic commit, push,
expiry, command execution, submodule initialization or LFS download is performed.

Errors expose a status class, never raw input or upstream error text:
invalid arguments → `INVALID_ARGUMENT`/400; missing resources → `NOT_FOUND`/404;
dirty or conflicting state → `FAILED_PRECONDITION`/400; output limit →
`RESOURCE_EXHAUSTED`/429; missing/wrong service token → `UNAUTHENTICATED`/401;
denied connector access → `PERMISSION_DENIED`/403; Git/storage/connector failure →
`UNAVAILABLE`/503. Cancellation and deadlines use standard gRPC mappings.

REST request bodies are capped at 1 MiB and gRPC requests at 20 MiB. The request
timeout includes waiting for the manager. SIGINT/SIGTERM drain for five seconds
before closing connections. Listeners use plaintext; use TLS endpoints for
remote access.

## Docker

Build from the repository root:

```sh
docker build -f forge-checkouts/service/Dockerfile -t forge-checkouts-service .
docker run -p 127.0.0.1:8088:8080 -p 127.0.0.1:9098:9090 \
  -v checkout-data:/data -e FORGE_CHECKOUTS_TOKEN forge-checkouts-service
```

The image includes Git and CA certificates and runs as UID 65532. Persist `/data`.
Mount trusted local repositories read/write if creating local worktrees: Git must
register them in the source repository. The [full-stack example](../../examples/fullstack/)
provisions connector access and a persistent volume.

## Upgrade from forge-workspaces

The tool is now `forge-checkouts`. Update imports to `forge-checkouts/library/checkouts`,
RPC names to `CreateCheckout`, `ListCheckouts`, and the matching checkout methods,
and REST paths to `/v1/checkouts`. Environment variables use `FORGE_CHECKOUTS_`;
management's registration variables use `FORGE_MANAGEMENT_CHECKOUTS_`.

Stop the old service before starting the renamed one. Keep the original data
root at the same absolute path, using `--data-dir` explicitly if it was the old
`./workspace-data` default. When `checkouts.json` is absent, the store reads
`workspaces.json` and converts its metadata on the first successful update.
IDs, retained state, revisions and files stay in place. The old metadata file
is kept as a backup; subsequent reads use `checkouts.json`.

For Docker, mount the existing volume at `/data`. Both Compose examples accept
`FORGE_CHECKOUTS_DATA_VOLUME` to select an existing volume, such as
`fullstack_workspaces-data`. Keep the old Compose project name to reuse the broker
volume. Change `FORGE_WORKSPACES_` keys in `.env` to `FORGE_CHECKOUTS_`, preserving
their values, and rerun client registration. Preserve the volume while upgrading.

## Regenerate

Install `protoc` and plugins `protoc-gen-go@v1.36.10`,
`protoc-gen-go-grpc@v1.5.1` and `protoc-gen-grpc-gateway@v2.27.3` on PATH.
Set `GOOGLEAPIS_DIR` to a checkout of `googleapis/googleapis` at
`ff8fd20f3840386e12f4f69750eff6dc99f6e1b7`. From this directory:

```sh
protoc -I proto -I "$GOOGLEAPIS_DIR" \
  --go_out=checkoutspb --go_opt=paths=source_relative \
  --go-grpc_out=checkoutspb --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=checkoutspb --grpc-gateway_opt=paths=source_relative \
  proto/checkouts.proto
```

[MIT](../../LICENSE)
