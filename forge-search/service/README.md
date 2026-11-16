# forge-search service

Serves the [forge-search library](../library/) over gRPC and generated REST
routes: index the code of [forge-checkouts](../../forge-checkouts/service/)
checkouts into named, durable indexes and search them, or search a checkout
without an index. Both return the same matches in the same order.

The service resolves checkout IDs through forge-checkouts, with its own
token, and reads the checkout directories from the forge-checkouts data
directory, mounted read-only. It never creates or changes checkouts and
holds no forge credentials.

## Build and test

Requires Go 1.27 or newer. From this directory:

```sh
go build -o /tmp/forge-search-service ./cmd/forge-search-service
go vet ./...
go test -race ./...
```

The module uses relative replacements for `../library`,
`../../forge-checkouts/*`, `../../forge-issues/library`,
`../../forge-symbols/library`, `../../forge-connector/library` and
`../../forge-core/*`; keep that checkout layout. Generated Go files are
committed in `searchpb/`. After editing the proto, run `buf generate` here.

Build the image from the repository root:

```sh
docker build -f forge-search/service/Dockerfile -t forge-search-service .
```

The image is pure Go on distroless, runs as user 65532, keeps its indexes in
the `/search-data` volume and listens on 9090 (gRPC) and 8080 (REST). The
path leaves `/data` free for the forge-checkouts data directory, mounted
read-only where forge-checkouts has it, as in the
[full-stack example](../../examples/fullstack/).

## Run

```sh
openssl rand -hex 32 > service-token
chmod 600 service-token
FORGE_SEARCH_CHECKOUTS_TOKEN="$(cat checkouts-token)" /tmp/forge-search-service \
  --token-file service-token \
  --checkouts-addr 127.0.0.1:9098 --checkouts-insecure true \
  --checkouts-data-dir ../../forge-checkouts/service/checkout-data
```

`checkouts-token` is the service token of forge-checkouts.

| Flag                            | Environment variable                       | Default                                               |
| ------------------------------- | ------------------------------------------ | ----------------------------------------------------- |
| `--grpc-listen`                 | `FORGE_SEARCH_GRPC_LISTEN`                 | `127.0.0.1:9105`                                      |
| `--http-listen`                 | `FORGE_SEARCH_HTTP_LISTEN`                 | `127.0.0.1:8095`                                      |
| `--token-file`                  | `FORGE_SEARCH_TOKEN_FILE`                  | none                                                  |
| `--token-env`                   | `FORGE_SEARCH_TOKEN_ENV`                   | `FORGE_SEARCH_TOKEN` when no source is set            |
| `--request-timeout`             | `FORGE_SEARCH_REQUEST_TIMEOUT`             | `30s`                                                 |
| `--data-dir`                    | `FORGE_SEARCH_DATA_DIR`                    | `./search-data`                                       |
| `--checkouts-addr`              | `FORGE_SEARCH_CHECKOUTS_ADDR`              | `127.0.0.1:9098`                                      |
| `--checkouts-insecure`          | `FORGE_SEARCH_CHECKOUTS_INSECURE`          | `false`: TLS                                          |
| `--checkouts-token-file`        | `FORGE_SEARCH_CHECKOUTS_TOKEN_FILE`        | none                                                  |
| `--checkouts-token-env`         | `FORGE_SEARCH_CHECKOUTS_TOKEN_ENV`         | `FORGE_SEARCH_CHECKOUTS_TOKEN` when no source is set  |
| `--checkouts-data-dir`          | `FORGE_SEARCH_CHECKOUTS_DATA_DIR`          | required                                              |
| `--checkouts-reported-data-dir` | `FORGE_SEARCH_CHECKOUTS_REPORTED_DATA_DIR` | `--checkouts-data-dir`                                |
| `--index-timeout`               | `FORGE_SEARCH_INDEX_TIMEOUT`               | `10m`                                                 |
| `--max-indexes`                 | `FORGE_SEARCH_MAX_INDEXES`                 | `32`                                                  |

Both tokens are read at startup, and the service does not start without
them. `--checkouts-data-dir` is the forge-checkouts data directory as
mounted here. When forge-checkouts sees it at another path, as in separate
containers, set `--checkouts-reported-data-dir` to its `--data-dir` there.
`--request-timeout` bounds every call but `IndexCheckout`, which
`--index-timeout` bounds. The connector flags that every service accepts are
ignored.

The data directory is created with mode 0700, and the service refuses one
that the group or others can access. It holds one SQLite database, whose
files are mode 0600.

### Checkouts

A checkout is read only when forge-checkouts reports it `ready` or
`retained`, and its location is
`<reported data dir>/copies/<id>`. It is then opened under
`<checkouts data dir>/copies` without following a symlink out of it. Anything
else fails with `FAILED_PRECONDITION` before a file is read. Entries named
`.git` are never read.

## Limits

