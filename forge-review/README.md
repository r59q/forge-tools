# forge-review

Fixed review snapshots, assignments and feedback for supplied diffs, commit
comparisons and forge change requests. Includes an independent [Go library](library/)
and a [REST/gRPC service](service/) with durable standalone storage.

No automatic analysis, merging, comment relocation or synchronization of local
reviews into forge reviews. Approvals record assessments; merge policy belongs
to the forge or application managing the proposal.

## Native adapter capabilities

All native writes use the connector session's identity and permissions. Native
feedback is immediately published; standalone feedback stays local. This table
describes the implemented adapters, not every feature in the forge's UI.

| Operation | GitHub | GitLab | Forgejo |
| --- | --- | --- | --- |
| Request reviewers | Usernames | Numeric user IDs; replaces list | Usernames |
| General feedback | Review body | Discussion | Review body |
| Approval | Review at commit | Approval guarded by `sha` | Review at commit |
| Request changes | Review | Unsupported | Review |
| Inline comments | Old/new lines | Old/new lines | Old/new lines |
| Same-side ranges | Yes | Unsupported | Unsupported |
| File comments | Yes | Unsupported | Unsupported |
| Replies | Root review comment | Diff discussion | Unsupported |
| Resolve/reopen | Unsupported in this REST adapter | Resolvable diff discussion | Unsupported |
| Read reviews/status | Native reviews | Approval state and discussions | Native reviews |

GitLab approval and discussion APIs are separate: an approval with a body is
unsupported; send discussion feedback separately. Quick actions in GitLab
comment bodies may be interpreted by the forge. GitLab edition, version and
project policy can restrict approvals. Forgejo range/reply/resolution support
varies by version; this adapter uses the common single-line review API and
returns explicit unsupported errors for those operations.

GitHub and Forgejo submissions include `commit_id`; GitLab inline positions
include `base_sha`, `head_sha` and `start_sha`. Changed heads fail preflight.
Replies/resolution preserve the original snapshot. General native discussions
without revision fields cannot be validated for reply/resolution here.
Native list results preserve provider JSON and include other revisions; callers
must examine the native revision fields when presenting them alongside a snapshot.

API references: [GitHub reviews](https://docs.github.com/en/rest/pulls/reviews),
[GitHub review comments](https://docs.github.com/en/rest/pulls/comments),
[GitLab discussions](https://docs.gitlab.com/api/discussions/),
[GitLab approvals](https://docs.gitlab.com/api/merge_request_approvals/), and
[Forgejo API schema](https://codeberg.org/forgejo/forgejo/src/branch/forgejo/templates/swagger/v1_json.tmpl).

## License

[MIT](../LICENSE)
