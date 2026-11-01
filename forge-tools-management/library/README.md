# forge-tools-management library

Sets up and inspects forge-tools capabilities. Each is optional.

```go
client := management.New(management.Config{
	Connector:    admin,  // *broker.Manager or a broker admin API adapter
	Repositories: reader, // *repositories.Client or a remote adapter
	Issues:       issues, // *issues.Client or a remote adapter
})
conn, err := client.AddConnection(ctx, management.NewConnection{
	Kind: forgeauth.GitLab, Host: "gitlab.example.com", Name: "work", Credential: token,
})
listing, err := client.ListRepositories(ctx)
open, err := client.ListIssues(ctx, management.IssueQuery{})
```

Connections:

- `AddConnection` checks the token with the forge and stores it in the
  connector. Host defaults to github.com, gitlab.com or codeberg.org. Forge
  endpoints are created and reused as needed.
- `ListConnections`, `TestConnection`, `RotateCredential` and
  `RemoveConnection` manage existing connections. Removing the last
  connection of a forge also removes the forge endpoint.
- `EnsureClient` makes the connector accept a configured token for a
  service, such as the proxy, granted every connection, including later
  ones. A new token replaces the service's old client.

Repositories: `Configured: false` means no repository capability.
`Configured: true` with no entries means a successful empty listing.
Entries retain full paths, source IDs, forge kinds, hosts and visibility.

Issues: `ListIssues` takes an `IssueQuery`. The zero value lists the open
issues assigned to each connected account. `Scope` picks assigned or
created issues, `State` picks open, closed or all, and `Repository` (with an
optional `Source`) lists one repository's issues instead. A bad scope or
state is `ErrInvalid`. `Configured` works as for repositories. Entries keep
source IDs, forge kinds, hosts, repository paths and numbers, but not issue
bodies or URLs.

Errors are fixed values, such as `ErrInvalid` and `ErrNotFound`. They never
include credentials, upstream details or partial results.

From this directory, with Go 1.27 or newer:

```sh
go build ./...
go vet ./...
go test -race ./...
```

Keep the checkout layout for the relative module replacements. Tests run a
real broker against fake forges; no services or live credentials are needed.
This is a library; run the [service](../service/) for REST, gRPC and the web
interface.
