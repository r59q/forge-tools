# forge-changes

Manage GitHub and Forgejo pull requests and GitLab merge requests through
one `ChangeRequest` model. It owns proposals, discussion and merging;
reviews, assignments, approvals and inline threads belong to forge-review.
No forge-code or forge-review service is needed.

- [Library](library/): independently usable Go client over connector sessions.
- [Service](service/): authenticated REST and gRPC from one protobuf definition.

## Forge mappings

| Feature | GitHub | GitLab | Forgejo |
| --- | --- | --- | --- |
| Proposal | Pull request number | Merge request `iid` | Pull request number |
| List/get/create/update | Pulls REST API | Merge requests REST API | Pulls REST API |
| Draft creation | `draft` field | `Draft:` title | `Draft:` title |
| Draft/ready transition | GraphQL mutation | Title prefix | Title prefix |
| Change target branch | `base` | `target_branch` | `base` |
| Change source branch | Unsupported | Unsupported | Unsupported |
| Close/reopen | `state` | `state_event` | `state` |
| General comments | Issue comments | Notes, excluding system/diff/thread notes | Issue comments |
| Commit list | Native endpoint, maximum 250 | Native endpoint | Native endpoint |
| Files | Metadata and hunks, maximum 3,000 | Diff endpoint, instance limits | Metadata only |
| Unified diff | Diff media type | `raw_diffs` | `.diff?binary=true` |
| Merge methods | merge, squash, rebase | default, squash | merge, squash, rebase, rebase-merge, fast-forward-only |
| Atomic head guard | `sha` | `sha` | `head_commit_id` |
| Merge readiness | `mergeable`, `mergeable_state` | `detailed_merge_status` | `mergeable`; other requirements unknown |

Native API references: [GitHub pulls](https://docs.github.com/en/rest/pulls/pulls),
[GitHub mutations](https://docs.github.com/en/graphql/reference/mutations),
[GitLab merge requests](https://docs.gitlab.com/api/merge_requests/),
[GitLab notes](https://docs.gitlab.com/api/notes/), and
[Forgejo definitions](https://codeberg.org/forgejo/forgejo/src/branch/forgejo/modules/structs/pull.go).

## Conventions and limits

A reference is `(source connection ID, target repository path, native number)`.
Single-proposal reads and writes require all three. Listing requires a
repository; omitting source searches that path on all connections. This
initial API does not discover repositories or create cross-fork proposals;
existing cross-fork proposals can be read and managed.

States are `open`, `closed` and `merged`; filtering closed excludes merged.
Titles preserve native draft prefixes. Draft transitions use default GitLab
and Forgejo prefixes. Custom Forgejo prefixes may cause `ErrUnsupported`
after the title update; callers should inspect the proposal before retrying.
GitHub draft transitions need `/graphql` on the supplied session. Standard
GitHub.com connector sessions support this. GitHub Enterprise REST sessions
whose GraphQL route is outside the API base cannot perform this operation.

Revision snapshots contain full commit IDs for head, target and merge base
when exposed. GitLab target is the diff version's `start_sha`. Empty IDs mean
unavailable. File paths are repository-relative; `OldPath` is empty for added
files and `Path` for deleted files. Per-file patches are unified hunks; raw
diffs include file headers. These conventions can also be used by forge-review
without importing this library. No new shared implementation is needed yet.

Commits, files and diffs check revisions before and after reading, failing on
an observed change. This is not a transactional snapshot: unavailable IDs or
an intervening change and revert cannot be detected. GitHub's 250-commit cap
returns `ErrTooLarge`, even if there happen to be exactly 250 commits. File
and diff completeness is explicit; upstream limits may make it `unknown`.
An unavailable patch is not an empty change. Raw diffs over 16 MiB fail.

Merge readiness is advisory and can be stale. Missing review/check conditions
remain `unknown`. The forge enforces all its rules during the merge, without
forge-review. No force merge, auto-merge or branch deletion is requested.
GitLab `default` preserves project settings, including squash policy;
`squash` explicitly enables squashing. Other strategies are unsupported there.
Expected-head checks are passed to the native merge operation, never emulated
with a read alone. A failed request is not automatically retried; after a
transport failure, inspect the forge before retrying a mutation.

## License

[MIT](../LICENSE)
