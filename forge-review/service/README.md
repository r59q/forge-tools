# forge-review service

REST and gRPC for the [review library](../library/), backed by durable storage.
The connector is optional. Supplied diffs and stored standalone reviews work
without it; connected captures and native operations discover its sessions.

## Build, test and run

From this directory, with Go 1.27:

```sh
go build ./cmd/forge-review-service
go vet ./...
go test -race ./...
./forge-review-service --data-dir ./review-data --token-file service-token
```

Add `--connector-url http://127.0.0.1:8081` and
`--connector-token-file connector-token` for connected subjects/native reviews.
Forge credentials remain in forge-connector.

| Flag                     | Environment variable                | Default                        |
| ------------------------ | ----------------------------------- | ------------------------------ |
| `--data-dir`             | `FORGE_REVIEW_DATA_DIR`             | `./review-data`                |
| `--grpc-listen`          | `FORGE_REVIEW_GRPC_LISTEN`          | `127.0.0.1:9097`               |
| `--http-listen`          | `FORGE_REVIEW_HTTP_LISTEN`          | `127.0.0.1:8087`               |
| `--token-file`           | `FORGE_REVIEW_TOKEN_FILE`           | none                           |
| `--token-env`            | `FORGE_REVIEW_TOKEN_ENV`            | `FORGE_REVIEW_TOKEN`           |
| `--connector-url`        | `FORGE_REVIEW_CONNECTOR_URL`        | disabled                       |
| `--connector-token-file` | `FORGE_REVIEW_CONNECTOR_TOKEN_FILE` | none                           |
| `--connector-token-env`  | `FORGE_REVIEW_CONNECTOR_TOKEN_ENV`  | `FORGE_REVIEW_CONNECTOR_TOKEN` |
| `--request-timeout`      | `FORGE_REVIEW_REQUEST_TIMEOUT`      | `30s`                          |

Flags override environment settings. Choose a file or variable source per
token; `--token-env` names a variable, not the token. Tokens are reread for
rotation. This is a trusted application API: the service token grants access
to all stored reviews and configured sessions. Local author names are caller
attribution, not verified users. Add application authorization where needed.
Remote deployments need a TLS endpoint preserving Authorization.

## Persistence

Snapshots and standalone reviews use a versioned JSON database. Updates acquire
an advisory process-shared lock, write a private temporary file, fsync it,
rename it atomically, then fsync the directory. Failed callbacks roll back;
corrupt/unknown-version databases are rejected. OS locks release on process
exit, so crashes do not leave a stale lock. A failed directory sync after
rename has an ambiguous result; reread before retrying.

Use a local Unix filesystem supporting flock, fsync and atomic rename. Keep
the data directory private, writable by the service and on a persistent volume.
Multiple service processes can share it on the same local filesystem. Each
operation reads the database and each update rewrites it: this initial store
suits modest installations, not large distributed deployments. Back up
`reviews.json`; temporary files and `store.lock` are not the database.
There is no automatic pruning. Native feedback lives at the forge; only its
subject snapshot is stored here. Native lists are live and include other
revisions. Native mutations are never retried automatically.

## API

[review.proto](proto/forge/review/v1/review.proto) generates both transports. Every call needs
`Authorization: Bearer <service token>` (gRPC metadata). REST ignores
`Grpc-Metadata-*` headers. Errors contain only status codes, not forge bodies.
JSON uses lowerCamelCase, RFC 3339 timestamps, and strings for int64 numbers.
Requests are limited to 20 MiB; snapshots to 16 MiB plus documented line limits.
Increase gRPC clients' receive limit above 4 MiB for large snapshots/reviews.
Responses disable caching. Graceful shutdown drains for up to five seconds.

