# forge-connector library

Go module: `github.com/r59q/forge-tools/forge-connector/library`.
The broker core uses the local `forge-core/forgeapi` module through a relative
`replace`. The generated `brokerpb` package adds protobuf, gRPC and grpc-gateway
runtimes. The [API definition](proto/README.md) documents regeneration.
There is no standalone executable.

## broker

Register the `forgeauth` providers you need, add forge API endpoints to a
`Store`, and construct a `Manager` with that store, a `Sealer` and the registry.
See the [setup example](broker/example_test.go).

- `CreateForge`, `ListForges` and `DeleteForge` manage registered endpoints.
  Metadata reads use `ListConnections`, `GetConnection` and `ListClients`.
- `CreateConnection` calls `Whoami` before storing a sealed forge token and
  identity. `TestConnection` refreshes identity, status and check time.
- `RotateCredential` validates the replacement before writing it. Failure
  preserves the old credential and metadata. `DeleteConnection` removes both
  metadata and ciphertext and removes the connection from client scopes.
- `CreateClient` returns a random 256-bit client token once and stores only its
  SHA-256 hash. `DeleteClient` invalidates that token. An empty scope grants
  no access. Client hashes are compared in constant time, scanning all clients.
- `VerifyClient` checks the client token, connection scope and last known
  connection status without decrypting credentials or contacting the forge.
- `IssueToken` also requires `CanIssueTokens`, then returns the forge kind,
  API base URL and decrypted token. It uses the last check's status; call
  `TestConnection` to refresh it.

A successful check sets `ok`. HTTP 401/403 sets `revoked`; other forge failures
set `invalid`. Both states deny verification and issuance until a successful
test or rotation. Failed checks return updated metadata and a sanitized error;
`errors.Is` preserves recognized error classes without forge response messages.

Forge secrets are write-only through Manager except for `IssueToken`. The new
client token returned once by `CreateClient` is the bootstrap exception.
Tokens use `forgeauth.StaticToken`, which redacts `fmt`, `slog` and JSON output.
Authorized consumers must explicitly convert the token to a string to use it.
Manager results contain metadata only, with client hashes omitted. Responses
that echo a submitted forge token are redacted from identity fields and errors.

`Store` and `Sealer` are trusted primitives, not public credential-reading APIs.
`Store.GetCredential` returns ciphertext; `Sealer.Open` decrypts it internally.
`MemoryStore` is concurrency-safe and ephemeral. Persistent stores must preserve
the interface's atomic writes, reference checks and defensive copies. A forge's
kind and endpoint cannot change while connections reference it.

`AESGCMSealer` uses AES-256-GCM with a fresh random nonce. The caller supplies
and retains a 32-byte key separately from stored data; losing it loses access
to the credentials. Use one Manager per Store and route mutations through it
while it is active. Manager operations are serialized, including forge calls.
Set context deadlines to bound those calls; client lookup scans all clients.

## Develop

From this directory:

```sh
gofmt -l .
go build ./...
go vet ./...
go test ./...
go test -race ./...
```

Tests use `MemoryStore` and local `httptest` forges; no real credentials or
external forge access are needed.
