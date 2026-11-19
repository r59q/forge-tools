# forge-chat service

Serves the standalone app, forwards agent operations to forge-agents, and
persists UI metadata in one directory per deployment. Requires Go 1.27.

## Build and run

From this directory, build the app and the service:

```sh
(cd ../frontend && npm ci && npm run build)
go build -o /tmp/forge-chat-service ./cmd/forge-chat-service
go test -race ./...
```

Start forge-agents, set `FORGE_CHAT_TOKEN` to a separate browser-facing
token and set `FORGE_CHAT_AGENTS_TOKEN` to the token accepted by forge-agents.
Run `/tmp/forge-chat-service` and use the browser-facing token at
http://127.0.0.1:8097/. Both tokens support file sources and are read on use.

| Flag | Environment | Default |
| --- | --- | --- |
| `--http-listen` | `FORGE_CHAT_HTTP_LISTEN` | `127.0.0.1:8097` |
| `--grpc-listen` | `FORGE_CHAT_GRPC_LISTEN` | `127.0.0.1:9107` |
| `--assets` | `FORGE_CHAT_ASSETS` | `../frontend/dist/web` |
| `--data-dir` | `FORGE_CHAT_DATA_DIR` | `./chat-data` |
| `--agents-url` | `FORGE_CHAT_AGENTS_URL` | `http://127.0.0.1:8096` |
| `--token-file` | `FORGE_CHAT_TOKEN_FILE` | none |
| `--token-env` | `FORGE_CHAT_TOKEN_ENV` | `FORGE_CHAT_TOKEN` |
| `--agents-token-file` | `FORGE_CHAT_AGENTS_TOKEN_FILE` | none |
| `--agents-token-env` | `FORGE_CHAT_AGENTS_TOKEN_ENV` | `FORGE_CHAT_AGENTS_TOKEN` |
| `--request-timeout` | `FORGE_CHAT_REQUEST_TIMEOUT` | `30s` |
| `--stream-timeout` | `FORGE_CHAT_STREAM_TIMEOUT` | `11m` |

The token grants access to all chats in this deployment. Run one service
instance per metadata directory. Use HTTPS when serving beyond loopback.

Build the container from the repository root with
`docker build -f forge-chat/service/Dockerfile -t forge-chat-service .`.
It serves HTTP on 8080, gRPC on 9090, and stores metadata in `/chat-data`.

## API

All API routes require the chat token. Go and REST metadata clients use
[the same protobuf definition](proto/forge/chat/v1/chat.proto).

| Route | Purpose |
| --- | --- |
| `GET /` | Open the public app shell. |
| `GET /app/{file}`, `GET /src/{file}` | Load public app assets. |
| `GET /v1/chat-metadata` | List stored display preferences. |
| `PATCH /v1/chat-metadata/{id}` | Set `archived`, `links.ids`, or `islandPosition` for a chat. |

Links are outgoing chat IDs. Send `{"links":{"ids":[]}}` to clear them.
Omitted fields keep their current values. Archiving affects only the chat list.

The app also uses these
[forge-agents REST routes](../../forge-agents/service/README.md#api) through
the service. Requests use the paths, payloads and responses from forge-agents.

- `GET` and `POST /v1/agents` list and create agents.
- `GET /v1/agents/{id}` reads agent state.
- `GET /v1/agents/{id}/events` pages historical transcript events.
- `GET /v1/agents/{id}/stream?after=N` streams NDJSON after sequence `N`.
- `POST /v1/agents/{id}/messages`, `POST /v1/agents/{id}/inputs/{request}`
  and `POST /v1/agents/{id}/stop` send a turn, answer input and stop an agent.

Delete agents through the forge-agents API.
See [buf.gen.yaml](buf.gen.yaml) for the pinned generators used by `buf generate`.
