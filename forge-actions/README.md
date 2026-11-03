# forge-actions

Pipelines, jobs, logs and artifacts across GitHub, GitLab and Forgejo.
Authenticated sessions come from [forge-connector](../forge-connector/).
Repositories are paths; no other domain tool is required.

- [library](library/): Go model and forge adapters.
- [service](service/): authenticated REST and gRPC from one protobuf definition.

A **pipeline** is one execution: a GitHub or Forgejo workflow run, or a
GitLab pipeline. Jobs belong to pipelines; steps and stages are optional.
Status separates lifecycle from outcome and preserves native values.
Pipeline results describe execution; merge readiness belongs to forge-changes.

Reads include paged pipeline history, pipeline/job details, bounded job logs,
archive metadata/downloads and GitHub's workflow definition catalog.
Source capabilities describe adapter support, subject to forge version and access.
Management provides an optional repository → pipelines → jobs → logs view.

The initial API reads current native state. Retry/trigger/cancel controls,
live subscriptions, dependency graphs and downstream pipeline traversal are
future extensions. No workflow configuration is evaluated locally.

## License

[MIT](../LICENSE)
