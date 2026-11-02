# forge-workspaces

Prepare and manage repository working copies for people, scripts, workflows and
coding agents. Create from a local Git repository or an authenticated
[forge-connector](../forge-connector/) connection, using a branch, tag or commit.

- [Library](library/): lifecycle, checkout, status, diff and cleanup protection.
- [Service](service/): durable metadata and matching REST/gRPC APIs.

Workspaces are host-local Git worktrees. Execution systems run processes in them;
this tool does not execute commands for callers, commit, push or manage reviews.
Worktrees share repository objects and are not security sandboxes.

[MIT](../LICENSE)
