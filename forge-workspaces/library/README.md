# forge-workspaces library

Manages independent working copies using Git worktrees on Unix. Requires Go 1.27
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
store := &workspaces.MemoryStore{} // Supply a durable Store for restart recovery.
manager, err := workspaces.New(ctx, "/tmp/example-workspaces", store, nil)
if err != nil { return err }
defer manager.Close()
w, err := manager.Create(ctx, workspaces.Input{
    Repository: workspaces.Repository{LocalPath: "/home/me/project"},
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

`List`, `Get` and `Retain` operate on saved metadata. States are `creating`,
`ready`, `retained`, `removing` and `failed`. There is no expiry: even ordinary
ready workspaces remain until explicitly removed. `Remove` refuses staged,
modified, untracked and ignored files unless `discard` is true. Stop processes
writing to the workspace before removal; filesystem edits outside this manager
cannot be locked. `Close` releases ownership without deleting working copies.

For connected repositories, provide a `Resolve` callback returning the matching
connection's `Git` endpoint from `connector.Client.Connections`. Each creation
resolves authorization again and fetches the requested ref. Git uses a temporary,
random loopback route which forwards fetches through the authenticated HTTP
client. No forge credentials or temporary remote URLs are persisted or passed
to Git. Existing copies remain usable when the connector is unavailable.

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
- `repositories/<hash>` contains tool-owned connected bare caches, reused by
  source ID and repository path. Caches remain after workspace removal.
- Local repositories stay caller-owned. Their canonical common Git directory is
  recorded as repository identity. Worktree registration changes there, but their
  branches, checkouts and working files remain untouched.

Back up metadata and files together. Keep existing local repositories, their
configuration and the data directory trusted. Worktrees share objects and local
branches; they provide neither process isolation nor a multi-tenant boundary.

[MIT](../../LICENSE)
