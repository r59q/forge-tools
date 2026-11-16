# forge-search service

Serves the [forge-search library](../library/) over gRPC and generated REST
routes: index the code of [forge-checkouts](../../forge-checkouts/service/)
checkouts, or of repository branches, into named, durable indexes and search
them, or search without an index. Both return the same matches in the same
order.

The service resolves checkout IDs through forge-checkouts, with its own
token, and reads the checkout directories from the forge-checkouts data
directory, mounted read-only. To index or search a repository reference it
creates a checkout, reads it and removes it again; it never changes the
files of a checkout and holds no forge credentials. Nothing is fetched in
the background: a repository index changes only when `IndexRepository` is
called.

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

| Flag                            | Environment variable                       | Default                                              |
| ------------------------------- | ------------------------------------------ | ---------------------------------------------------- |
| `--grpc-listen`                 | `FORGE_SEARCH_GRPC_LISTEN`                 | `127.0.0.1:9105`                                     |
| `--http-listen`                 | `FORGE_SEARCH_HTTP_LISTEN`                 | `127.0.0.1:8095`                                     |
| `--token-file`                  | `FORGE_SEARCH_TOKEN_FILE`                  | none                                                 |
| `--token-env`                   | `FORGE_SEARCH_TOKEN_ENV`                   | `FORGE_SEARCH_TOKEN` when no source is set           |
| `--request-timeout`             | `FORGE_SEARCH_REQUEST_TIMEOUT`             | `30s`                                                |
| `--data-dir`                    | `FORGE_SEARCH_DATA_DIR`                    | `./search-data`                                      |
| `--checkouts-addr`              | `FORGE_SEARCH_CHECKOUTS_ADDR`              | `127.0.0.1:9098`                                     |
| `--checkouts-insecure`          | `FORGE_SEARCH_CHECKOUTS_INSECURE`          | `false`: TLS                                         |
| `--checkouts-token-file`        | `FORGE_SEARCH_CHECKOUTS_TOKEN_FILE`        | none                                                 |
| `--checkouts-token-env`         | `FORGE_SEARCH_CHECKOUTS_TOKEN_ENV`         | `FORGE_SEARCH_CHECKOUTS_TOKEN` when no source is set |
| `--checkouts-data-dir`          | `FORGE_SEARCH_CHECKOUTS_DATA_DIR`          | required                                             |
| `--checkouts-reported-data-dir` | `FORGE_SEARCH_CHECKOUTS_REPORTED_DATA_DIR` | `--checkouts-data-dir`                               |
| `--index-timeout`               | `FORGE_SEARCH_INDEX_TIMEOUT`               | `10m`                                                |
| `--max-indexes`                 | `FORGE_SEARCH_MAX_INDEXES`                 | `32`                                                 |

Both tokens are read at startup, and the service does not start without
them. `--checkouts-data-dir` is the forge-checkouts data directory as
mounted here. When forge-checkouts sees it at another path, as in separate
containers, set `--checkouts-reported-data-dir` to its `--data-dir` there.
`--request-timeout` bounds every call but `IndexCheckout`,
`IndexRepository` and `SearchRepository`, which `--index-timeout` bounds;
a `SearchRepository` of an existing index searches within
`--request-timeout`. The connector flags that every service accepts are
ignored.

The data directory is created with mode 0700, and the service refuses one
that the group or others can access. It holds one SQLite database, whose
files are mode 0600, and the `checkouts` journal described below.

### Checkouts

A checkout is read only when forge-checkouts reports it `ready` or
`retained`, and its location is
`<reported data dir>/copies/<id>`. It is then opened under
`<checkouts data dir>/copies` without following a symlink out of it. Anything
else fails with `FAILED_PRECONDITION` before a file is read. Entries named
`.git` are never read.

### Repository checkouts

`IndexRepository`, and `SearchRepository` of a reference without an index,
create a checkout with `CreateCheckout`, read it as above and remove it with
`RemoveCheckout` and `discard`. The forge-checkouts token must allow both
calls; forge-checkouts has a single service token, so the one given with
`--checkouts-token-file` or `--checkouts-token-env` does.

forge-checkouts fetches the repository on every `CreateCheckout`, so each
call sees the branch as the forge has it then. An empty reference, or
`HEAD`, is the default branch.

At most 4 such checkouts exist at once; another call fails with
`RESOURCE_EXHAUSTED` before it creates one. A checkout is removed when its
call ends, whether it succeeded, failed, timed out or was cancelled; the
removal has 30 seconds of its own after the call's deadline.

Each checkout ID is written to `<data dir>/checkouts/<id>` before the
checkout is read, and deleted once forge-checkouts has removed it. At
startup, and before each new checkout, the service removes the journaled
checkouts no call is using, so those a crash or a failed removal left go
away. Two kinds of leftover are not journaled and need an operator: a
checkout forge-checkouts created when the service stopped, or the call
timed out, before its ID came back, and the `failed` record forge-checkouts
keeps when a checkout cannot be made, such as for an unknown reference.
Find them with `GET /v1/checkouts` on forge-checkouts and remove them with
`DELETE /v1/checkouts/{id}?discard=true`.

## Limits

