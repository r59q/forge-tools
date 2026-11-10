# forge-checkouts library

Manages Git worktrees and isolated repositories on Unix. Requires Go 1.27
and Git on `PATH`. From this directory:

```sh
go build ./...
gofmt -l .
go vet ./...
go test -race ./...
```

The module uses `../../forge-core/forgeapi`; keep the checkout layout.

## Use

```go
store := &checkouts.MemoryStore{} // Supply a durable Store for restart recovery.
manager, err := checkouts.New(ctx, "/tmp/example-checkouts", store, nil)
if err != nil { return err }
defer manager.Close()
w, err := manager.Create(ctx, checkouts.Input{
    Repository: checkouts.Repository{LocalPath: "/home/me/project"},
    Reference: "main",
})
if err != nil { return err }
status, err := manager.Inspect(ctx, w.ID)
_ = status
if err != nil { return err }
return manager.Remove(ctx, w.ID, false)
```

`Create` accepts a branch, tag or commit; empty reference means `HEAD`. Each
checkout starts detached, allowing multiple copies of the same branch. Metadata
keeps the requested reference and initial commit. `Inspect` reports current HEAD,
the current branch if a consumer attached one, and Git porcelain file statuses.
`ReadDiff` separates staged and unstaged binary-capable patches, capped at 8 MiB
each. Untracked/ignored contents are excluded from patches but listed in status.
Submodules and Git LFS content are not downloaded automatically.

`Input.Mode` and `Checkout.Mode` are `worktree` (the default) or `isolated`.
Worktrees share refs, config and objects with their source and require trusted
writers. Isolated copies have their own `.git`, refs, index, config, hooks and new
objects. Plain Git commit, rebase, stash, bisect, log and blame work in the copy.
Its object alternates borrow from the managed cache. Isolated mode rejects local
sources with `ErrInvalid`: their owners may prune objects at any time, and this
tool must not pin refs in caller-owned repositories.

For an isolated connected copy, set `Mode: checkouts.Isolated` alongside
`Repository{Source: "connection-id", Path: "owner/repo"}`. The cache holds
`refs/checkouts/<id>` at the initial revision until removal, including for failed
copies. Creation records its intent before pinning and creates files only after
the pin and revision are saved. Restart reconciles pins with records, removing
orphan refs and restoring missing pins before serving operations. Future cache
cleanup must preserve these refs and serialize with creation/removal.

`List`, `Get` and `Retain` operate on saved metadata. States are `creating`,
`ready`, `retained`, `removing` and `failed`. There is no expiry: even ordinary
ready checkouts remain until explicitly removed. `Remove` refuses staged,
modified, untracked and ignored files unless `discard` is true. Stop processes
writing to the checkout before removal; filesystem edits outside this manager
cannot be locked. `Close` releases ownership without deleting working copies.

For connected repositories, provide a `Resolve` callback returning the matching
connection's `Git` endpoint from `connector.Client.Connections`. Each creation
resolves authorization again and fetches the requested ref. Git uses a temporary,
random loopback route which forwards fetches through the authenticated HTTP
client. No forge credentials or temporary remote URLs are persisted or passed
to Git. Existing copies remain usable when the connector is unavailable.
New caches detect SHA-1 or SHA-256 from the remote refs before fetching.

## Storage and ownership

`Store.Read` must return isolated snapshots. `Store.Update` must serialize access,
commit successful callbacks atomically and roll back errors. Records contain
public metadata and a private backing Git directory. `MemoryStore` is provided
for temporary use; the [service store](../service/storage/) uses fsync and atomic
rename for durability.

Use one manager with one store per private root, on a local filesystem supporting
`flock`. Do not move either independently. The manager locks its root for its
lifetime and serializes operations with context-aware waiting. Interrupted
creations/removals become `failed` on restart. Partial files remain available for
inspection and explicit removal; a failed creation is never marked ready.

- `copies/<id>` contains tool-owned worktrees. Only these are removed.
- In isolated mode, `copies/<id>` instead contains a repository with its own `.git`.
- `shadows/` holds disposable, host-owned Git directories used for isolated reads.
- `repositories/<hash>` contains tool-owned connected bare caches, reused by
  source ID and repository path. Caches remain after checkout removal.
- Local repositories stay caller-owned. Their canonical common Git directory is
  recorded as repository identity. Worktree registration changes there, but their
  branches, checkouts and working files remain untouched.

Back up metadata and files together. Keep existing local repositories, their
configuration and the data directory trusted. Neither mode runs or sandboxes
processes. Untrusted processes may write only their isolated copy; enforce this
with OS isolation. Keep caches read-only and reachable at their original paths
for Git alternates, and keep metadata and shadows inaccessible to those processes.

Host `Inspect`, `ReadDiff` and removal checks use a fresh shadow Git directory
with trusted config and an explicit work tree. HEAD, refs and index are copied as
data. Copy-local objects are snapshotted into a private alternate alongside the
cache, avoiding untrusted object symlinks and alternate paths. This adds I/O
proportional to objects created in the copy. Filters, config includes, hooks and
`core.worktree` from the copy are never used. Submodule contents are not inspected
in isolated mode, since nested repositories have untrusted config too. Stop
writers before inspecting for a consistent snapshot, and always before removal.

Never run host Git from an isolated copy, including for pushes. Move commits into
a trusted repository first. Commit transfer and push are outside this library.
Git inside the sandbox may use the copy's config; host operations must use the
manager's shadow. No ownership checks are disabled.

[MIT](../../LICENSE)
