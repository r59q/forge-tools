# forge-issues library

Go module: `github.com/r59q/forge-tools/forge-issues/library`.
It uses the local `forge-core/forgeapi` module through a relative `replace` and
has no other dependencies. Sessions come from forge-connector; the library
never handles tokens or connection details. It does not depend on
forge-repositories: a repository is just a path such as `owner/name` or
`group/sub/project`.

Package `issues` gives one model for issues and comments on GitHub, GitLab and
Forgejo. A `Client` combines any number of sources and picks the forge API from
each session's kind. Pull and merge requests are never returned or changed.

## Sources

A `Source` is an ID and an open `forgeauth` session. Get sessions from the
[forge-connector `connector` package](../../forge-connector/library/README.md#connector):

```go
conns, err := connectorClient.Connections(ctx)
sources := make([]issues.Source, len(conns))
for i, conn := range conns {
	sources[i] = issues.Source{ID: string(conn.ID), Session: conn.Session}
}
c, err := issues.New(sources...)
```

Every `Issue` carries a `SourceInfo`: the source ID, the forge kind and the
forge's web host, taken from the issue's web URL.

## Reads

- `List` with `ListOptions.Repository` lists that repository's issues on
  `ListOptions.Source`, or on every source that has it. Without a repository,
  each source lists its account's issues: `Assigned` (default) or `Created`.
- `State` is `open` (default), `closed` or `all`. `Labels` keeps issues that
  have every label, ignoring case.
- `Get` returns one issue by `Ref{Source, Repository, Number}`. Without a
  source, it must exist on exactly one source, or the error wraps
  `ErrAmbiguous`.
- `Comments` lists an issue's comments, oldest first.

Sources are queried in parallel and merged in source order. A source without
the repository or issue, including a 410 Gone as GitHub sends for deleted
issues, is skipped; any other failure fails the whole call, with the source ID
in the error. Requests, paging and path checks use
[`forgerest`](../../forge-core/forgeapi/README.md#forgerest).

## Writes

`Create`, `Update` and `AddComment` need `Source`. `Update` leaves nil fields
unchanged; an empty `Labels` or `Assignees` slice clears them. Assignees are
logins. Writes first read the issue, so a pull request is never changed, and
go to where the forge says the issue is, so a transferred issue is changed in
its new repository. The package never deletes issues or comments.

## Errors

Forge failures are `*forgehttp.Error` values, so `errors.Is` works with
`forgehttp.ErrNotFound`, `ErrForbidden`, `ErrRateLimited` and the other
sentinels on every forge. A pull request number, a path that cannot exist on
a forge, or a 410 Gone on a read is `forgehttp.ErrNotFound`. Malformed arguments wrap `ErrInvalid`
and unknown source IDs wrap `ErrUnknownSource`.

## Forge mapping

| Forge   | Repository issues                 | Account issues                                | Comments                  |
| ------- | --------------------------------- | --------------------------------------------- | ------------------------- |
| GitHub  | `/repos/{owner}/{name}/issues`    | `/issues?filter=assigned\|created`            | `.../issues/{n}/comments` |
| GitLab  | `/projects/{group%2Fname}/issues` | `/issues?scope=assigned_to_me\|created_by_me` | `.../issues/{iid}/notes`  |
| Forgejo | `/repos/{owner}/{name}/issues`    | `/repos/issues/search?assigned\|created=true` | `.../issues/{n}/comments` |

Forge notes:

- GitHub lists pull requests as issues; they are dropped. GitHub may drop
  labels and assignees silently when the account lacks push access.
- GitLab numbers are iids. State `opened` maps to `open`; updates use
  `state_event`. Assignee logins are looked up with `/users?username=`; an
  unknown login is `ErrInvalid`. GitLab Free keeps only the first assignee.
  System notes are left out of comments, and a comment's web URL is the issue
  URL plus `#note_{id}`. All GitLab issue types, such as incidents and tasks,
  are listed. The label filters `None` and `Any` are applied by the library
  only, as GitLab gives them a special meaning.
- GitLab runs quick actions, such as `/close`, in comment bodies and
  descriptions. A comment of only quick actions adds no note; `AddComment`
  then returns a comment with no ID.
- Forgejo labels are matched by name to repository and organization label
  IDs; unknown names are ignored. `Create` sends them with the issue in one
  call. `Update` checks assignee logins first (unknown is `ErrInvalid`),
  changes the other fields, then sets the labels; if only the labels fail,
  the error says the issue was updated. Like GitHub, Forgejo drops labels and
  assignees silently when the account cannot write issues.
- Forgejo repository lists are filtered by labels in the library only, and
  the account search matches any label, so the library keeps only issues
  that have all of them.
- Unknown labels may be created on GitHub and GitLab.

## Develop

From this directory:

```sh
gofmt -l .
go vet ./...
go test ./...
```

Tests use local `httptest` forges; no credentials or network access are needed.
