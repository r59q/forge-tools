# forge-actions library

Go module: `github.com/r59q/forge-tools/forge-actions/library`.
Package `actions` reads automation through sessions supplied by forge-connector.
Its only dependency is `forge-core/forgeapi`.

```go
client, err := actions.New(actions.Source{ID: string(connection.ID), Session: connection.Session})
// Check err before using client.
page, err := client.ListPipelines(ctx, actions.ListOptions{
    RepositoryRef: actions.RepositoryRef{Source: string(connection.ID), Repository: "owner/repo"},
    Branch: "main",
})
```

## Operations

`Sources`, `ListPipelines`, `GetPipeline`, `ListJobs`, `GetJob`, `ReadJobLog`,
`ListArtifacts`, `DownloadArtifact` and `ListDefinitions`.

All repository operations require a source ID and full repository path,
including nested GitLab groups. Resource IDs are decimal strings from the
forge, scoped by source and repository. GitLab artifact IDs identify jobs.

Pipeline filters: commit, branch, definition, lifecycle phase and result.
Pages contain up to 50 native entries. Phase/result filtering is local;
follow `NextPage` even after an empty page. Other filters are native.
Listings are live and may move between pages. No state is cached.

| Feature            | GitHub              | GitLab                  | Forgejo                     |
| ------------------ | ------------------- | ----------------------- | --------------------------- |
| Pipeline           | Workflow run        | Pipeline                | Action run                  |
| Jobs               | Latest run jobs     | Latest non-retried jobs | Run jobs                    |
| Steps              | When returned       | Unavailable             | When returned by job detail |
| Stages             | Unavailable         | Native stage            | Unavailable                 |
| Definition catalog | Workflows API       | Unsupported             | Unsupported                 |
| Definition filter  | Numeric workflow ID | Unsupported             | Workflow filename           |
| Job log            | Job logs endpoint   | Job trace               | Job logs endpoint           |
| Artifacts          | Run archives        | Latest job archives     | Run archives                |

Forgejo uses its own response fields and array shapes. Older instances may
lack endpoints or steps. Missing endpoints remain native errors; an omitted
steps field is `StepsKnown: false`. Native job dependencies, GitLab trigger
jobs and downstream pipelines are not included. GitHub's pipeline finish
time is unavailable; its update time is not treated as a finish time.

`Status.Phase`: unknown, queued, running, waiting, finished.
`Status.Result`: empty until known, then success, failure, cancelled,
skipped, neutral or unknown. Native statuses and conclusions are preserved.
GitLab manual jobs and Forgejo blocked jobs are waiting. Allowed job failure
is separate from its result. Missing timestamps are zero values.

## Bounds and errors

- Job, definition and artifact listings: 1000 entries; excess is `ErrTooLarge`.
- Job logs: first 1 MiB; `Truncated` marks omitted bytes. Invalid UTF-8 is replaced.
- Artifact downloads: 16 MiB; excess fails, with no partial archive returned.
- GitHub filtered history may hit its native 1000-result limit; an incomplete
  page beyond that limit fails instead of claiming a complete history.
- Archives are returned as bytes and never extracted.
- Expired/deleted logs and artifacts retain native not-found errors.
- GitLab lists available job archives; individual report files are excluded.

`ErrInvalid`, `ErrUnknownSource`, `ErrUnsupported` and `ErrTooLarge` describe
library failures. Native failures wrap `forgehttp` errors. Credentials and
access remain the connector's responsibility.

Downloads that redirect to object storage need the connector proxy's
`DownloadHosts` / `FORGE_PROXY_DOWNLOAD_HOSTS` allowlist. Without it a stripped
redirect returns `ErrUnsupported`. Direct sessions only follow downloads
within their API base. Signed external URLs are never exposed by this tool.

API references: [GitHub Actions](https://docs.github.com/en/rest/actions),
[GitLab pipelines](https://docs.gitlab.com/api/pipelines/),
[GitLab jobs](https://docs.gitlab.com/api/jobs/),
[Forgejo schema](https://code.forgejo.org/swagger.v1.json).

## Develop

From this directory, with Go 1.27:

```sh
go test ./...
go vet ./...
```

Tests use local fake forges and require no credentials. The library has no
standalone process.
