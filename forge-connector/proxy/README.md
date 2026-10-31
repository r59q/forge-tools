# forge-connector proxy

Standalone HTTP service that lets a client call a forge API without holding
the forge credential. It serves the [library proxy handler](../library/proxy/)
and reaches the [broker](../broker/) over gRPC. Callers present a broker client
token; the proxy checks it, fetches the connection's forge token and forwards
the request.

```text
caller --Bearer client token--> proxy --gRPC--> broker
                                  |
                                  +--Bearer forge token--> forge
```

Requests go to `/c/{connectionID}/<forge API path>`. Query strings, methods,
bodies and responses pass through unchanged, and pagination links are rewritten
to point back at the proxy.

The proxy has no gRPC or protobuf forwarding API. The forge API surface is
plain HTTP by design, so the proxy forwards plain HTTP.

## Build and test

Requires Go 1.24 or newer. From this directory:

```sh
go build -o /tmp/forge-connector-proxy ./cmd/forge-connector-proxy
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
```

The module uses relative replacements for `../library` and
`../../forge-core/forgeapi`; keep that checkout layout. It does not import the
broker service. Tests run a fake gRPC broker built from the generated
`brokerpb` package against a local forge server. They cover the full request,
token and verification caches, token drop on a forge 401, broker outages, and a
`forgeauth` session paging through results via the proxy.

## Run

The proxy needs its own broker client token. Create a client in the broker
with `CanIssueTokens` and access to the connections the proxy should serve,
then store the token in a private file:

```sh
install -m 600 /dev/null proxy-token
printf '%s\n' "$PROXY_CLIENT_TOKEN" > proxy-token
/tmp/forge-connector-proxy \
  --broker-addr 127.0.0.1:9090 \
  --token-file proxy-token
```

| Flag | Environment variable | Default |
| --- | --- | --- |
| `--http-listen` | `FORGE_PROXY_HTTP_LISTEN` | `127.0.0.1:8081` |
| `--grpc-listen` | `FORGE_PROXY_GRPC_LISTEN` | `127.0.0.1:9091` |
| `--broker-addr` | `FORGE_PROXY_BROKER_ADDR` | `127.0.0.1:9090` |
| `--token-file` | `FORGE_PROXY_TOKEN_FILE` | none |
| `--token-env` | `FORGE_PROXY_TOKEN_ENV` | `FORGE_PROXY_TOKEN` when no source is set |
| `--token-ttl` | `FORGE_PROXY_TOKEN_TTL` | `60s` |
| `--verify-ttl` | `FORGE_PROXY_VERIFY_TTL` | `30s` |
| `--broker-timeout` | `FORGE_PROXY_BROKER_TIMEOUT` | `10s` |
| `--broker-tls` | `FORGE_PROXY_BROKER_TLS` | `false` |
| `--broker-ca-file` | `FORGE_PROXY_BROKER_CA_FILE` | system roots; implies TLS |
| `--public-url` | `FORGE_PROXY_PUBLIC_URL` | derived from the request |

Flags override environment settings. Choose one token source; `--token-env`
names a variable rather than containing the token. The token is read through
`forgeauth` credentials and the proxy refuses to start without one. Connection
settings are never configured here: they live in the broker.

Set `--public-url` when a TLS-terminating proxy sits in front, so rewritten
pagination links use the public origin.

## Caching

- Forge tokens are cached per connection for `--token-ttl`. A forge 401
  drops the cached token and the request is not retried; the next request asks
  the broker for a fresh token. Use `0` to disable.
- Client approvals are cached for `--verify-ttl`, keyed by a hash of the caller
  token and the connection, so raw tokens are never stored. Denials are cached
  no longer than approvals, and for at most five seconds. Transient broker
  errors are never cached. Use `0` to disable.

Revoking a client therefore takes effect within `--verify-ttl`.

## Health and logs

`GET /healthz` returns `200 ok`, and `503` once shutdown starts. The standard
gRPC health service is served on `--grpc-listen`. SIGINT and SIGTERM allow five
seconds to drain requests.

Each request logs the connection ID, method, path, status and duration. Query
strings, tokens and headers are never logged.

## Error mapping

| Status | Meaning |
| --- | --- |
| 401 | Missing or invalid client token |
| 403 | Client may not use the connection |
| 404 | Unknown connection |
| 503 | Broker unavailable or rejected the proxy's own token |
| 504 | Broker call timed out |

Forge responses, including forge errors, pass through unchanged.

## Use with forgeauth

Point a session at the proxy by using the connection's path as its base URL and
the client token as its credential:

```go
reg := forgeauth.Registry{}
_ = reg.Register(github.Provider{})

session, err := reg.Open(ctx, forgeauth.Connection{
	Kind:    forgeauth.GitHub, // the kind of the connection's forge
	BaseURL: "http://127.0.0.1:8081/c/my-connection",
}, forgeauth.EnvToken("FORGE_CLIENT_TOKEN"))
if err != nil {
	return err
}
var me struct{ Login string }
err = session.GetJSON(ctx, "/user", &me)
```

Pagination helpers such as `forgehttp.NextPage` follow links that already point
back at the proxy.
