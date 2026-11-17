# forge-agents service

Serves the [forge-agents library](../library/) over gRPC, generated REST
routes and an NDJSON event stream, from
[one protobuf definition](proto/forge/agents/v1/agents.proto). Agents are
headless sessions of the Claude Code, Codex and Gemini CLIs. They work in
scratch directories, in existing directories below allowed roots, or in
new [forge-checkouts](../../forge-checkouts/service/) worktrees.

The service keeps agents and transcripts in SQLite, so both outlive a
restart. It never commits, pushes or opens pull requests, and it holds no
forge credentials; what happens to a worktree's changes is up to the caller.

## Build and test

Requires Go 1.27 or newer. From this directory:

```sh
go build -o /tmp/forge-agents-service ./cmd/forge-agents-service
go vet ./...
go test -race ./...
```

No test calls a model. Tests run fake client scripts that print hand-built
output, and a fake forge-checkouts.

The module uses relative replacements for `../library`,
`../../forge-checkouts/*`, `../../forge-connector/library` and
`../../forge-core/*`; keep that checkout layout. Generated Go files are
committed in `agentspb/`. After editing the proto, install
[buf](https://buf.build/docs/installation) 1.74.0 and run `buf generate`
here. CI fails when the generated code is out of date.

Build the image from the repository root:

```sh
docker build -f forge-agents/service/Dockerfile -t forge-agents-service .
```

The image is Node 24 on Alpine with Git, ripgrep and pinned versions of the
three CLIs. It runs as user 65532, the same user as the forge-checkouts
image, so agents can write in its worktrees. It keeps its data in the
`/agents-data` volume, its CLI configuration in `HOME=/home/agent`, and
listens on 9090 (gRPC) and 8080 (REST).

## Run

```sh
openssl rand -hex 32 > service-token
chmod 600 service-token
ANTHROPIC_API_KEY=... /tmp/forge-agents-service --token-file service-token --clients claude
```

| Flag                            | Environment variable                       | Default                                              |
| ------------------------------- | ------------------------------------------ | ---------------------------------------------------- |
| `--grpc-listen`                 | `FORGE_AGENTS_GRPC_LISTEN`                 | `127.0.0.1:9106`                                     |
| `--http-listen`                 | `FORGE_AGENTS_HTTP_LISTEN`                 | `127.0.0.1:8096`                                     |
| `--token-file`                  | `FORGE_AGENTS_TOKEN_FILE`                  | none                                                 |
| `--token-env`                   | `FORGE_AGENTS_TOKEN_ENV`                   | `FORGE_AGENTS_TOKEN` when no source is set           |
| `--request-timeout`             | `FORGE_AGENTS_REQUEST_TIMEOUT`             | `30s`                                                |
| `--data-dir`                    | `FORGE_AGENTS_DATA_DIR`                    | `./agents-data`                                      |
| `--clients`                     | `FORGE_AGENTS_CLIENTS`                     | `claude,codex,gemini`                                |
| `--claude-path`                 | `FORGE_AGENTS_CLAUDE_PATH`                 | `claude`, looked up in `PATH`                        |
| `--codex-path`                  | `FORGE_AGENTS_CODEX_PATH`                  | `codex`                                              |
| `--gemini-path`                 | `FORGE_AGENTS_GEMINI_PATH`                 | `gemini`                                             |
| `--path-roots`                  | `FORGE_AGENTS_PATH_ROOTS`                  | none: path workspaces are refused                    |
| `--max-agents`                  | `FORGE_AGENTS_MAX_AGENTS`                  | `100`                                                |
| `--max-running`                 | `FORGE_AGENTS_MAX_RUNNING`                 | `4`                                                  |
| `--max-events`                  | `FORGE_AGENTS_MAX_EVENTS`                  | `10000`                                              |
| `--turn-timeout`                | `FORGE_AGENTS_TURN_TIMEOUT`                | `1h`                                                 |
| `--stream-timeout`              | `FORGE_AGENTS_STREAM_TIMEOUT`              | `10m`                                                |
| `--tools-listen`                | `FORGE_AGENTS_TOOLS_LISTEN`                | `127.0.0.1:0`                                        |
| `--checkouts-addr`              | `FORGE_AGENTS_CHECKOUTS_ADDR`              | none: checkout workspaces are refused                |
| `--checkouts-insecure`          | `FORGE_AGENTS_CHECKOUTS_INSECURE`          | `false`: TLS                                         |
| `--checkouts-token-file`        | `FORGE_AGENTS_CHECKOUTS_TOKEN_FILE`        | none                                                 |
| `--checkouts-token-env`         | `FORGE_AGENTS_CHECKOUTS_TOKEN_ENV`         | `FORGE_AGENTS_CHECKOUTS_TOKEN` when no source is set |
| `--checkouts-data-dir`          | `FORGE_AGENTS_CHECKOUTS_DATA_DIR`          | required with `--checkouts-addr`                     |
| `--checkouts-reported-data-dir` | `FORGE_AGENTS_CHECKOUTS_REPORTED_DATA_DIR` | `--checkouts-data-dir`                               |

Every client in `--clients` must be found at startup. `--path-roots` is a
comma-separated list of absolute directories. `--request-timeout` bounds
every call but `StreamEvents`, which `--stream-timeout` bounds; a turn runs
on after the call that started it, until `--turn-timeout`. `--tools-listen`
is the loopback MCP server through which clients ask questions. The
connector flags that every service accepts are ignored.

The service token, and the forge-checkouts token when `--checkouts-addr` is
set, are read at startup, and the service does not start without them.

### Client credentials

Clients run with the service's environment, without variables whose names
start with `FORGE_` or that hold the service tokens. Give them credentials
the way each CLI reads them: `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` or
`GEMINI_API_KEY`, or a login stored under `HOME`. Values of variables whose
names hold `KEY`, `TOKEN`, `SECRET`, `PASSWORD`, `CREDENTIAL` or `AUTH` are
masked in transcripts, raw output and errors. Logs never hold them.

### Data directory

The data directory is created with mode 0700, and the service refuses one
the group or others can access. It holds `agents.db`, mode 0600, the
scratch workspaces in `scratch/`, and per-turn client configuration in
`run/`. When the service starts, turns a previous run left in progress end
with an `error` result and their agents return to `idle`.

### Checkouts

A checkout workspace is a forge-checkouts `worktree` checkout, created with
`CreateCheckout` and removed with `RemoveCheckout`; the forge-checkouts
token must allow both. The client runs in `<checkouts data dir>/copies/<id>`,
which must be writable here. Worktrees link to their repository by absolute
path, so mount the forge-checkouts data directory read-write at the path
forge-checkouts uses, as the [full-stack example](../../examples/fullstack/)
does at `/data`. When it must be mounted elsewhere, set
`--checkouts-reported-data-dir` to its `--data-dir` there; Git inside the
worktree then cannot find the repository, though the files are there.

## API

Every request requires `Authorization: Bearer <service token>` (gRPC
metadata for gRPC). REST bodies and query parameters use the proto's JSON
names.

| RPC             | REST endpoint                                      |
| --------------- | -------------------------------------------------- |
| `CreateAgent`   | `POST /v1/agents`                                  |
| `GetAgent`      | `GET /v1/agents/{id}`                              |
| `ListAgents`    | `GET /v1/agents?state=&parent=&repository.source=` |
| `SendMessage`   | `POST /v1/agents/{id}/messages`                    |
| `AnswerInput`   | `POST /v1/agents/{id}/inputs/{request_id}`         |
| `CancelTurn`    | `POST /v1/agents/{id}/cancel` with `{}`            |
| `StopAgent`     | `POST /v1/agents/{id}/stop` with `{}`              |
| `DeleteAgent`   | `DELETE /v1/agents/{id}?remove_checkout=false`     |
| `ListEvents`    | `GET /v1/agents/{id}/events?after=0&limit=100`     |
| `StreamEvents`  | `GET /v1/agents/{id}/stream?after=0`, NDJSON       |
| `GetTurnOutput` | `GET /v1/agents/{id}/turns/{turn}/output`          |

Create an agent in a new worktree and give it a task:

```sh
auth="Authorization: Bearer $(cat service-token)"
curl -s -H "$auth" localhost:8096/v1/agents -d '{"client":"claude",
  "workspace":{"kind":"checkout","repository":{"source":"github","path":"owner/repo"}},
  "policy":{"mode":"edit","ask":true}}'
curl -s -H "$auth" localhost:8096/v1/agents/$ID/messages -d '{"text":"Fix the failing test"}'
curl -sN -H "$auth" "localhost:8096/v1/agents/$ID/stream?after=0"
```

An empty `reference` is the remote HEAD. When the stream shows an
`input_request` and the agent is `waiting_input`, answer it:

```sh
curl -s -H "$auth" localhost:8096/v1/agents/$ID/inputs/$REQUEST -d '{"allow":true}'
```

The stream sends one `StreamEventsResponse` per line and ends normally
once the agent is stopped or failed and every event was sent, or at
`--stream-timeout`; resume with `after` set to the last `seq` seen. A cut
connection is an error. A `ListEvents` page holds at most 4 MiB, so it may
hold fewer than `limit` events; an empty page means the caller has caught
up. `GetTurnOutput` returns the raw output of one of the latest 10 turns,
base64 in JSON. gRPC clients must raise their receive limit to 5 MiB for
both.

Errors carry only a status code; why a turn failed is in the agent's
`error` and its transcript.

| Code                  | HTTP | When                                                                                              |
| --------------------- | ---- | ------------------------------------------------------------------------------------------------- |
| `INVALID_ARGUMENT`    | 400  | An invalid request, client, policy, workspace or limit                                            |
| `UNAUTHENTICATED`     | 401  | A missing or wrong service token                                                                  |
| `NOT_FOUND`           | 404  | An unknown agent, turn output, repository, reference or checkout                                  |
| `FAILED_PRECONDITION` | 400  | A call the agent state forbids, a disabled workspace kind, or a dirty checkout                    |
| `RESOURCE_EXHAUSTED`  | 429  | Too many agents or running turns, a full transcript, a request over 1 MiB, a repository too large |
| `UNAVAILABLE`         | 503  | forge-checkouts or storage failing                                                                |
| `CANCELLED`           | 499  | The caller cancelled                                                                              |
| `DEADLINE_EXCEEDED`   | 504  | The request timeout passed                                                                        |

SIGINT and SIGTERM end running turns with an `error` result before the
service exits. Listeners use plaintext; use TLS endpoints for remote
access.

## License

[MIT](../../LICENSE)
