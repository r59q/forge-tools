# forge-code library

Go module: `github.com/r59q/forge-tools/forge-code/library`.
It uses the local `forge-core/forgeapi` module through a relative `replace` and
has no other dependencies. Sessions come from forge-connector; the library
never handles tokens or connection details. A repository is just a path such
as `owner/name` or `group/sub/project`.

Package `code` reads repository content on GitHub, GitLab and Forgejo through
one model: refs, commits, history, trees, files and blobs, merge bases and
diffs. Writes push commits and ref updates with the Git protocol.

## Sources

A `Source` is an ID, an open `forgeauth` session for the REST API and an
optional `forgeauth.GitRemote` for pushes:

```go
conns, err := connectorClient.Connections(ctx)
sources := make([]code.Source, len(conns))
for i, conn := range conns {
	sources[i] = code.Source{ID: string(conn.ID), Session: conn.Session, Git: conn.Git}
}
c, err := code.New(sources...)
```

`Capabilities` says what a source supports. Writes need `Source.Git`.

## Revisions

One grammar on every forge:

- a full 40 or 64 digit object ID;
- `HEAD` (or empty), the default branch;
- `refs/heads/X` or `refs/tags/X`;
- a bare `X`, looked up as a tag and a branch. If both exist the call fails
  with `ErrAmbiguous`.

Each may end in `~N` or `^N` suffixes, N up to 100. Short object IDs, ranges,
`@{...}`, `:` and other ref namespaces are `ErrInvalid`.

## Reads

`Resolve`, `ListRefs`, `Commit`, `History`, `Tree`, `Stat`, `ReadFile`,
`ReadBlob` and `MergeBase`.

- Each call resolves its revision once and then reads only by object ID.
  Results name the commit they read (`Tree.Commit`, `Blob.Commit`,
  `HistoryPage.Tip`), so a branch that moves mid-call never mixes snapshots.
- `History` pages with opaque cursors that keep the first page's tip.
- `ReadFile` never follows symlinks. Readers stream and fail with
  `ErrTooLarge` past `Limits.MaxBlobBytes`. Git LFS pointers are flagged;
  LFS content is never fetched.
- Each call makes at most `Limits.MaxCalls` forge requests. Listings stop
  early and set `Truncated`; other calls fail with `ErrTooLarge`.
- Nothing is cached between calls.

## Compare

`Compare` lists the changes from base to head. The library computes them
from trees and blobs; forge compare APIs are never used.

- `MergeBase` mode (the default) compares head with the merge base, like
  `git diff base...head`. `Direct` compares base with head.
- Statuses are added, deleted, modified, renamed (exact content match only)
  and type changed. Mode changes count as modified. Changes are sorted by
  path.
- Only subtrees whose IDs differ are listed. GitLab reports no root tree
  ID, so both root listings are always read there.
- `Paths` keeps changes at or below whole path components.
- `Patches` adds hunks without file headers. Binary files (a NUL in the
  first 8000 bytes), pure renames and mode-only changes get none.

| Option | Default | When exceeded |
|---|---|---|
| `MaxChanges` | 3000 | `Truncated`; the kept changes are not always the first by path |
| `MaxPatchFiles` | 100, at most 1000 | later changes get `PatchOmitted` |
| `MaxPatchBytes` | 256 KiB per patch, at most 1 MiB | `PatchOmitted`; line counts are kept. Blobs over 16 times this get no patch |
| Patch reads | 64 MiB of blobs per `Compare` | later changes get `PatchOmitted` |
| `Limits.MaxCalls` | 1000 | `ErrTooLarge` while resolving, `Truncated` while walking, `PatchOmitted` while reading blobs |

## Writes

Writes push with the Git protocol and need `Source.Git`, otherwise
`ErrUnsupported`. Through forge-connector this is the proxy's `/g/` route,
which only accepts pushes when the proxy runs with `AllowGitPush`. Otherwise
the route is hidden and writes fail with `forgehttp.ErrNotFound`.

- `CreateCommit` applies `Changes` to the tree of `Parents[0]` (an empty
  tree without parents), builds the commit locally and pushes it, moving
  `Ref` from `Old` in the same push. Only directories on changed paths are
  read. Commits are unsigned and the same input gives the same commit ID on
  every forge.
- `UpdateRefs` creates, moves and deletes refs in one atomic push: all
  apply or none.
- Every update is a compare-and-swap. `Old` of 40 zeros means the ref must
  not exist; `New` of 40 zeros deletes it. Short and SHA-256 IDs are
  rejected.

Errors:

- `ErrStale`: a ref was not at `Old`, before or during the push. Nothing
  changed; read the ref again and retry.
- `ErrRejected`: the forge refused the push, for example branch protection,
  a hook or a missing object. Retrying the same push will not help.
- `ErrUnsupported`: several updates on a forge without atomic pushes,
  deletes it does not allow, SHA-256 repositories, or trees the library
  cannot rebuild exactly.

A push is a real push: forge hooks, webhooks, CI and branch protection run
as for any `git push`. If a push fails in transit, the library reads the
refs again and reports success only if every ref is at `New`.

## Limits

| Setting | Default | When exceeded |
|---|---|---|
| `Limits.MaxBlobBytes` | 100 MiB | `ErrTooLarge`, while streaming or up front when the size is known |
| `Limits.MaxCalls` | 1000 forge requests per call | listings set `Truncated`; other calls fail with `ErrTooLarge` |
| `Limits.MaxWriteBytes` | 64 MiB of content per commit | `ErrTooLarge` |
| `RefsOptions.Limit` | 10000 refs | `Truncated`; branches come before tags |
| `HistoryOptions.PageSize` | 50, at most 100 | `ErrInvalid` |
| `TreeOptions.MaxEntries` | 100000 entries | `Truncated` |

