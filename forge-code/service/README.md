# forge-code service

Serves the [forge-code library](../library/) over gRPC, generated REST routes
and a raw download route. It reads and writes the Git content of every forge
connected through [forge-connector](../../forge-connector/): GitHub, GitLab
and Forgejo through one API. It holds no forge credentials; the connector
handles authentication.

## Build and test

Requires Go 1.27 or newer. From this directory:

```sh
go build -o /tmp/forge-code-service ./cmd/forge-code-service
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
```

The module uses relative replacements for `../library`,
`../../forge-connector/library` and `../../forge-core/forgeapi`; keep that
checkout layout. Tests use local fake forges, the real proxy handler and
synthetic tokens. They cover both transports, streams, raw downloads,
authentication, error mapping, body caps, the in-flight limit and shutdown.
`cmd/forge-code-service/write_e2e_test.go` pushes through the real proxy to
`git http-backend`; it skips when `git` is not on `PATH`.

Generated Go files are committed in `codepb/`; see
[Regenerate](#regenerate) when editing the proto.

## Configure

Give the service a forge-connector proxy URL and a broker client token. Create
the client in the broker with the connections to use; it does not need
`canIssueTokens`. The proxy's own client must also cover those connections.
Writes push through the proxy's Git route, so the proxy must run with
`--allow-git-push`. Without it writes fail with `NOT_FOUND`.

On every request the service asks the
[connector](../../forge-connector/library/README.md#connector) for its usable
connections and uses each as a source named by its connection ID. New,
deleted and revoked connections apply without a restart. If the connector
cannot list connections, requests fail with `UNAVAILABLE`.

## Run

```sh
openssl rand -hex 32 > service-token
chmod 600 service-token
/tmp/forge-code-service --token-file service-token \
  --connector-url http://127.0.0.1:8081 --connector-token-file connector-token
```

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--grpc-listen` | `FORGE_CODE_GRPC_LISTEN` | `127.0.0.1:9095` |
| `--http-listen` | `FORGE_CODE_HTTP_LISTEN` | `127.0.0.1:8085` |
| `--token-file` | `FORGE_CODE_TOKEN_FILE` | none |
| `--token-env` | `FORGE_CODE_TOKEN_ENV` | `FORGE_CODE_TOKEN` when no source is set |
| `--connector-url` | `FORGE_CODE_CONNECTOR_URL` | required |
| `--connector-token-file` | `FORGE_CODE_CONNECTOR_TOKEN_FILE` | none |
| `--connector-token-env` | `FORGE_CODE_CONNECTOR_TOKEN_ENV` | `FORGE_CODE_CONNECTOR_TOKEN` when no source is set |
| `--request-timeout` | `FORGE_CODE_REQUEST_TIMEOUT` | `30s` |
| `--stream-timeout` | `FORGE_CODE_STREAM_TIMEOUT` | `10m` |
| `--max-inflight-per-source` | `FORGE_CODE_MAX_INFLIGHT_PER_SOURCE` | `8` |

Flags override environment settings. The service token is the bearer token
callers must send. The connector token is the broker client token. Choose one
file or environment source for each; `--*-token-env` names a variable rather
than containing the token. Both are reread on each request, allowing rotation.
Startup fails if either is empty.

The request timeout limits unary calls. The stream timeout limits
`ReadFile`, `ReadBlob` and `GET /v1/raw`. Requests over the in-flight limit
of one source fail at once with `RESOURCE_EXHAUSTED`.

Listeners use plaintext on loopback by default. For remote use, put them
behind a TLS endpoint that preserves the Authorization header. The service
does not log requests, responses or upstream errors. REST responses use
`Cache-Control: no-store`. REST bodies are limited to 1 MiB, or 96 MiB for
`POST /v1/commits`; gRPC messages to 72 MiB. The token is checked before a
commit body or gRPC message is read. A commit upload and its push each get
the request timeout. gRPC connections allow 100 concurrent streams. SIGINT
and SIGTERM allow five seconds to drain requests.

### Docker

Build from the repository root:

```sh
docker build -f forge-code/service/Dockerfile -t forge-code-service .
docker run -p 127.0.0.1:8085:8080 -p 127.0.0.1:9095:9090 \
  -e FORGE_CODE_CONNECTOR_URL=http://proxy:8081 \
  -e FORGE_CODE_TOKEN -e FORGE_CODE_CONNECTOR_TOKEN \
  forge-code-service
```

The image listens on all interfaces and runs as a non-root user. Pass tokens
as environment variables or mounted files; never bake them into the image.

## API

The [documented proto](proto/code.proto) is the source of truth. Every RPC
and `GET /v1/raw` requires `Authorization: Bearer <service token>` (gRPC
metadata for gRPC). REST does not trust `Grpc-Metadata-*` headers. JSON uses
lowerCamelCase field names and RFC 3339 timestamps, and omits empty fields.
Sizes are int64, so JSON carries them as strings; `bytes` fields are base64.

| RPC | REST endpoint |
| --- | --- |
| `ListSources` | `GET /v1/sources` |
| `GetCapabilities` | `GET /v1/capabilities?source=` |
| `Resolve` | `GET /v1/resolve?source=&repository=&revision=` |
| `ListRefs` | `GET /v1/refs?source=&repository=&prefix=&limit=` |
| `GetCommit` | `GET /v1/commit?source=&repository=&revision=` |
| `ListHistory` | `GET /v1/history?source=&repository=&revision=&path=&page_size=&cursor=` |
| `GetTree` | `GET /v1/tree?source=&repository=&revision=&path=&recursive=&max_entries=` |
| `Stat` | `GET /v1/stat?source=&repository=&revision=&path=` |
| `GetFile` | `GET /v1/file?source=&repository=&revision=&path=` (at most 4 MiB) |
| `GetBlob` | `GET /v1/blob?source=&repository=&id=` (at most 4 MiB) |
| `ReadFile`, `ReadBlob` | gRPC streams of 64 KiB chunks; REST uses `GET /v1/raw` |
| `GetMergeBase` | `GET /v1/merge-base?source=&repository=&a=&b=` |
| `Compare` | `GET /v1/compare?source=&repository=&base=&head=&mode=&paths=&patches=&context=&max_changes=&max_patch_files=&max_patch_bytes=` |
| `CreateCommit` | `POST /v1/commits` with `{"source","repository","parents","changes","author","committer","message","ref","old"}` |
| `UpdateRefs` | `POST /v1/ref-updates` with `{"source","repository","updates":[{"name","old","new"}]}` |

`repository`, file paths and ref names contain slashes, so they go in the
query or body. Every call names one `source`. A revision is a full object
ID, `HEAD`, `refs/heads/X`, `refs/tags/X` or a bare name, with optional `~N`
and `^N`; empty means `HEAD`. Each read resolves it once, and results carry
the commit they read. Short object IDs are rejected.

`GetFile` and `GetBlob` suit small files. gRPC clients reading files near
4 MiB must raise their receive limit above the 4 MiB default, or use the
streams. The first `Chunk` of a stream carries the `Blob`.

`GET /v1/raw?source=&repository=&path=&revision=` or
`GET /v1/raw?source=&repository=&blob=` returns the content as
`application/octet-stream` with `Content-Disposition: attachment`, and
`Content-Length` when the size is known. `X-Forge-Code-Blob`,
`X-Forge-Code-Commit` (files only) and `X-Forge-Code-LFS-Pointer` describe
it. If reading fails after the first byte, the connection is aborted, so a
truncated download shows as an error.

Writes push with Git through the connector: `CreateCommit` makes one commit
and moves `ref` from `old` to it; `UpdateRefs` applies all updates or none.
An `old` of 40 zeros means the ref must not exist; a `new` of 40 zeros
deletes it. Commits are unsigned.

```sh
api() { curl -fsS -H "Authorization: Bearer $(cat service-token)" "$@"; }
api 'localhost:8085/v1/tree?source=<id>&repository=mrbean/teddy&revision=main'
api -o README.md 'localhost:8085/v1/raw?source=<id>&repository=mrbean/teddy&path=README.md'
api 'localhost:8085/v1/compare?source=<id>&repository=mrbean/teddy&base=main&head=fix&patches=true'
api -X POST localhost:8085/v1/commits -d '{"source":"<id>","repository":"mrbean/teddy",
  "parents":["<sha>"],"ref":"refs/heads/main","old":"<sha>","message":"Update",
  "author":{"name":"Mr Bean","email":"bean@example.com"},
  "changes":[{"path":"README.md","content":"aGVsbG8K"}]}'
```

Errors carry only a status code, never input or forge responses. Causes are
checked in this order:

| Cause | gRPC | HTTP |
| --- | --- | --- |
| Missing or wrong service token | `UNAUTHENTICATED` | 401 |
| Canceled or timed out | `CANCELLED` / `DEADLINE_EXCEEDED` | 499 / 504 |
| Bad argument or unknown source | `INVALID_ARGUMENT` | 400 |
| Ref not at `old` | `ABORTED` | 409 |
| Ambiguous name, path not a file, or forge refused the push | `FAILED_PRECONDITION` | 400 |
| Size or call limit exceeded | `OUT_OF_RANGE` | 400 |
| Not supported by the forge or source | `UNIMPLEMENTED` | 501 |
| Repository, revision or path not found, or proxy pushes off | `NOT_FOUND` | 404 |
| Forge denied access | `PERMISSION_DENIED` | 403 |
| Forge rate limit, or source in-flight limit | `RESOURCE_EXHAUSTED` | 429 |
| Forge conflict | `FAILED_PRECONDITION` | 400 |
| Forge rejected a write's arguments | `INVALID_ARGUMENT` | 400 |
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
  --go_out=codepb --go_opt=paths=source_relative \
  --go-grpc_out=codepb --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=codepb --grpc-gateway_opt=paths=source_relative \
  proto/code.proto
go vet ./...
go test ./...
```

## License

[MIT](../../LICENSE)
