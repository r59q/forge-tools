# forge-actions service

REST and gRPC for [pipeline browsing](../library/), using forge-connector
sessions. Connections are refreshed on every request. All RPCs require a
service bearer token; no forge credentials are accepted here.

## Build, test and run

From this directory, with Go 1.27:

```sh
go build ./cmd/forge-actions-service
go test ./...
go vet ./...
go run ./cmd/forge-actions-service \
  --connector-url http://127.0.0.1:8081 \
  --connector-token-file /run/secrets/actions-connector-token \
  --token-file /run/secrets/actions-service-token
```

Register the connector client token in the broker first, or let management
register it. The [full-stack example](../../examples/fullstack/) configures
both services and exposes the pipeline browser in management.

| Flag                     | Environment                          | Default                                  |
| ------------------------ | ------------------------------------ | ---------------------------------------- |
| `--http-listen`          | `FORGE_ACTIONS_HTTP_LISTEN`          | `127.0.0.1:8089`                         |
| `--grpc-listen`          | `FORGE_ACTIONS_GRPC_LISTEN`          | `127.0.0.1:9099`                         |
| `--connector-url`        | `FORGE_ACTIONS_CONNECTOR_URL`        | Required                                 |
| `--token-file`           | `FORGE_ACTIONS_TOKEN_FILE`           | None                                     |
| `--token-env`            | `FORGE_ACTIONS_TOKEN_ENV`            | Variable `FORGE_ACTIONS_TOKEN`           |
| `--connector-token-file` | `FORGE_ACTIONS_CONNECTOR_TOKEN_FILE` | None                                     |
| `--connector-token-env`  | `FORGE_ACTIONS_CONNECTOR_TOKEN_ENV`  | Variable `FORGE_ACTIONS_CONNECTOR_TOKEN` |
| `--request-timeout`      | `FORGE_ACTIONS_REQUEST_TIMEOUT`      | `30s`                                    |

Choose one source for each credential. Tokens are reread for rotation.
Use TLS termination when serving across a network boundary.

## API

The [protobuf definition](proto/forge/actions/v1/actions.proto) generates
both APIs, including the `forge.actions.v1.ActionsService` gRPC service.
Send `Authorization: Bearer <service token>` as an HTTP header or gRPC
metadata.
All resource routes require `source` and `repository` query parameters.

| RPC                | REST GET route                 |
| ------------------ | ------------------------------ |
| `ListSources`      | `/v1/sources`                  |
| `ListPipelines`    | `/v1/pipelines`                |
| `GetPipeline`      | `/v1/pipelines/{id}`           |
| `ListJobs`         | `/v1/pipelines/{id}/jobs`      |
| `GetJob`           | `/v1/jobs/{id}`                |
| `ReadJobLog`       | `/v1/jobs/{id}/log`            |
| `ListArtifacts`    | `/v1/pipelines/{id}/artifacts` |
| `DownloadArtifact` | `/v1/artifacts/{id}/download`  |
| `ListDefinitions`  | `/v1/definitions`              |

Pipeline query fields: `page`, `commit`, `branch`, `definition_id`, `phase`,
`result`. Page defaults to 1; follow `nextPage` until zero. The library's
limits and capability differences apply unchanged.

Logs are UTF-8 text in JSON with a truncation flag. Archive downloads are
protobuf bytes (base64 in REST JSON), capped at 16 MiB. gRPC clients downloading
archives should set `grpc.MaxCallRecvMsgSize(24 << 20)`.

Unsupported features map to `UNIMPLEMENTED` / HTTP 501, size limits to
`RESOURCE_EXHAUSTED` / 429, missing objects to `NOT_FOUND` / 404, invalid
arguments to `INVALID_ARGUMENT` / 400 and forbidden reads to
`PERMISSION_DENIED` / 403. Backend credential failures are `UNAVAILABLE` /
503. Responses never echo upstream error bodies and are marked `no-store`.

GitHub log/artifact downloads require the connector's trusted HTTPS download
hosts. The full-stack example enables `*.blob.core.windows.net` and
`*.actions.githubusercontent.com`. Configure your own storage hosts for
self-hosted forges. The connector fetches these URLs without credentials.

## Container

Build from the repository root:

```sh
docker build -f forge-actions/service/Dockerfile -t forge-actions-service .
```

The container listens on HTTP 8080 and gRPC 9090 and needs no persistent storage.

## Regenerate protobuf

Generated files are committed in `actionspb/`. To regenerate, install
[buf](https://buf.build/docs/installation) 1.74.0 and Go, then run from
this folder:

```sh
buf generate
go vet ./...
go test ./...
```

CI fails when the generated code is out of date.