| Limit                | Value                                                     |
| -------------------- | --------------------------------------------------------- |
| Request              | 64 KiB, gRPC or REST, checked before the body is read     |
| Indexes              | `--max-indexes`                                           |
| File                 | 1 MiB; larger files are skipped and counted in `tooLarge` |
| Index                | 100000 documents and 1 GiB                                |
| Indexed checkout     | 100000 entries, 64 levels deep and 1 GiB                  |
| Unindexed checkout   | 10000 entries, 64 levels deep and 64 MiB                  |
| Results              | `maxResults`: 100 by default, at most 500                 |
| Repository checkouts | 4 at once                                                 |
| Target               | `source` 128 bytes, `repository` 256, `reference` 256     |

A checkout over the index or checkout limits fails `IndexCheckout` with
`RESOURCE_EXHAUSTED` and leaves the index as it was. An unindexed search
over 64 MiB reads the files in match order and sets `files.truncated`.
Indexed and unindexed searches agree whenever the unindexed one is within
its limits.

## API

Every call needs `Authorization: Bearer <token>`. The protobuf definition in
[proto/](proto/forge/search/v1/search.proto) is the source of truth and
documents every field.

| Method and path                          | Body              | Returns                                                       |
| ---------------------------------------- | ----------------- | ------------------------------------------------------------- |
| `POST /v1/indexes/{index}/checkout`      | `checkoutId`      | `index`, `added`, `replaced`, `removed`, `unchanged`, `files` |
| `GET /v1/indexes`                        |                   | `indexes`                                                     |
| `GET /v1/indexes/{index}`                |                   | `index`                                                       |
| `DELETE /v1/indexes/{index}`             |                   | nothing                                                       |
| `POST /v1/indexes/{index}:search`        | `query`           | `matches`, `state`, `revision`                                |
| `POST /v1/checkouts/{checkoutId}:search` | `query`           | `matches`, `state`, `revision`, `files`                       |
| `POST /v1/repositories:index`            | `target`          | as for a checkout, and `upToDate`                             |
| `POST /v1/repositories:search`           | `target`, `query` | `matches`, `state`, `revision`, and `index` or `files`        |

An index name is 1 to 64 bytes of `a-z`, `0-9`, `.`, `-` and `_`, starts
with a letter or digit and holds no `..`. An index is `{name, documents,
bytes, revision, checkoutId, updatedAt, target}`: the files it holds, their
size, the checkout revision, checkout and time of the last completed
indexing, and for a repository index its target. The revision is empty
while an indexing is changing the index.

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

### Index a repository

A target is `{source, repository, reference}`: the forge-connector
connection ID, the repository path such as `owner/name`, and a branch, tag or
commit, empty for the default branch. Its index is named `repo-` and 40 hex
digits derived from the target, and `IndexCheckout` refuses names starting
with `repo-`. List, get and delete repository indexes as any other.

`IndexRepository` checks the target out, syncs it into its index as
`IndexCheckout` does and removes the checkout. When the index already holds
the revision of the checkout, it changes nothing and returns `upToDate`
with the index as it was. Calls for one target run one at a time; another
fails with `ABORTED` before it creates a checkout.

```sh
TARGET='{"source": "github", "repository": "octo/demo"}'
search /v1/repositories:index -d "{\"target\": $TARGET}" |
  jq '{name: .index.name, revision: .index.revision, upToDate, added, replaced, removed}'
```

### Search a repository

`SearchRepository` searches the index of the target when there is one and
returns it as `index`, with its revision and time. Without one, it searches
a checkout made for the call, removes it and returns the checkout revision
and `files`, under the unindexed limits. The matches and their order are
the same either way.

```sh
search /v1/repositories:search -d "{\"target\": $TARGET, \"query\": {\"text\": \"Greet\"}}" |
  jq -r '.revision, (.matches[] | "\(.path):\(.range.startLine) \(.preview)")'
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

| Code                  | HTTP | When                                                                                                                                   |
| --------------------- | ---- | -------------------------------------------------------------------------------------------------------------------------------------- |
| `UNAUTHENTICATED`     | 401  | The token is missing or wrong. Checked before the body is read.                                                                        |
| `INVALID_ARGUMENT`    | 400  | An index name, checkout ID, target, query or `maxResults` is invalid, or forge-checkouts refuses the target, or the body is malformed. |
| `NOT_FOUND`           | 404  | The index, or the checkout, source, repository or reference in forge-checkouts, does not exist.                                        |
| `FAILED_PRECONDITION` | 400  | The checkout is not `ready` or `retained`, is not readable under the checkouts root, or forge-checkouts refuses it in its state.       |
| `ABORTED`             | 409  | The index is being indexed or deleted, or the checkout changed while it was read.                                                      |
| `RESOURCE_EXHAUSTED`  | 429  | The request, checkout, repository, index, number of indexes or of repository checkouts is over its limit, or the disk is full.         |
| `UNAVAILABLE`         | 503  | forge-checkouts cannot be reached or rejects the token, or storage is read-only.                                                       |
| `DEADLINE_EXCEEDED`   | 504  | The call took too long.                                                                                                                |
| `INTERNAL`            | 500  | Storage failed, or the checkout journal cannot be written.                                                                             |

## License

[MIT](../../LICENSE)
