# Broker API

`broker.proto` is the source of truth for gRPC and REST. Every RPC documents
its HTTP route and credential requirement. JSON uses protobuf's lowerCamelCase
field names and RFC 3339 timestamps. REST errors use gRPC status codes mapped to
HTTP (401 unauthenticated, 403 forbidden, 400 invalid input or failed
precondition, 404 missing record, 500 internal failure).

`VerifyClient` verifies the token in `Authorization: Bearer`, so a proxy forwards
its caller's token. No token is accepted in the URL, query, or request body.
`CreateClient` and `IssueToken` explicitly return secrets to authorized callers;
all other responses contain metadata only. Never log secret-bearing messages.

## Regenerate

Builds use the committed files in `../brokerpb`; no generators are needed.
To regenerate, install protoc 33.0 and these Go plugins on PATH:

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.10
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.27.3
```

Check out `googleapis/googleapis` at
`ff8fd20f3840386e12f4f69750eff6dc99f6e1b7` and set `GOOGLEAPIS_DIR` to its root.
From the library directory, with protoc's bundled standard includes available:

```sh
protoc -I proto -I "$GOOGLEAPIS_DIR" \
  --go_out=brokerpb --go_opt=paths=source_relative \
  --go-grpc_out=brokerpb --go-grpc_opt=paths=source_relative \
  --grpc-gateway_out=brokerpb --grpc-gateway_opt=paths=source_relative \
  proto/broker.proto
go vet ./...
go test ./...
```
