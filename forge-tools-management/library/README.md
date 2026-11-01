# forge-tools-management library

Read-only inspection of optional forge-tools capabilities. The initial API
lists repositories through the existing forge-repositories library contract.

```go
// reader may be an existing *repositories.Client, a remote adapter, or nil.
client := management.New(reader)
listing, err := client.ListRepositories(ctx)
```

`Configured: false` means no repository capability. `Configured: true` with
no entries means a successful empty listing. Errors discard all results and
hide upstream details. Entries retain full paths, source IDs, forge kinds,
hosts and visibility. No credential or repository URL is returned.

From this directory, with Go 1.27 or newer:

```sh
go build ./...
go vet ./...
go test -race ./...
```

Keep the checkout layout for the relative module replacements. No running
services or live forge credentials are needed for tests. This is a library;
run the [service](../service/) for REST, gRPC and the web interface.
