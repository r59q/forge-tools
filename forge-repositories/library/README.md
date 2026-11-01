# forge-repositories library

Go module: `github.com/r59q/forge-tools/forge-repositories/library`.
It uses the local `forge-core/forgeapi` module through a relative `replace` and
has no other dependencies. Sessions come from forge-connector; the library
never handles tokens or connection details.

Package `repositories` gives one model for repositories, branches and tags on
GitHub, GitLab and Forgejo. There are no per-forge entry points: a `Client`
combines any number of sources and picks the forge API from each session's
kind. A client without sources lists and finds nothing.

## Sources

A `Source` is an ID and an open `forgeauth` session, which supplies the base
URL, kind and authenticated HTTP client. The library only adds API paths.
Get sessions from the
[forge-connector `connector` package](../../forge-connector/library/README.md#connector):

```go
conns, err := connectorClient.Connections(ctx)
sources := make([]repositories.Source, len(conns))
for i, conn := range conns {
	sources[i] = repositories.Source{ID: string(conn.ID), Session: conn.Session}
}
c, err := repositories.New(sources...)
```

Every `Repository` carries a `SourceInfo`: the source ID, the forge kind and
the forge's web host. The host and `WebURL` come from the forge's response
(`html_url` or `web_url`), so they name the forge even through a proxy.

## Reads

- `List` returns the repositories each source's account owns or is a member
  of, merged in source order. `ListOptions.Owner` keeps one user, organization
  or group across every source, ignoring case; a GitLab group also matches its
  subgroups. So listing owner `mrbean` returns mrbean's GitHub and GitLab
  repositories together. `ListOptions.Source` reads one source only.
- `Find` returns the repository at a path on every source that has it.
- `Get` returns one repository by `Ref{Source, Path}`. Paths are `owner/name`
  or nested GitLab paths such as `group/sub/project`; pass them unencoded.
  Without a source, the path must exist on exactly one source, or the error
  wraps `ErrAmbiguous`.
- `Branches` lists branches with each commit SHA and marks the default branch.
  `Tags` lists tags with the SHA of the tagged commit.

Sources are queried in parallel. Any failure other than "not found" fails the
whole call, with the source ID in the error, rather than returning partial
results. Each list follows `Link` pagination up to 1000 pages, only within the
session's scheme, host and base path. Requests, paging and path checks use
[`forgerest`](../../forge-core/forgeapi/README.md#forgerest).

## Updates

`Update` sets the description, the default branch or both, and returns the
updated repository. Nil fields are left unchanged. The package never creates,
forks or deletes repositories, branches or tags.

## Errors

Forge failures are `*forgehttp.Error` values, so `errors.Is` works with
`forgehttp.ErrNotFound`, `ErrUnauthorized`, `ErrForbidden`, `ErrRateLimited`
and the other sentinels on every forge. A path that cannot exist on a forge,
such as a nested path on GitHub, is also `forgehttp.ErrNotFound`. Malformed
arguments wrap `ErrInvalid`, and unknown source IDs wrap `ErrUnknownSource`.
Errors and logs never contain tokens; URLs in errors drop the query string.

## Forge mapping

| Forge   | List                             | Repository                       | Page size  |
| ------- | -------------------------------- | -------------------------------- | ---------- |
| GitHub  | `/user/repos`                    | `/repos/{owner}/{name}`          | `per_page` |
| GitLab  | `/projects?membership=true`      | `/projects/{group%2Fsub%2Fname}` | `per_page` |
| Forgejo | `/user/repos`                    | `/repos/{owner}/{name}`          | `limit`    |

Branches and tags live below the repository path (`/branches` and `/tags`, or
`/repository/branches` and `/repository/tags` on GitLab). Updates use `PATCH`
on GitHub and Forgejo and `PUT` on GitLab.

## Develop

From this directory:

```sh
gofmt -l .
go vet ./...
go test ./...
```

Tests use local `httptest` forges; no credentials or network access are needed.
