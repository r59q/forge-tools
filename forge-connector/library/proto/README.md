# Broker API

[`forge/connector/v1/broker.proto`](forge/connector/v1/broker.proto) defines
`forge.connector.v1.BrokerService`, the source of truth for gRPC and REST.
Every RPC documents its HTTP route and credential requirement. JSON uses
protobuf's lowerCamelCase field names and RFC 3339 timestamps. REST errors use gRPC status codes mapped to
HTTP (401 unauthenticated, 403 forbidden, 400 invalid input or failed
precondition, 404 missing record, 500 internal failure).

`VerifyClient` and `ListClientConnections` verify the token in
`Authorization: Bearer`, so a proxy forwards its caller's token. No token is accepted in the URL, query, or request body.
`CreateClient` and `IssueToken` explicitly return secrets to authorized callers;
all other responses contain metadata only. Never log secret-bearing messages.

## Regenerate

Generated files are committed in `../brokerpb/`. To regenerate, install
[buf](https://buf.build/docs/installation) 1.74.0 and Go, then run from
the library folder:

```sh
buf generate
go vet ./...
go test ./...
```

CI fails when the generated code is out of date.
