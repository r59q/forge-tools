# forge-changes library

`changes` manages proposals on GitHub, GitLab and Forgejo. It depends only on
forge-core HTTP/session helpers. The caller obtains authenticated sessions
from forge-connector; this library never reads tokens or manages connections.

From this directory, with Go 1.27:

```sh
go build ./...
go vet ./...
go test -race ./...
```

There is no standalone process to run. Import
`github.com/r59q/forge-tools/forge-changes/library/changes`:

```go
client, err := changes.New(changes.Source{ID: string(connection.ID), Session: connection.Session})
if err != nil {
    return err
}
items, err := client.List(ctx, changes.ListOptions{
    Source: string(connection.ID), Repository: "owner/project", State: "open",
})
```

`Get`, `Create`, `Update` and `SetDraft` manage lifecycle. `Comments` and
`AddComment` handle general discussion; GitLab quick actions retain native
semantics. `Commits`, `Files` and `Diff` read content. `Readiness` reports
advisory conditions. `Merge` accepts a supported method and optional full
lowercase expected head ID. `Sources` and `Capabilities` describe adapters.

Updates use pointers: nil preserves a field; an empty description clears it.
`State` updates accept open or closed, never merged. Source-branch changes
return `ErrUnsupported` before any mutation. Methods return forge errors
wrapping `forgehttp` sentinels; use `errors.Is` for `ErrInvalid`,
`ErrUnknownSource`, `ErrUnsupported`, `ErrHeadChanged`, `ErrMergeRejected` and
`ErrTooLarge`. Readiness separately represents unavailable conditions.

See [forge mappings and limits](../README.md) and exported Go documentation
for reference, revision and diff semantics. Tests use local fake APIs and
need no credentials or live forge.

## License

[MIT](../../LICENSE)
