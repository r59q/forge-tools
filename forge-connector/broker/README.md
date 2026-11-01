# forge-connector broker

Standalone, single-owner service for forge connections and scoped clients.
It exposes the [broker library](../library/) through gRPC and generated REST
routes. GitHub, GitLab and Forgejo providers are registered at startup.
No proxy or code generation tools are needed to run it.

## Build and test

Requires Go 1.27 or newer. From this directory:

```sh
go build -o /tmp/forge-connector-broker ./cmd/forge-connector-broker
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
CGO_ENABLED=0 go build ./...
```

The module uses relative replacements for `../library` and
`../../forge-core/forgeapi`; keep that checkout layout. Tests use temporary
SQLite files, local forge servers and synthetic credentials. They cover the
shared store contract, both transports, authentication, scope, rotation,
restart persistence, encrypted credentials, startup and shutdown.

Generated Go files are committed in `../library/brokerpb`. See
[API generation](../library/proto/README.md) when editing the protobuf source.
Run `go vet ./...` and `go test ./...` from `../library` as well.

## Run

Create a private directory, a persistent encryption key and an admin token:

```sh
mkdir -m 700 broker-data
openssl rand -base64 32 > broker-data/key
openssl rand -hex 32 > broker-data/admin
chmod 600 broker-data/key broker-data/admin
/tmp/forge-connector-broker \
  --db broker-data/broker.db \
  --key-file broker-data/key \
  --admin-token-file broker-data/admin
```

Preserve the key across restarts and back it up separately from the database.
The service refuses to start without a valid key and admin token. It never
creates a fallback key. Losing the key loses access to stored forge credentials.
Changing the key does not re-encrypt existing data; key migration is not provided.

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--grpc-listen` | `FORGE_BROKER_GRPC_LISTEN` | `127.0.0.1:9090` |
| `--http-listen` | `FORGE_BROKER_HTTP_LISTEN` | `127.0.0.1:8080` |
| `--db` | `FORGE_BROKER_DB` | `broker.db` |
| `--key-file` | `FORGE_BROKER_KEY_FILE` | none |
| `--key-env` | `FORGE_BROKER_KEY_ENV` | `FORGE_BROKER_KEY` when no source is set |
| `--admin-token-file` | `FORGE_BROKER_ADMIN_TOKEN_FILE` | none |
| `--admin-token-env` | `FORGE_BROKER_ADMIN_TOKEN_ENV` | `FORGE_BROKER_ADMIN_TOKEN` when no source is set |
| `--request-timeout` | `FORGE_BROKER_REQUEST_TIMEOUT` | `30s` |

Flags override environment settings. Choose one file or environment source for
each secret; setting both is an error. `--key-env` and `--admin-token-env` name
variables, rather than containing secrets. The key source must contain standard
base64 encoding of exactly 32 bytes. The admin source must contain a nonempty
bearer token. File sources trim surrounding whitespace. The admin source uses
`FileToken` or `EnvToken` and is reread on each admin request, allowing rotation.
It is never stored in SQLite. The sealer key is loaded only at startup.
Connections are created through the API and never loaded from configuration.

Listeners use plaintext on loopback by default. For remote use, place them
behind a trusted TLS endpoint that supports gRPC and REST, preserves the
Authorization header and restricts access to the backend listeners. Do not
log Authorization headers, request bodies or the two token-bearing responses.
REST responses use `Cache-Control: no-store`. The service does not log requests,
responses or upstream errors. SIGINT and SIGTERM allow five seconds to drain
requests before forcing shutdown. Requests have a configurable operation
deadline and a 1 MiB message/body limit.

### Docker

Build from the repository root, or pull `ghcr.io/r59q/forge-connector-broker`:

```sh
docker build -f forge-connector/broker/Dockerfile -t forge-connector-broker .
docker run -v broker-data:/data -p 127.0.0.1:8080:8080 -p 127.0.0.1:9090:9090 \
  -e FORGE_BROKER_KEY -e FORGE_BROKER_ADMIN_TOKEN forge-connector-broker
