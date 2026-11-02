# forge-changes service

REST and gRPC for the [changes library](../library/). Each request discovers
usable connections through forge-connector and uses its authenticated
sessions. Forge tokens and connection management stay in the connector.

## Build, test and run

From this directory, with Go 1.27:

```sh
go build ./cmd/forge-changes-service
go vet ./...
go test -race ./...
./forge-changes-service --connector-url http://127.0.0.1:8081 \
  --token-file service-token --connector-token-file connector-token
```

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--grpc-listen` | `FORGE_CHANGES_GRPC_LISTEN` | `127.0.0.1:9096` |
| `--http-listen` | `FORGE_CHANGES_HTTP_LISTEN` | `127.0.0.1:8086` |
| `--token-file` | `FORGE_CHANGES_TOKEN_FILE` | none |
| `--token-env` | `FORGE_CHANGES_TOKEN_ENV` | `FORGE_CHANGES_TOKEN` |
| `--connector-url` | `FORGE_CHANGES_CONNECTOR_URL` | required |
| `--connector-token-file` | `FORGE_CHANGES_CONNECTOR_TOKEN_FILE` | none |
| `--connector-token-env` | `FORGE_CHANGES_CONNECTOR_TOKEN_ENV` | `FORGE_CHANGES_CONNECTOR_TOKEN` |
| `--request-timeout` | `FORGE_CHANGES_REQUEST_TIMEOUT` | `30s` |

Flags override environment values. Choose one file or environment source for
each token. `--token-env` names a variable, not a token. Defaults apply only
when neither source is set. Tokens are reread on each request for rotation.
Listeners use plaintext loopback; remote deployments need a TLS endpoint that
preserves Authorization. REST bodies are limited to 1 MiB. Responses use
`Cache-Control: no-store`; errors never echo forge data. Shutdown drains for
up to five seconds. Large diff clients should increase gRPC's receive limit
above its default 4 MiB (raw diffs can reach 16 MiB).

Build Docker from the repository root:

```sh
docker build -f forge-changes/service/Dockerfile -t forge-changes-service .
docker run -p 127.0.0.1:8086:8080 -p 127.0.0.1:9096:9090 \
  -e FORGE_CHANGES_CONNECTOR_URL=http://proxy:8081 \
  -e FORGE_CHANGES_TOKEN -e FORGE_CHANGES_CONNECTOR_TOKEN forge-changes-service
```

The image runs as non-root. Supply tokens through environment variables or
mounted files, never the image. The service workflow builds pull requests
without registry access and deploys on pushes to master.

## API

[changes.proto](proto/changes.proto) is the source for both transports. All
calls require `Authorization: Bearer <service token>` (metadata for gRPC).
REST ignores `Grpc-Metadata-*` headers. JSON uses lowerCamelCase, RFC 3339
timestamps and strings for int64 numbers. Empty default fields are omitted.

| RPC | REST route |
| --- | --- |
| `ListSources` | `GET /v1/sources` |
| `ListChanges` | `GET /v1/changes?repository=&source=&author=&state=&draft=` |
| `GetChange` | `GET /v1/changes/{number}?source=&repository=` |
| `CreateChange` | `POST /v1/changes` |
| `UpdateChange` | `PATCH /v1/changes/{number}` |
| `SetDraft` | `PUT /v1/changes/{number}/draft` |
| `ListCommits` | `GET /v1/changes/{number}/commits?source=&repository=` |
| `ListFiles` | `GET /v1/changes/{number}/files?source=&repository=` |
| `GetDiff` | `GET /v1/changes/{number}/diff?source=&repository=` |
| `ListComments` | `GET /v1/changes/{number}/comments?source=&repository=` |
| `AddComment` | `POST /v1/changes/{number}/comments` |
| `GetReadiness` | `GET /v1/changes/{number}/readiness?source=&repository=` |
| `Merge` | `POST /v1/changes/{number}/merge` |

`repository` is a required plain path, such as `owner/project` or GitLab
`group/sub/project`. List accepts an optional source; other operations require
it. `state` is open (default), closed, merged or all. Omitting `draft` lists
both draft and ready; `draft=false` selects ready proposals.

Writes put source and repository in the JSON body. Create also requires
`title`, `sourceBranch` and `targetBranch`; `description` and `draft` are
optional. Update accepts optional `title`, `description`, `targetBranch`
and `state` (open or closed). Omit a field to preserve it; use an empty
description to clear it. `sourceBranch` updates fail as unsupported.
SetDraft uses `draft: true` or `false`. AddComment requires nonempty `body`.

Merge accepts `method` and optional `expectedHead`. For example:

```json
{
  "source": "connection-id",
  "repository": "owner/project",
  "method": "squash",
  "expectedHead": "0123456789abcdef0123456789abcdef01234567"
}
```

`ListSources` includes adapter capabilities. [Forge mappings](../README.md)
document methods, GitHub GraphQL routing, draft title prefixes and diff limits.
Readiness is advisory and includes explicit unknown conditions. Merging
requires no forge-review service; the forge enforces its policies.

| Cause | gRPC | HTTP |
| --- | --- | --- |
| Missing/wrong service token | UNAUTHENTICATED | 401 |
| Invalid argument, unknown source, forge validation | INVALID_ARGUMENT | 400 |
| Unsupported operation | UNIMPLEMENTED | 501 |
| Head changed, merge rejected, conflict | FAILED_PRECONDITION | 400 |
| Missing proposal/repository | NOT_FOUND | 404 |
| Forge denied access | PERMISSION_DENIED | 403 |
| Rate limit or oversized result | RESOURCE_EXHAUSTED | 429 |
| Deadline | DEADLINE_EXCEEDED | 504 |
| Other connector/forge failure | UNAVAILABLE | 503 |

## Regenerate

Use protoc 33.0 with its bundled includes and these pinned plugins:

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.10
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.27.3
```

Set `GOOGLEAPIS_DIR` to a googleapis/googleapis checkout at
`ff8fd20f3840386e12f4f69750eff6dc99f6e1b7`. From this directory:

```sh
protoc -I proto -I "$GOOGLEAPIS_DIR" \
  --go_out=changespb --go_opt=paths=source_relative \
  --go-grpc_out=changespb --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=changespb --grpc-gateway_opt=paths=source_relative \
  proto/changes.proto
go vet ./...
go test ./...
```

## License

[MIT](../../LICENSE)
