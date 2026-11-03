# Explorer service

Authenticated REST/gRPC access to explicitly configured local worktrees. Uses
`forge-core/servicekit`; no connector or other tool is needed for local content.

From this directory:

```sh
go mod tidy -diff
go build ./...
go vet ./...
go test -race ./...
buf generate
```

These commands need no Node, frontend build, or NudaUI access. API tests use local
fixtures and loopback listeners. Generated protobuf/gateway code is committed.

## Standalone application

Build the frontend once from `../frontend` with `npm ci && npm run build`, then:

```sh
FORGE_EXPLORER_TOKEN=local-development-token go run ./cmd/forge-explorer-service \
  --roots 'local=/absolute/worktree' --assets ../frontend/dist/app
```

Open `http://127.0.0.1:8090`, enter the same service token, and connect. Tokens stay
in page memory and are not stored in local/session storage or URLs. gRPC listens
on `127.0.0.1:9100`. The public HTML shell contains no repository content; content
APIs require `Authorization: Bearer <token>`. Use TLS at a reverse proxy for remote
access. All authenticated callers can read every configured root.

For embedding, configure `--roots 'old=/path/to/old;new=/path/to/new'` and open
`/embedding.html`. See the [frontend contract](../frontend/README.md).

| Flag | Environment | Default |
| --- | --- | --- |
| `--roots` | `FORGE_EXPLORER_ROOTS` | required; up to 32 `id=/absolute/path` mappings separated by `;` |
| `--assets` | `FORGE_EXPLORER_ASSETS` | `../frontend/dist/app` |
| `--http-listen` | `FORGE_EXPLORER_HTTP_LISTEN` | `127.0.0.1:8090` |
| `--grpc-listen` | `FORGE_EXPLORER_GRPC_LISTEN` | `127.0.0.1:9100` |
| `--request-timeout` | `FORGE_EXPLORER_REQUEST_TIMEOUT` | `10s` |
| `--token-file` | `FORGE_EXPLORER_TOKEN_FILE` | optional file, reread per call |
| `--token-env` | `FORGE_EXPLORER_TOKEN_ENV` | `FORGE_EXPLORER_TOKEN` |

IDs are 1–64 ASCII letters, digits, `_` or `-`. Duplicate IDs fail startup. Paths
containing `;` cannot be configured through this compact flag format. Only the
Linux service platform is currently supported. Missing assets return 404 while
backend APIs remain available.

## API

The [protobuf definition](proto/forge/explorer/v1/explorer.proto) is the source of
truth. REST uses protobuf JSON (`sourceVersion`, `expectedVersion`, `pathGlob`).
Every per-source request requires the `version` returned by `ListSources` as
`sourceVersion`; this is distinct from a file's content version.

| RPC | REST | Request / response |
| --- | --- | --- |
| `ListSources` | `GET /v1/explorer/sources` | `{sources:[...]}` includes capabilities and fixed limits |
| `Browse` | `GET /v1/explorer/sources/{source}/tree?sourceVersion=...&path=.` | `{directory:{source,version,entries,state}}` |
| `Read` | `GET /v1/explorer/sources/{source}/file?sourceVersion=...&path=src/main.go&expectedVersion=...` | `{file:{location,text}}`; file expectation is optional |
| `Search` | `POST /v1/explorer/sources/{source}/search` | JSON `{sourceVersion,text,pathGlob,files,limit}` → `{results:{matches,state}}` |

Search requires `sourceVersion` in its JSON body; full-body protobuf routes do not bind query fields. Empty/oversized queries,
invalid globs or result limits above 200 are invalid arguments. See the shared
[source policies and limits](../README.md); result states are never implicit.
RPC cancellation/deadlines stop work. A canceled transport may discard partial
results even when the library accumulated matches.

Errors use stable gRPC codes and sanitized messages: InvalidArgument (HTTP 400),
NotFound (404), PermissionDenied (403), FailedPrecondition/stale (400),
ResourceExhausted/large (429), Unimplemented/non-text (501), Unauthenticated (401).
Request bodies are capped at 1 MiB. Responses disable caching and sniffing.

## Asset packaging

From repository root:

```sh
docker build -f forge-explorer/service/Dockerfile -t forge-explorer-service .
```

The pinned Node build stage uses `npm ci` and committed source/lockfiles. It copies
only `dist/app` into the final image alongside the independently built Go binary.
The nonroot runtime has neither Node nor Git. Mount content read-only and set
`FORGE_EXPLORER_ROOTS=local=/content`. Container ports are 8080/9090. Builds never
fetch NudaUI; its selected source and license are committed. Go checks never
build frontend assets. The [cross-project browser test](../test-browser.mjs)
explicitly composes both projects and is separate from their independent checks.

The [service workflow](../../.github/workflows/forge-explorer-service.yml) builds
Linux amd64/arm64 images on pull requests without pushing. After its Go, protobuf
and browser checks pass on `master`, it publishes
`ghcr.io/r59q/forge-explorer-service` with `latest` and commit SHA tags. This
publishes the service and bundled standalone app. The frontend has a separate
[GitHub npm publishing job](../frontend/README.md).