| RPC                 | REST                                                                |
| ------------------- | ------------------------------------------------------------------- |
| CreateSubject       | `POST /v1/subjects`                                                 |
| GetSubject          | `GET /v1/subjects/{subjectId}`                                      |
| RequestReview       | `POST /v1/reviews`                                                  |
| ListReviews         | `GET /v1/reviews?subjectId=&reviewer=&status=`                      |
| GetReview           | `GET /v1/reviews/{reviewId}`                                        |
| AddComment          | `POST /v1/reviews/{reviewId}/threads`                               |
| Reply               | `POST /v1/reviews/{reviewId}/threads/{threadId}/replies`            |
| Resolve             | `PUT /v1/reviews/{reviewId}/threads/{threadId}/resolution`          |
| Submit              | `POST /v1/reviews/{reviewId}/submission`                            |
| GetCapabilities     | `GET /v1/sources/{source}/capabilities`                             |
| RequestNativeReview | `POST /v1/subjects/{subjectId}/native/reviewers`                    |
| ListNative          | `GET /v1/subjects/{subjectId}/native/{resource}?reviewId=`          |
| SubmitNative        | `POST /v1/subjects/{subjectId}/native/submission`                   |
| CommentNative       | `POST /v1/subjects/{subjectId}/native/threads`                      |
| ReplyNative         | `POST /v1/subjects/{subjectId}/native/threads/{threadId}/replies`   |
| ResolveNative       | `PUT /v1/subjects/{subjectId}/native/threads/{threadId}/resolution` |

Create a supplied subject with:

```json
{"kind":"patch","patch":"--- a/file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new\n"}
```

Optional `context` entries contain `path`, `side` (`old`/`new`) and complete
`content`. A comparison uses `kind: "comparison"`, `ref: {source, repository}`,
`from` and `to`. A change request uses `kind: "change_request"` and
`ref: {source, repository, number}`. Connected input cannot override forge text.
`GetSubject` provides the captured diff/context; later pushes never alter it.

Request a local assignment with `{subjectId, reviewer}`. AddComment needs
`author`, `body` and optional `anchor: {path, side, start, end}`. Zero start/end
means file-level; omit anchor for general feedback. Reply takes author/body;
Resolve takes `resolved: true/false`. Submit takes `status` (`commented`,
`approved`, `changes_requested`) and optional `body`. Non-approval submissions
need a body or existing thread. Submitted assessments are immutable; replies
and resolution remain available. List filters are optional.

Native routes publish immediately as the session user. RequestNativeReview
accepts `reviewers` (usernames, or GitLab numeric IDs as strings).
SubmitNative takes status/body; CommentNative takes body/anchor; ReplyNative
takes body; ResolveNative takes resolved. ListNative resource is `reviews`,
`threads` or `reviewers`; Forgejo thread lists require `reviewId`. Native
responses have `storage: "native"`, `subjectId`, `resource` and `dataJson`
containing unmodified forge JSON. Check native revision fields when displaying
these live results next to a snapshot. See the [capability matrix](../README.md)
for unsupported operations, version limits and non-atomic native guards.

| Cause                                     | gRPC                | HTTP |
| ----------------------------------------- | ------------------- | ---- |
| Missing/wrong service token               | UNAUTHENTICATED     | 401  |
| Invalid input/anchor/source               | INVALID_ARGUMENT    | 400  |
| Missing subject/review/thread             | NOT_FOUND           | 404  |
| Submitted review/changed revision         | FAILED_PRECONDITION | 400  |
| Unsupported capability/connector disabled | UNIMPLEMENTED       | 501  |
| Forge permission denied                   | PERMISSION_DENIED   | 403  |
| Oversized snapshot/rate limit             | RESOURCE_EXHAUSTED  | 429  |
| Deadline                                  | DEADLINE_EXCEEDED   | 504  |
| Storage/connector/upstream failure        | UNAVAILABLE         | 503  |

## Container

Build from the repository root:

```sh
docker build -f forge-review/service/Dockerfile -t forge-review-service .
docker run -p 127.0.0.1:8087:8080 -p 127.0.0.1:9097:9090 \
  -v review-data:/data -e FORGE_REVIEW_TOKEN forge-review-service
```

The image runs as UID/GID 65532 with data in `/data`. Bind-mounted directories
must be writable by that user. Use persistent named volumes across container
replacements. Workflows test/build pull requests without registry access and
publish service images on pushes to master.

## Regenerate

Generated files are committed in `reviewpb/`. To regenerate, install
[buf](https://buf.build/docs/installation) 1.74.0 and Go, then run from
this folder:

```sh
buf generate
go vet ./...
go test ./...
```

CI fails when the generated code is out of date.

## License

[MIT](../../LICENSE)
