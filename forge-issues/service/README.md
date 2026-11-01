# forge-issues service

Serves the [forge-issues library](../library/) over gRPC and generated REST
routes. It reads every forge connected through
[forge-connector](../../forge-connector/) at once, so listing the issues
assigned to you returns them from GitHub, GitLab and Forgejo together. It
holds no forge credentials; the connector handles authentication.

## Build and test

Requires Go 1.27 or newer. From this directory:

```sh
go build -o /tmp/forge-issues-service ./cmd/forge-issues-service
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
```

The module uses relative replacements for `../library`,
`../../forge-connector/library` and `../../forge-core/forgeapi`; keep that
checkout layout. Tests use local fake forges, the real proxy handler and
synthetic tokens. They cover both transports, authentication, connection
discovery, nested GitLab paths, writes, error mapping and shutdown.

Generated Go files are committed in `issuespb/`; see
[Regenerate](#regenerate) when editing the proto.

## Configure

Give the service a forge-connector proxy URL and a broker client token. Create
the client in the broker with the connections to use; it does not need
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
/tmp/forge-issues-service --token-file service-token \
  --connector-url http://127.0.0.1:8081 --connector-token-file connector-token
```

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--grpc-listen` | `FORGE_ISSUES_GRPC_LISTEN` | `127.0.0.1:9094` |
| `--http-listen` | `FORGE_ISSUES_HTTP_LISTEN` | `127.0.0.1:8084` |
| `--token-file` | `FORGE_ISSUES_TOKEN_FILE` | none |
| `--token-env` | `FORGE_ISSUES_TOKEN_ENV` | `FORGE_ISSUES_TOKEN` when no source is set |
| `--connector-url` | `FORGE_ISSUES_CONNECTOR_URL` | required |
| `--connector-token-file` | `FORGE_ISSUES_CONNECTOR_TOKEN_FILE` | none |
| `--connector-token-env` | `FORGE_ISSUES_CONNECTOR_TOKEN_ENV` | `FORGE_ISSUES_CONNECTOR_TOKEN` when no source is set |
| `--request-timeout` | `FORGE_ISSUES_REQUEST_TIMEOUT` | `30s` |

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

Build from the repository root:

```sh
docker build -f forge-issues/service/Dockerfile -t forge-issues-service .
docker run -p 127.0.0.1:8084:8080 -p 127.0.0.1:9094:9090 \
  -e FORGE_ISSUES_CONNECTOR_URL=http://proxy:8081 \
  -e FORGE_ISSUES_TOKEN -e FORGE_ISSUES_CONNECTOR_TOKEN \
  forge-issues-service
```

The image listens on all interfaces and runs as a non-root user. Pass tokens
as environment variables or mounted files; never bake them into the image.

## API

The [documented proto](proto/issues.proto) is the source of truth. Every RPC
requires `Authorization: Bearer <service token>` (gRPC metadata for gRPC).
REST does not trust `Grpc-Metadata-*` headers. JSON uses lowerCamelCase field
names and RFC 3339 timestamps, and omits empty fields. Issue numbers are
int64, so JSON responses carry them as strings, such as `"number":"7"`.

| RPC | REST endpoint |
| --- | --- |
| `ListSources` | `GET /v1/sources` (usable connections) |
| `ListIssues` | `GET /v1/issues?source=&repository=&state=&labels=&scope=` |
| `GetIssue` | `GET /v1/issues/{number}?repository=&source=` |
| `CreateIssue` | `POST /v1/issues` with `{"source","repository","title","body","labels","assignees"}` |
| `UpdateIssue` | `PATCH /v1/issues/{number}` with `{"source","repository","title","body","state","labels","assignees"}` |
| `ListComments` | `GET /v1/issues/{number}/comments?repository=&source=` |
| `AddComment` | `POST /v1/issues/{number}/comments` with `{"source","repository","body"}` |

`repository` is the plain path, such as `owner/name` or `group/sub/project`;
it goes in the query or body because it contains slashes. Reads without
`source` search every source, and `GetIssue` and `ListComments` need the issue
on exactly one. Writes need `source`.

`ListIssues` with a repository lists it on every source that has it. Without
one, it lists each source's own issues: `scope` is `assigned` (default) or
`created`. `state` is `open` (default), `closed` or `all`. Repeat `labels`
to require several: `labels=bug&labels=ui`.

In `UpdateIssue`, `labels` and `assignees` are `{"values":[...]}` objects.
Leave one out to keep it; send `{}` to clear it. `state` is `open` or
`closed`. Assignees are logins. Pull and merge requests are never returned
or changed.

```sh
api() { curl -fsS -H "Authorization: Bearer $(cat service-token)" "$@"; }
api localhost:8084/v1/issues
api 'localhost:8084/v1/issues?repository=mrbean/tools/cli&state=all'
api -X POST localhost:8084/v1/issues \
  -d '{"source":"<connection-id>","repository":"mrbean/teddy","title":"Bear is lost"}'
api -X PATCH localhost:8084/v1/issues/1 \
  -d '{"source":"<connection-id>","repository":"mrbean/teddy","state":"closed","labels":{}}'
api -X POST localhost:8084/v1/issues/1/comments \
  -d '{"source":"<connection-id>","repository":"mrbean/teddy","body":"Found him"}'
```

Errors carry only a status code, never input or forge responses:

| Cause | gRPC | HTTP |
| --- | --- | --- |
| Missing or wrong service token | `UNAUTHENTICATED` | 401 |
| Bad argument, unknown source or login, empty update, forge rejected the write | `INVALID_ARGUMENT` | 400 |
| Issue exists on several sources, or forge conflict | `FAILED_PRECONDITION` | 400 |
| Repository or issue not found, or the number is a pull request | `NOT_FOUND` | 404 |
| Forge denied access | `PERMISSION_DENIED` | 403 |
| Forge rate limit | `RESOURCE_EXHAUSTED` | 429 |
| Connector or forge rejected the connector token, or failed | `UNAVAILABLE` | 503 |

A GitHub repository with issues turned off answers 410, which is
`UNAVAILABLE`.

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
  --go_out=issuespb --go_opt=paths=source_relative \
  --go-grpc_out=issuespb --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=issuespb --grpc-gateway_opt=paths=source_relative \
  proto/issues.proto
go vet ./...
go test ./...
```

## License

[MIT](../../LICENSE)
