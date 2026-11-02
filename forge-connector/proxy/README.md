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

Requests go to `/c/{connectionID}/<forge API path>`. Send the client token as
`Authorization: Bearer <token>` for any forge. Query strings, methods,
bodies and responses pass through unchanged, and pagination links are rewritten
to point back at the proxy.

Git smart HTTP goes to `/g/{connectionID}/{repoPath}.git`. The proxy sends it
to the connection's Git base URL from the broker, with the forge's Git
credential. Fetches always work; pushes only with `--allow-git-push`, which
[forge-code](../../forge-code/) needs for its writes. Send the client token as
a Bearer header or as the Basic password:

```sh
git -c "http.extraHeader=Authorization: Bearer $CLIENT_TOKEN" \
  clone http://localhost:8081/g/my-connection/owner/repo.git
```

The route allows only the smart-HTTP upload-pack and receive-pack endpoints,
streams capped bodies, never follows redirects and drops cookies and
credentials. See the [library](../library/README.md#git-route) for the rules.

`GET /connections` lists the connections the client token may use, so callers
need no connection IDs in their own configuration:

```sh
curl -H "Authorization: Bearer $CLIENT_TOKEN" localhost:8081/connections
# {"connections":[{"id":"…","name":"me","kind":"github","status":"ok"}]}
```

Only connections with status `ok` can be used. Forge credentials and URLs are
never listed. The proxy's own client must also cover a connection to serve it.
Go tools use the [connector package](../library/README.md#connector), which
turns this list into ready sessions.

The proxy has no gRPC or protobuf forwarding API. The forge API surface is
plain HTTP by design, so the proxy forwards plain HTTP.

## Build and test

Requires Go 1.27 or newer. From this directory:

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
connection listing, the Git route, token and verification caches, token drop on a forge 401, broker outages, and a
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
| `--allow-git-push` | `FORGE_PROXY_ALLOW_GIT_PUSH` | `false` |
| `--max-git-push-bytes` | `FORGE_PROXY_MAX_GIT_PUSH_BYTES` | `2147483648` (2 GiB) |

Flags override environment settings. Choose one token source; `--token-env`
names a variable rather than containing the token. The token is read through
`forgeauth` credentials and the proxy refuses to start without one. Connection
settings are never configured here: they live in the broker.

Set `--public-url` when a TLS-terminating proxy sits in front, so rewritten
pagination links use the public origin.

### Docker

Build from the repository root, or pull `ghcr.io/r59q/forge-connector-proxy`:

```sh
docker build -f forge-connector/proxy/Dockerfile -t forge-connector-proxy .
docker run -p 127.0.0.1:8081:8081 -e FORGE_PROXY_TOKEN \
  -e FORGE_PROXY_BROKER_ADDR=broker:9090 forge-connector-proxy
```

The image listens on all interfaces and runs as a non-root user.

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

Each request, including Git requests, logs the connection ID, method, path, status and duration. Query
strings, tokens and headers are never logged.

## Error mapping

| Status | Meaning |
| --- | --- |
| 401 | Missing or invalid client token |
| 405 | `/connections` or a Git endpoint with the wrong method |
| 413 | Git request body over its cap |
| 415 | Git POST without the matching content type |
| 403 | Client may not use the connection |
| 404 | Unknown connection, or a Git route outside the allow-list |
| 502 | Invalid endpoint or token, no Git base URL, or forge transport failure |
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
