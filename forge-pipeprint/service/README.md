# forge-pipeprint service

Serves the [forge-pipeprint library](../library/) over gRPC and generated REST
routes: convert pipeline files between formats, check them, and return job
graphs or text drawings. It reads only request content and holds no forge
credentials, so it needs no forge-connector.

## Build and test

Requires Go 1.27 or newer. From this directory:

```sh
go build -o /tmp/forge-pipeprint-service ./cmd/forge-pipeprint-service
go vet ./...
go test -race ./...
```

The module uses relative replacements for `../library` and
`../../forge-core/*`; keep that checkout layout. Generated Go files are
committed in `pipeprintpb/`. After editing the proto, run `buf generate` here.

Build the image from the repository root:

```sh
docker build -f forge-pipeprint/service/Dockerfile -t forge-pipeprint-service .
```

## Run

```sh
openssl rand -hex 32 > service-token
chmod 600 service-token
/tmp/forge-pipeprint-service --token-file service-token
```

| Flag                | Environment variable         | Default                                   |
| ------------------- | ---------------------------- | ----------------------------------------- |
| `--grpc-listen`     | `FORGE_PIPEPRINT_GRPC_LISTEN`    | `127.0.0.1:9102`                          |
| `--http-listen`     | `FORGE_PIPEPRINT_HTTP_LISTEN`    | `127.0.0.1:8092`                          |
| `--token-file`      | `FORGE_PIPEPRINT_TOKEN_FILE`     | none                                      |
| `--token-env`       | `FORGE_PIPEPRINT_TOKEN_ENV`      | `FORGE_PIPEPRINT_TOKEN` when no source is set |
| `--request-timeout` | `FORGE_PIPEPRINT_REQUEST_TIMEOUT` | `10s`                                    |

The connector flags that every service accepts are ignored.

## API

Every call needs `Authorization: Bearer <token>`. Bodies are at most 1 MiB.
Formats are `pipeprint`, `pipeprint-json`, `github`, `forgejo` and
`gitlab`; an empty `from` detects the format.

| Method and path              | Body                                         | Returns                                |
| ---------------------------- | -------------------------------------------- | -------------------------------------- |
| `GET /v1/pipeprint/formats`  |                                              | `formats`                              |
| `POST /v1/pipeprint/convert` | `content`, `from`, `to`                      | `content`, `warnings`                  |
| `POST /v1/pipeprint/check`   | `content`, `from`                            | `valid`, `format`, `problems`          |
| `POST /v1/pipeprint/graph`   | `content`, `from`                            | `nodes`, `edges`, `levels`, `triggers` |
| `POST /v1/pipeprint/render`  | `content`, `from`, `steps`, `ascii`, `width` | `text`                                 |

Errors carry only a status code. Invalid content is `INVALID_ARGUMENT`
(HTTP 400); call `check` to see why. The protobuf definition in
[proto/](proto/forge/pipeprint/v1/pipeprint.proto) is the source of truth.

```sh
jq -n --rawfile c .github/workflows/ci.yml '{content: $c, to: "gitlab"}' |
  curl -fsS -H "Authorization: Bearer $(cat service-token)" \
    http://127.0.0.1:8092/v1/pipeprint/convert -d @- | jq -r .content
```

## License

[MIT](../../LICENSE)
