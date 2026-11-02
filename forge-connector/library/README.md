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
  `RegisterForge` also takes an explicit `GitBaseURL`; otherwise it is
  derived with `forgeauth.DefaultGitURL`, or left empty when it cannot be.
  Metadata reads use `ListConnections`, `GetConnection` and `ListClients`.
- `CreateConnection` calls `Whoami` before storing a sealed forge token and
  identity. `TestConnection` refreshes identity, status and check time.
- `RotateCredential` validates the replacement before writing it. Failure
  preserves the old credential and metadata. `DeleteConnection` removes both
  metadata and ciphertext and removes the connection from client scopes.
- `CreateClient` returns a random 256-bit client token once and stores only its
  SHA-256 hash. `DeleteClient` invalidates that token. An empty scope grants
  no access. `CreateClientForAll` grants every connection, including later
  ones, for services such as a proxy. `RegisterClient` creates a client with
  a token you choose, such as one from a deployment's configuration. Client hashes are compared in constant time, scanning all clients.
- `VerifyClient` checks the client token, connection scope and last known
  connection status without decrypting credentials or contacting the forge.
- `ClientConnections` lists the ID, name, forge kind and status of each
  connection in a client token's scope, without credentials or endpoints.
- `IssueToken` also requires `CanIssueTokens`, then returns the forge kind,
  API base URL, Git base URL and decrypted token. It uses the last check's status; call
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
kind, API endpoint and Git endpoint cannot change while connections reference it.

`AESGCMSealer` uses AES-256-GCM with a fresh random nonce. The caller supplies
and retains a 32-byte key separately from stored data; losing it loses access
to the credentials. Use one Manager per Store and route mutations through it
while it is active. Manager operations are serialized, including forge calls.
Set context deadlines to bound those calls; client lookup scans all clients.

## connector

Tools use `connector.New(connector.Config{URL, Credentials})` with the proxy
URL and their broker client token. `Connections` returns each usable
connection (status `ok`, supported kind) with its ID, name, kind and an open
`forgeauth` session for `<proxy>/c/{id}`. Make forge API calls with the
session; the proxy swaps in the forge credential. `Connection.Git` is a
`forgeauth.GitRemote` for `<proxy>/g/{id}`; its client sends the client token
as a Bearer header, only to the proxy's origin. `Connections` asks the proxy
each time, so new, deleted and revoked connections apply at once.

```go
c, err := connector.New(connector.Config{URL: proxyURL, Credentials: forgeauth.EnvToken("FORGE_TOKEN")})
conns, err := c.Connections(ctx)
var me struct{ Login string }
err = conns[0].Session.GetJSON(ctx, "/user", &me)
```

## proxy

`proxy.New(proxy.Config{...})` builds an `http.Handler` for
`/c/{connectionID}/<forge API path>`. Point a normal `forgeauth` session at
`https://proxy.example/c/{connectionID}`, using its client token. The handler
uses that forge's provider to read the caller header and set the real token.
It removes incoming Authorization, PRIVATE-TOKEN, cookies and proxy credentials.

Supply small, trusted `Verifier` and `TokenIssuer` adapters. `Verifier.Connection`
returns the connection's forge kind and API base URL from broker metadata;
`VerifyClient` checks the caller token and scope. `TokenIssuer` uses the proxy's
own broker client with `CanIssueTokens`, only after verification. Callers do not
need that permission. Adapters map broker RPC errors to `broker` error classes.
They are service wiring, not provider plugins. Register forgeauth providers
explicitly. Connection transport settings are ignored; `Config.Transport` is
the handler's trusted transport.

Set `Config.Connections` to serve `GET /connections`, which lists the caller's
connections as JSON. The `ConnectionLister` adapter must call the broker with
the caller's token. The `connector` package is the matching Go client.

Mount the handler without a path-cleaning router. It rejects absolute request
targets, dot segments, backslashes and malformed or repeatedly encoded traversal.
Encoded slashes, such as GitLab project names, are preserved. Requests only go
to the connection's scheme, host and base path, without following redirects.
`Link` and `Location` URLs within that endpoint become absolute proxy URLs;
external, out-of-base and unsafe URLs are dropped. Bodies stream unchanged,
including forge error bodies. Request trailers are not forwarded.

Set `Config.PublicURL` to the public origin behind a TLS-terminating reverse
proxy. Otherwise rewritten URLs use the request's Host and TLS state; forwarded
headers are ignored. Use service context deadlines to bound broker and forge
calls. Proxy errors start with `proxy:`: 400 invalid request, 401 missing or
invalid credentials, 403 denied scope, 404 unknown connection, 503 broker or
connection unavailable, 504 broker deadline, and 502 invalid endpoint/token or
forge transport failure. Dependency error messages are never logged or echoed.

### Git route

The handler also serves Git smart HTTP at
`/g/{connectionID}/{repoPath}.git/{endpoint}`. It forwards to the broker's
Git base URL for the connection and sets the provider's Git credential, so the
provider must implement `forgeauth.GitAuthenticator`; otherwise it answers 502.
Callers send the client token as `Authorization: Bearer`, or as the password
of a Basic header, as Git clients do. [forge-code](../../forge-code/) pushes
its commits and ref updates through it.

- Only `GET info/refs?service=git-upload-pack` and `POST git-upload-pack` are
  allowed, plus the `git-receive-pack` pair when `Config.AllowGitPush` is set.
  The query must match exactly. Everything else, such as dumb HTTP, archives
  and LFS, gets 404.
- Repository paths use `[A-Za-z0-9._-]` segments, never all dots. Escapes,
  encoded slashes and traversal get 400.
- POST bodies need the matching `application/x-git-*-request` type (415
  otherwise). They stream, capped at 16 MiB for fetches and
  `Config.MaxGitPushBytes` (default 2 GiB) for pushes (413).
- Only `Accept`, `Accept-Encoding`, `Content-Encoding`, `Git-Protocol` and
  `User-Agent` reach the forge. Responses keep only content and caching
  headers. `Set-Cookie` and trailers are dropped, and `WWW-Authenticate` is
  replaced with the proxy's realm.
- Redirects are never followed. A `Location` is kept only when it points to an
  allowed route on the same Git origin, rewritten to the proxy.
- The issued kind and API base URL must match the verified connection.

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
external forge access are needed. When `git` is on PATH, a proxy test pushes
and fetches through the handler to `git http-backend`; otherwise it is skipped.
