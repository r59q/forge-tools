# forge-review library

An independent Go library for fixed review snapshots and human or automated
reviewers' feedback. It performs no automatic code analysis.

From this directory, with Go 1.27:

```sh
go build ./...
go vet ./...
go test -race ./...
```

```go
store := &review.MemoryStore{}
client, err := review.New(store) // no connection needed for a patch
if err != nil { return err }
subject, err := client.CreateSubject(ctx, review.Input{
    Kind: "patch",
    Patch: "--- a/file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new\n",
})
if err != nil { return err }
assignment, err := client.RequestReview(ctx, subject.ID, "alice")
if err != nil { return err }
_, err = client.AddComment(ctx, assignment.ID, "alice", "Please explain this change",
    &review.Anchor{Path: "file", Side: "new", Start: 1, End: 1})
if err != nil { return err }
_, err = client.Submit(ctx, assignment.ID, "changes_requested", "Needs explanation")
```

Import `github.com/r59q/forge-tools/forge-review/library/review`.
The caller supplies context and handles errors. `GetSubject` returns the patch,
changed files and all captured context; no other forge tool is required.

## Subjects and anchors

- `patch`: unified patch plus optional complete `FileContext` entries, keyed
  by path and `old`/`new` side. Context must agree with visible hunk lines.
- `comparison`: `Ref{Source, Repository}`, plus `From` and `To`. References
  resolve to exact commit IDs before any diff/file request.
- `change_request`: `Ref{Source, Repository, Number}`. Number is a pull-request
  number or GitLab IID, not a global ID. The revisions are checked before and
  after fetching the diff; concurrent changes fail with `ErrConflict`.

Supply connected sessions through `review.Source{ID, Session}` from
forge-connector. Neither forge-code nor forge-changes is a dependency.
Repository paths follow their existing unescaped owner/name or group/project
convention. Snapshots record head, target and available diff-base IDs.
GitHub comparisons use its merge-base diff; GitLab and Forgejo comparisons
use a direct comparison between the resolved commits. Forgejo comparisons
reconstruct hunks from pinned file contents using shared forge-core code.

Subject IDs hash the subject kind/reference, exact revisions, patch, sorted
context and file metadata. Identical captures reuse the stored snapshot and
creation time. A push cannot change it. Reads use storage only.
Forge-provided completeness is `unknown`: upstream limits may omit files.
Explicit truncation/timeouts and local size limits fail instead of claiming
completeness. GitHub/GitLab snapshots retain hunk context; Forgejo comparisons
also retain the complete text used to construct each textual diff.

Nil anchors are general feedback. File anchors need a path and side with zero
start/end. Line anchors use inclusive one-based start/end. Every line must
exist in that side's hunk text or supplied complete context. Additions have no
old side; deletions have no new side. Binary files allow file comments only.
Malformed/overlapping hunks and ambiguous paths fail validation. Quote Git
header paths when spaces followed by `b/` make the old/new boundary ambiguous.
Encoded `GIT binary patch` payloads are unsupported; binary-file markers are
supported.
Snapshots are limited to 16 MiB of patch plus context and 200,000 patch lines.

## Standalone lifecycle and storage

`RequestReview` creates one assignment with status `requested`. `AddComment`
opens threads before submission. `Reply` and `Resolve` remain available after
submission; resolution can be reversed. `Submit` records `commented`,
`approved` or `changes_requested`. Identical submissions are idempotent;
changing a submitted assessment requires a new assignment.
`GetReview` and `ListReviews` expose status, threads and timestamps.

`Store` is the persistence boundary. `Read` returns isolated data; `Update`
serializes writers and atomically commits a successful callback. The included
`MemoryStore` is for embedding/tests. The service supplies durable storage.
Caller-supplied author/reviewer names are attribution, not authentication;
applications must enforce their own user permissions. An approval never grants
merge permission by itself.

## Native operations

Native calls are separate and explicitly named. `RequestNativeReview`,
`SubmitNative`, `CommentNative`, `ReplyNative` and `ResolveNative` publish
immediately through the session user. There is no local-to-native conversion.
`ListNative` reads native reviews, discussions or assignments, preserving forge
JSON, IDs, statuses and revision fields in `NativeResult.Data`. Lists include
other revisions and may change over time; the subject snapshot remains fixed.
Forgejo thread lists require a native review ID. Reviewer lists on GitLab and
Forgejo retain the full native proposal object containing assignments.

Assessments and new comments check the current proposal revision and use a
commit ID or diff position where the API supports it. Reviewer requests and
general GitLab discussions lack an atomic revision guard. Replies/resolution
validate the original thread head and do not relocate it. General discussions
without revision metadata cannot be safely associated with a snapshot by
these operations and return `ErrUnsupported`.

Native writes are not retried automatically. A network failure after a write
may have published it: inspect `ListNative` before retrying. Capability errors
never fall back to local storage. See the [capability matrix](../README.md).

## License

[MIT](../../LICENSE)