```

The image listens on all interfaces, stores the database in the `/data`
volume and runs as a non-root user. Mount secrets as files or pass them as
environment variables; never bake them into the image.

## API

The [documented proto](../library/proto/broker.proto) is the source of truth.
Both transports accept only `Authorization: Bearer <token>` (gRPC metadata for
gRPC). Client tokens cannot call admin methods, and the admin credential does
not substitute for a client token. Missing, wrong, malformed or repeated
credentials fail authentication. REST does not trust `Grpc-Metadata-*` headers.

| Admin RPC | REST endpoint |
| --- | --- |
| `CreateForge` / `ListForges` | `POST` / `GET /v1/forges` |
| `DeleteForge` | `DELETE /v1/forges/{id}` |
| `CreateConnection` / `ListConnections` | `POST` / `GET /v1/connections` |
| `GetConnection` / `DeleteConnection` | `GET` / `DELETE /v1/connections/{id}` |
| `TestConnection` | `POST /v1/connections/{id}/test` |
| `RotateCredential` | `POST /v1/connections/{id}/credential` |
| `CreateClient` / `ListClients` | `POST` / `GET /v1/clients` |
| `DeleteClient` | `DELETE /v1/clients/{id}` |

| Client RPC | REST endpoint |
| --- | --- |
| `IssueToken` | `POST /v1/connections/{connection_id}/token` |
| `VerifyClient` | `POST /v1/connections/{connection_id}/verify` |
| `ListClientConnections` | `GET /v1/client/connections` |

Start by creating a forge with `kind`, `baseUrl` (including its API path) and
optional `name`. Create a connection with `forgeId`, `credential` and optional
`name`; the broker checks Whoami before saving it. Create a client with
`connectionIds`, `canIssueTokens` and optional `name`, and save its returned
token securely. Set `allConnections` instead of `connectionIds` to grant every
connection, including ones created later, such as for a proxy. To use a token
from your own configuration, set `token`; it needs at least 32 printable
characters, without spaces, and must be unique. Use that token as the bearer credential for client operations.
`CreateClient` returns its token once; `IssueToken` deliberately returns the
forge credential to an authorized client. All other results are metadata only.

`ListClientConnections` returns the ID, name, forge kind and status of each
connection in the bearer token's scope, never credentials or URLs. It does not
require `canIssueTokens`.

`VerifyClient` verifies the bearer token itself; a proxy forwards its caller's
token and supplies the connection ID. No token is accepted in its body or query.
It requires scope and a healthy connection, but does not require
`canIssueTokens`. `IssueToken` also requires that grant. Empty scopes grant
nothing. Deleting a client immediately revokes it. Deleting a connection removes
all its client grants atomically. A referenced forge cannot be deleted.

`TestConnection` refreshes health. Forge rejection marks the connection revoked;
other forge errors mark it invalid. Either denies verification and issuance.
The RPC returns a sanitized error on failure; `GetConnection` reads the updated
status. A successful test or rotation restores access. Issuance uses the last
known status and does not contact the forge. Failed rotations preserve the old
credential. REST uses protobuf JSON, rejects unknown fields, and returns safe
gRPC error codes mapped to HTTP statuses; upstream bodies and database errors
are never returned.

## Persistence

SQLite uses the pure-Go `modernc.org/sqlite` driver with no cgo. Schema version
1 is applied transactionally on first open; newer unknown versions are rejected.
Use one broker process per database. A single database connection serializes
transactions. Connection metadata and AES-256-GCM ciphertext are written
atomically; client tokens are stored only as SHA-256 hashes. Metadata and hashes
are not encrypted. The database file is restricted to mode 0600. Keep its parent
directory private and use a SQLite-consistent backup method.

One Manager serializes broker operations, including Whoami calls. This is suited
to a single owner with a modest number of connections and clients. Client lookup
scans stored hashes, matching the library's constant-time comparison behavior.
