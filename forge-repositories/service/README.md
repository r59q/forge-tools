# forge-repositories service

Serves the [forge-repositories library](../library/) over gRPC and generated
REST routes. It reads every configured source at once, so listing owner
`mrbean` returns their repositories from GitHub, GitLab and Forgejo together.
Sources are [forge-connector](../../forge-connector/) proxy connections or
direct forge accounts.

## Build and test

Requires Go 1.27 or newer. From this directory:

```sh
go build -o /tmp/forge-repositories-service ./cmd/forge-repositories-service
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
```

The module uses relative replacements for `../library` and
`../../forge-core/forgeapi`; keep that checkout layout. Tests use local fake
forges and synthetic tokens. They cover both transports, authentication,
nested GitLab paths, updates, error mapping, configuration and shutdown.

Generated Go files are committed in `repositoriespb/`; see
[Regenerate](#regenerate) when editing the proto.

## Configure

The service reads a JSON file that says where each source is and where its
token lives. It never holds tokens itself:

```json
{
  "proxy": { "url": "http://127.0.0.1:8081", "tokenEnv": "FORGE_PROXY_CLIENT_TOKEN" },
  "sources": [
    { "kind": "github", "connection": "<github-connection-id>" },
    { "kind": "gitlab", "connection": "<gitlab-connection-id>" },
    { "id": "codeberg", "kind": "forgejo", "baseUrl": "https://codeberg.org/api/v1", "tokenFile": "/run/secrets/codeberg" }
  ]
}
```

- `proxy` is optional. `url` is the forge-connector proxy. `tokenEnv` or
  `tokenFile` holds a broker client token scoped to every listed connection.
- A source with `connection` reads `<proxy url>/c/{connection}` with the proxy
  token. Its `id` defaults to the connection ID.
- A source with `baseUrl` reads the forge API directly, with its own
  `tokenEnv` or `tokenFile`. `baseUrl` includes the API path, such as
  `https://gitlab.com/api/v4`, and may be omitted only for github.com.
- `kind` is `github`, `gitlab` or `forgejo` and is always required. IDs must
  be unique; callers use them as `source` in requests.

Unknown fields are rejected. Startup fails if a token source is empty, but
tokens are reread for every request, so a rotated token file takes effect
without a restart.

## Run

```sh
openssl rand -hex 32 > service-token
chmod 600 service-token
/tmp/forge-repositories-service --config repositories.json --token-file service-token
```

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--grpc-listen` | `FORGE_REPOSITORIES_GRPC_LISTEN` | `127.0.0.1:9092` |
| `--http-listen` | `FORGE_REPOSITORIES_HTTP_LISTEN` | `127.0.0.1:8082` |
| `--config` | `FORGE_REPOSITORIES_CONFIG` | required |
| `--token-file` | `FORGE_REPOSITORIES_TOKEN_FILE` | none |
| `--token-env` | `FORGE_REPOSITORIES_TOKEN_ENV` | `FORGE_REPOSITORIES_TOKEN` when no source is set |
| `--request-timeout` | `FORGE_REPOSITORIES_REQUEST_TIMEOUT` | `30s` |

Flags override environment settings. The service token is the bearer token
callers must send; choose one file or environment source for it. It is reread
on each request, allowing rotation.

Listeners use plaintext on loopback by default. For remote use, put them
behind a TLS endpoint that preserves the Authorization header. The service
does not log requests, responses or upstream errors. REST responses use
`Cache-Control: no-store`, and bodies are limited to 1 MiB. SIGINT and SIGTERM
allow five seconds to drain requests.

### Docker

Build from the repository root, or pull `ghcr.io/r59q/forge-repositories-service`:

```sh
docker build -f forge-repositories/service/Dockerfile -t forge-repositories-service .
docker run -v "$PWD/repositories.json:/config/repositories.json:ro" \
  -p 127.0.0.1:8082:8080 -p 127.0.0.1:9092:9090 \
  -e FORGE_REPOSITORIES_TOKEN -e FORGE_PROXY_CLIENT_TOKEN forge-repositories-service
```

The image listens on all interfaces, reads `/config/repositories.json` and
runs as a non-root user. Pass tokens as environment variables or mounted
files; never bake them into the image.

## API

The [documented proto](proto/repositories.proto) is the source of truth.
Every RPC requires `Authorization: Bearer <service token>` (gRPC metadata for
gRPC). REST does not trust `Grpc-Metadata-*` headers. JSON uses lowerCamelCase
field names and RFC 3339 timestamps, and omits empty fields.

| RPC | REST endpoint |
| --- | --- |
| `ListSources` | `GET /v1/sources` |
| `ListRepositories` | `GET /v1/repositories?owner=&source=` |
| `GetRepository` | `GET /v1/repositories/{path}?source=` |
| `UpdateRepository` | `PATCH /v1/repositories/{path}` with `{"source","description","defaultBranch"}` |
| `ListBranches` | `GET /v1/branches/{path}?source=` |
| `ListTags` | `GET /v1/tags/{path}?source=` |

`{path}` is the plain repository path and may be nested, such as
`/v1/branches/group/sub/project`. Without `source`, the path must exist on
exactly one source.

```sh
api() { curl -fsS -H "Authorization: Bearer $(cat service-token)" "$@"; }
api 'localhost:8082/v1/repositories?owner=mrbean'
api localhost:8082/v1/branches/mrbean/tools/cli
api -X PATCH localhost:8082/v1/repositories/mrbean/teddy \
  -d '{"source":"<github-connection-id>","description":"A bear"}'
```

Errors carry only a status code, never input or forge responses:

| Cause | gRPC | HTTP |
| --- | --- | --- |
| Missing or wrong service token | `UNAUTHENTICATED` | 401 |
| Bad path, unknown source, empty update, forge rejected the update | `INVALID_ARGUMENT` | 400 |
| Path exists on several sources | `FAILED_PRECONDITION` | 400 |
| Repository not found | `NOT_FOUND` | 404 |
| Forge denied access | `PERMISSION_DENIED` | 403 |
| Forge rate limit | `RESOURCE_EXHAUSTED` | 429 |
| Forge or proxy rejected the source token, or failed | `UNAVAILABLE` | 503 |

## Regenerate

Install protoc 33.0 and these plugins on PATH:

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.10
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.27.3
```

Check out `googleapis/googleapis` at
`ff8fd20f3840386e12f4f69750eff6dc99f6e1b7` and set `GOOGLEAPIS_DIR` to its
root. From this directory, with protoc's bundled includes available:

```sh
protoc -I proto -I "$GOOGLEAPIS_DIR" \
  --go_out=repositoriespb --go_opt=paths=source_relative \
  --go-grpc_out=repositoriespb --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=repositoriespb --grpc-gateway_opt=paths=source_relative \
  proto/repositories.proto
go vet ./...
go test ./...
```

## License

[MIT](../../LICENSE)