A truncated tree keeps the first entries in the forge's listing order, so
which entries come back differs per forge. Entries are always sorted by path.

## Forge mapping

Paths are relative to the session base. GitLab `:id` is the URL-encoded
project path. Every read after `Resolve` uses only object IDs.

| Read | GitHub | GitLab | Forgejo |
|---|---|---|---|
| `HEAD` | `GET /repos/{o}/{r}` | `GET /projects/:id` | `GET /repos/{o}/{r}` |
| Commit by ID | `git/commits/{id}` | `repository/commits/{id}` | `git/commits/{id}` |
| Ref | `git/ref/{name}`, tags peeled with `git/tags/{id}` | `repository/branches/{b}` or `repository/tags/{t}` | `git/refs/{name}` (exact match picked), tags peeled with `git/commits/{id}` |
| `ListRefs` | unpaged `git/matching-refs/{heads,tags}/`, peeled from paged `/tags`; tags read again to catch moves | paged `repository/branches` and `repository/tags` | unpaged `git/refs/{heads,tags}/`, peeled from paged `/tags` |
| `History` | `commits?sha=&path=` | `repository/commits?ref_name=&path=&follow=false` | `commits?sha=&path=:(literal)…`, always 50 per forge page |
| `Tree`, `Stat` | `git/trees/{id}` per path segment, `?recursive=1` (subtree walk if cut) | `repository/tree?ref=&path=`, keyset paging | `git/trees/{id}` per path segment, paged |
| `ReadFile` | tree walk, then `git/blobs/{id}` (raw) | parent `repository/tree?ref=&path=`, then `repository/blobs/{id}/raw` | `raw/{p}?ref=` |
| `ReadBlob` | `git/blobs/{id}` (raw) | `repository/blobs/{id}/raw` | `git/blobs/{id}` (base64 JSON) |
| `MergeBase` | `compare/{a}...{b}` | `repository/merge_base` | computed from `commits?sha=&not=` listings |

Typical cost: `Resolve` takes 1 request for an ID or full ref name, 2 for
a bare name (looked up in parallel), plus 1 per `~`/`^` step and, for an
annotated tag, 1 per tag level on GitHub and 1 on Forgejo. After that,
`ReadFile` takes 1 request on Forgejo, 2 on GitLab (plus 1 per 100 entries
before the file in its directory), and on GitHub 2 plus 1 per directory in
the path. Trees are memoised within one call only.

### Differences between forges

- **GitLab** does not report tree IDs or entry sizes: `Commit.Tree` and
  `Tree.ID` are empty, and `Entry.Size` is -1 (`Capabilities.TreeIDs` is
  false). File sizes from `ReadFile` are known.
  `CreateCommit` fetches the parent commit object, without trees, over
  `git-upload-pack` to learn its tree ID.
- **Forgejo** `Resolve` and `ListRefs` peel tag chains of any depth, but
  `ListRefs` follows a tag that points to another annotated tag only if
  the inner tag has a ref of its own. Otherwise it reports the inner tag
  object as `Ref.Commit`. `ReadBlob` on a tree or commit ID returns its raw
  content, and blobs over the forge's API limit (10 MiB by default) are
  `ErrTooLarge`; use `ReadFile`. A criss-cross merge base is
  `ErrUnsupported`. Path history needs the default `[git]
  COMMITS_RANGE_SIZE` and `[api] MAX_RESPONSE_ITEMS` of 50; otherwise the
  forge skips commits and `History` returns `ErrUnsupported`.
- **GitHub** blobs over 100 MB are `ErrTooLarge`. Tag chains over 4 levels
  are `ErrUnsupported` in `Resolve`. `git/matching-refs` is not paged, so a
  ref listing over 32 MiB is `ErrTooLarge`.
- **GitLab** before 18.10 ignores `follow=false`: path history follows
  renames unless the `remove_file_commit_history_following` flag is on.
- `ListRefs` is one snapshot on Forgejo only; GitHub and GitLab take
  several requests.
- History order is each forge's log order, not topological. Renames are not
  followed, except on older GitLab (above).
- Commit messages lose their trailing newline on every forge, as GitHub
  strips it; GitLab may sanitise invalid UTF-8.
- A default branch whose name the library refuses (`%` or a leading `-`)
  is still `RefList.Default`, but `HEAD` is `ErrUnsupported`.
- Signature times keep the forge's time zone; GitHub sends UTC.

## Errors

Forge failures are `*forgehttp.Error` values, so `errors.Is` works with the
`forgehttp` sentinels. A missing revision or path wraps `ErrRevisionNotFound`
or `ErrPathNotFound` and also matches `forgehttp.ErrNotFound`, whatever status
the forge sent. Other sentinels: `ErrInvalid`, `ErrUnknownSource`,
`ErrNotFile`, `ErrAmbiguous`, `ErrTooLarge`, `ErrUnsupported`, `ErrStale` and
`ErrRejected`.

## Develop

From this directory:

```sh
gofmt -l .
go vet ./...
go test ./...
```

Tests use local fakes and `httptest` forges; no credentials or network access
are needed. `code/conformance_test.go` runs the same reads on the GitHub,
GitLab and Forgejo fakes and checks that they agree. Tests that need `git`
skip when it is not on `PATH`.
