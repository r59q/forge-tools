# forge-repositories service

Serves the [forge-repositories library](../library/) over gRPC and generated
REST routes. It reads every forge connected through
[forge-connector](../../forge-connector/) at once, so listing owner `mrbean`
returns their repositories from GitHub, GitLab and Forgejo together. It holds
no forge credentials; the connector handles authentication.

## Build and test

Requires Go 1.27 or newer. From this directory:

```sh
go build -o /tmp/forge-repositories-service ./cmd/forge-repositories-service
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
```

The module uses relative replacements for `../library`,
`../../forge-connector/library` and `../../forge-core/forgeapi`; keep that
checkout layout. Tests use local fake forges, the real proxy handler and
synthetic tokens. They cover both transports, authentication, connection
discovery, nested GitLab paths, updates, error mapping and shutdown.

Generated Go files are committed in `repositoriespb/`; see
[Regenerate](#regenerate) when editing the proto.

## Configure

Give the service a forge-connector proxy URL and a broker client token. Create
the client in the broker with the connections to read; it does not need
`canIssueTokens`. The proxy's own client must also cover those connections.

On every request the service asks the
[connector](../../forge-connector/library/README.md#connector) for its usable
connections and reads each as a source named by its connection ID. The
connector supplies authenticated sessions, so new, deleted and revoked
connections apply without a restart and this service never sees forge
credentials. If the connector cannot list connections, requests fail with
`UNAVAILABLE`.

## Run

```sh
openssl rand -hex 32 > service-token
chmod 600 service-token
/tmp/forge-repositories-service --token-file service-token \
  --connector-url http://127.0.0.1:8081 --connector-token-file connector-token
```

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--grpc-listen` | `FORGE_REPOSITORIES_GRPC_LISTEN` | `127.0.0.1:9092` |
| `--http-listen` | `FORGE_REPOSITORIES_HTTP_LISTEN` | `127.0.0.1:8082` |
| `--token-file` | `FORGE_REPOSITORIES_TOKEN_FILE` | none |
| `--token-env` | `FORGE_REPOSITORIES_TOKEN_ENV` | `FORGE_REPOSITORIES_TOKEN` when no source is set |
| `--connector-url` | `FORGE_REPOSITORIES_CONNECTOR_URL` | required |
| `--connector-token-file` | `FORGE_REPOSITORIES_CONNECTOR_TOKEN_FILE` | none |
| `--connector-token-env` | `FORGE_REPOSITORIES_CONNECTOR_TOKEN_ENV` | `FORGE_REPOSITORIES_CONNECTOR_TOKEN` when no source is set |
| `--request-timeout` | `FORGE_REPOSITORIES_REQUEST_TIMEOUT` | `30s` |

Flags override environment settings. The service token is the bearer token
callers must send. The connector token is the broker client token. Choose one
file or environment source for each; `--*-token-env` names a variable rather
than containing the token. Both are reread on each request, allowing rotation.
Startup fails if either is empty.

Listeners use plaintext on loopback by default. For remote use, put them
behind a TLS endpoint that preserves the Authorization header. The service
does not log requests, responses or upstream errors. REST responses use
`Cache-Control: no-store`, and bodies are limited to 1 MiB. SIGINT and SIGTERM
allow five seconds to drain requests.

### Docker

Build from the repository root, or pull `ghcr.io/r59q/forge-repositories-service`:

```sh
docker build -f forge-repositories/service/Dockerfile -t forge-repositories-service .
docker run -p 127.0.0.1:8082:8080 -p 127.0.0.1:9092:9090 \
  -e FORGE_REPOSITORIES_CONNECTOR_URL=http://proxy:8081 \
  -e FORGE_REPOSITORIES_TOKEN -e FORGE_REPOSITORIES_CONNECTOR_TOKEN \
  forge-repositories-service
```

The image listens on all interfaces and runs as a non-root user. Pass tokens
as environment variables or mounted files; never bake them into the image.
The [management example](../../examples/management/) runs it with the
connector in Docker Compose.

## API

The [documented proto](proto/repositories.proto) is the source of truth.
Every RPC requires `Authorization: Bearer <service token>` (gRPC metadata for
gRPC). REST does not trust `Grpc-Metadata-*` headers. JSON uses lowerCamelCase
field names and RFC 3339 timestamps, and omits empty fields.

| RPC | REST endpoint |
| --- | --- |
| `ListSources` | `GET /v1/sources` (usable connections) |
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
  -d '{"source":"<connection-id>","description":"A bear"}'
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
| Connector or forge rejected the connector token, or failed | `UNAVAILABLE` | 503 |

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