| Limit              | Value                                                     |
| ------------------ | --------------------------------------------------------- |
| Request            | 64 KiB, gRPC or REST, checked before the body is read     |
| Indexes            | `--max-indexes`                                           |
| File               | 1 MiB; larger files are skipped and counted in `tooLarge` |
| Index              | 100000 documents and 1 GiB                                |
| Indexed checkout   | 100000 entries, 64 levels deep and 1 GiB                  |
| Unindexed checkout | 10000 entries, 64 levels deep and 64 MiB                  |
| Results            | `maxResults`: 100 by default, at most 500                 |

A checkout over the index or checkout limits fails `IndexCheckout` with
`RESOURCE_EXHAUSTED` and leaves the index as it was. An unindexed search
over 64 MiB reads the files in match order and sets `files.truncated`.
Indexed and unindexed searches agree whenever the unindexed one is within
its limits.

## API

Every call needs `Authorization: Bearer <token>`. The protobuf definition in
[proto/](proto/forge/search/v1/search.proto) is the source of truth and
documents every field.

| Method and path                          | Body         | Returns                                                       |
| ---------------------------------------- | ------------ | ------------------------------------------------------------- |
| `POST /v1/indexes/{index}/checkout`      | `checkoutId` | `index`, `added`, `replaced`, `removed`, `unchanged`, `files` |
| `GET /v1/indexes`                        |              | `indexes`                                                     |
| `GET /v1/indexes/{index}`                |              | `index`                                                       |
| `DELETE /v1/indexes/{index}`             |              | nothing                                                       |
| `POST /v1/indexes/{index}:search`        | `query`      | `matches`, `state`, `revision`                                |
| `POST /v1/checkouts/{checkoutId}:search` | `query`      | `matches`, `state`, `revision`, `files`                       |

An index name is 1 to 64 bytes of `a-z`, `0-9`, `.`, `-` and `_`, starts
with a letter or digit and holds no `..`. An index is `{name, documents,
bytes, revision, checkoutId, updatedAt}`: the files it holds, their size,
and the checkout revision, checkout and time of the last completed
`IndexCheckout`.

```sh
search() {
  curl -fsS -H "Authorization: Bearer $(cat service-token)" "http://127.0.0.1:8095$@"
}
```

### Index a checkout

Creates the index if needed, then syncs the checkout into it: only the files
that changed since the last sync are written, and files that are gone are
removed. Indexing another checkout into the same index replaces its
content the same way, so an index follows a branch from one checkout to the
next. One `IndexCheckout` or `DeleteIndex` runs on an index at a time;
another fails with `ABORTED`. Searches go on meanwhile.

```sh
search /v1/indexes/main/checkout -d "{\"checkoutId\": \"$CHECKOUT\"}" |
  jq '{revision: .index.revision, added, replaced, removed, unchanged}'
```

### Search

A query is `{text, pathPrefix, maxResults}`. The text is literal and
case-sensitive, 1 to 256 bytes, without line breaks. `pathPrefix` keeps the
paths that start with it, such as `lib/`. Matches are in path order, then by
line, with at most one match per line. Each is `{path, version, range,
preview, previewColumn}`; ranges have one-based lines and UTF-16 columns with
an exclusive end column, as in forge-explorer and forge-symbols. `version`
is the hex SHA-256 of the file content. `state.truncated` is set when more
matches exist; `state.skipped` counts binary files, which are not searched.

```sh
search /v1/indexes/main:search -d '{"query": {"text": "Greet", "pathPrefix": "lib/"}}' |
  jq -r '.matches[] | "\(.path):\(.range.startLine) \(.preview)"'
search "/v1/checkouts/$CHECKOUT:search" -d '{"query": {"text": "Greet"}}' |
  jq -r '.matches[] | "\(.path):\(.range.startLine) \(.preview)"'
```

### Delete an index

Removes the index, then checkpoints the database so that its content leaves
the storage files. The checkpoint rewrites the full-text index of every
index, so it takes time in proportion to all of them.

```sh
search /v1/indexes/main -X DELETE
```

## Errors

Errors carry only a status code; they never echo request content, paths or
tokens.

| Code                  | HTTP | When                                                                                      |
| --------------------- | ---- | ----------------------------------------------------------------------------------------- |
| `UNAUTHENTICATED`     | 401  | The token is missing or wrong. Checked before the body is read.                           |
| `INVALID_ARGUMENT`    | 400  | An index name, checkout ID, query or `maxResults` is invalid, or the body is malformed.   |
| `NOT_FOUND`           | 404  | The index, or the checkout in forge-checkouts, does not exist.                            |
| `FAILED_PRECONDITION` | 400  | The checkout is not `ready` or `retained`, or is not readable under the checkouts root.   |
| `ABORTED`             | 409  | The index is being indexed or deleted, or the checkout changed while it was read.         |
| `RESOURCE_EXHAUSTED`  | 429  | The request, checkout, index or number of indexes is over its limit, or the disk is full. |
| `UNAVAILABLE`         | 503  | forge-checkouts cannot be reached or rejects the token, or storage is read-only.          |
| `DEADLINE_EXCEEDED`   | 504  | The call took too long.                                                                   |
| `INTERNAL`            | 500  | Storage failed.                                                                           |

## License

[MIT](../../LICENSE)
