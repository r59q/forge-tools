# forge-symbols service

Serves the [forge-symbols library](../library/) over gRPC and generated REST
routes: find the definitions and references in source files, and navigate
from a name or position to what it calls, what calls it, and where it is
defined. It reads only request content, keeps nothing between requests and
holds no forge credentials, so it needs no forge-connector.

## Build and test

Requires Go 1.27 or newer. From this directory:

```sh
go build -o /tmp/forge-symbols-service ./cmd/forge-symbols-service
go vet ./...
go test -race ./...
```

The module uses relative replacements for `../library` and
`../../forge-core/*`; keep that checkout layout. Generated Go files are
committed in `symbolspb/`. After editing the proto, run `buf generate` here.

Build the image from the repository root:

```sh
docker build -f forge-symbols/service/Dockerfile -t forge-symbols-service .
```

The image listens on 9090 (gRPC) and 8080 (REST).

## Run

```sh
openssl rand -hex 32 > service-token
chmod 600 service-token
/tmp/forge-symbols-service --token-file service-token
```

| Flag                | Environment variable            | Default                                     |
| ------------------- | ------------------------------- | ------------------------------------------- |
| `--grpc-listen`     | `FORGE_SYMBOLS_GRPC_LISTEN`     | `127.0.0.1:9104`                            |
| `--http-listen`     | `FORGE_SYMBOLS_HTTP_LISTEN`     | `127.0.0.1:8094`                            |
| `--token-file`      | `FORGE_SYMBOLS_TOKEN_FILE`      | none                                        |
| `--token-env`       | `FORGE_SYMBOLS_TOKEN_ENV`       | `FORGE_SYMBOLS_TOKEN` when no source is set |
| `--request-timeout` | `FORGE_SYMBOLS_REQUEST_TIMEOUT` | `30s`                                       |

The connector flags that every service accepts are ignored.

## Limits

The library's defaults apply to each request: at most 1000 files, 1 MiB per
file, 16 MiB in total, 5 s of parsing per file and 256 MiB of parser memory.
Navigation returns at most 1000 definitions and references. gRPC messages
and REST bodies may be just large enough for 16 MiB of content (REST bodies
carry it as base64, about 22 MiB). Larger ones are rejected before they
are parsed: as `RESOURCE_EXHAUSTED`, or as `INVALID_ARGUMENT` for a chunked
REST body without a length.

Each request is analyzed from scratch. Parsing many large files can take
several seconds, so keep requests to the files the question needs.

## API

Every call needs `Authorization: Bearer <token>`, and is a `POST` with a JSON
body. The protobuf definition in
[proto/](proto/forge/symbols/v1/symbols.proto) is the source of truth and
documents every field.

| Method and path                | Body                            | Returns                            |
| ------------------------------ | ------------------------------- | ---------------------------------- |
| `POST /v1/symbols/analyze`     | `files`                         | `analyses`                         |
| `POST /v1/symbols/outgoing`    | `files`, `target`, `maxResults` | `name`, `groups`, `truncated`      |
| `POST /v1/symbols/incoming`    | `files`, `target`, `maxResults` | `name`, `groups`, `truncated`      |
| `POST /v1/symbols/definitions` | `files`, `target`, `maxResults` | `name`, `definitions`, `truncated` |

A file is `{path, content, language}`. `content` is base64, as for every
protobuf `bytes` field in JSON. The path's extension selects the language
unless `language` is one of `go`, `java`, `javascript`, `typescript`, `tsx`
or `python`.

Ranges have one-based lines and UTF-16 columns, with an exclusive end
column. Indexes point into a file's `definitions` or `references` as
returned by `analyze`; `parent`, `enclosing` and a definition result's
`index` are absent at the file level. Results match by name only, so each
has `precision` `"heuristic"`, and are ranked by `tier`: `TIER_FILE` for
the target's file, `TIER_CONTAINER` for the target's container elsewhere,
then `TIER_OTHER`.

A target is a `name`, a `path` with a `line` and `column`, or both. A
position selects the definition whose name it is on, else the reference it
is on, else the innermost definition around it. `kind` narrows the
definitions, such as to `method`. `maxResults` defaults to and is at most
1000.

The examples below send the Go files in the current directory:

```sh
files() {
  for f in *.go; do
    jq -n --arg p "$f" --rawfile c "$f" '{path: $p, content: ($c | @base64)}'
  done | jq -s .
}
symbols() {
  curl -fsS -H "Authorization: Bearer $(cat service-token)" \
    "http://127.0.0.1:8094/v1/symbols/$1" -d @-
}
```

### Analyze

Lists the definitions and references of each file, in request order.

```sh
jq -n --argjson f "$(files)" '{files: $f}' | symbols analyze |
  jq -r '.analyses[] | .path as $p | .definitions[] |
    "\($p):\(.nameRange.startLine) \(.kind) \(.name)"'
```

### Outgoing

Lists the references inside the target definitions, such as the calls a
function makes, with one group per definition. A position on a definition
targets only that definition; a name targets every definition of it.

```sh
jq -n --argjson f "$(files)" '{files: $f, target: {path: "main.go", line: 12, column: 6}}' |
  symbols outgoing | jq -r '.groups[].references[].reference.name'
```

### Incoming

Lists the references named like the target, grouped by the definition
around them, such as the callers of a function. A reference outside any
definition is grouped under a `caller` with only `path`, `tier` and
`precision`.

```sh
jq -n --argjson f "$(files)" '{files: $f, target: {name: "Greet", path: "greet.go"}}' |
  symbols incoming | jq -r '.groups[] | "\(.caller.path) \(.caller.definition.name // "(file)")"'
```

### Definitions

Lists the definitions named like the target, such as where the call under
a cursor is defined.

```sh
jq -n --argjson f "$(files)" '{files: $f, target: {path: "main.go", line: 14, column: 9}}' |
  symbols definitions |
  jq -r '.definitions[] | "\(.path):\(.definition.nameRange.startLine) \(.tier)"'
```

## Errors

Errors carry only a status code; they never echo request content.

| Code                 | HTTP | When                                                                              |
| -------------------- | ---- | --------------------------------------------------------------------------------- |
| `UNAUTHENTICATED`    | 401  | The token is missing or wrong. Checked before the body is read.                   |
| `INVALID_ARGUMENT`   | 400  | The target is incomplete, `maxResults` is out of range, or the body is malformed. |
| `NOT_FOUND`          | 404  | The target position is on no definition or reference.                             |
| `RESOURCE_EXHAUSTED` | 429  | A file, the request or parser memory is over its limit.                           |
| `UNIMPLEMENTED`      | 501  | A file's language is not supported. The whole request fails.                      |
| `DEADLINE_EXCEEDED`  | 504  | A parse or the request took too long.                                             |

## License

[MIT](../../LICENSE)
