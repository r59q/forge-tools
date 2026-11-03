# Forge-Tools

Building blocks for forges: code sharing and collaboration platforms such as
GitHub, GitLab and Forgejo.

Use them to integrate with existing forges or to build new ones. Each tool is
useful on its own.

## Tools

| Tool                                              | Description                                                            |
| ------------------------------------------------- | ---------------------------------------------------------------------- |
| [forge-checkouts](forge-checkouts/)               | Persistent working copies, status and diffs.                           |
| [forge-actions](forge-actions/)                   | Pipelines, jobs, logs and artifacts across forges.                     |
| [forge-code](forge-code/)                         | Git content across forges: trees, files, diffs and commits.            |
| [forge-explorer](forge-explorer/)                 | Live worktree browsing, bounded search and embeddable code exploration. |
| [forge-review](forge-review/)                     | Fixed review snapshots, assignments and native or standalone feedback. |
| [forge-changes](forge-changes/)                   | Proposed code changes, discussion and merging across forges.           |
| [forge-issues](forge-issues/)                     | Issues and comments across forges.                                     |
| [forge-connector](forge-connector/)               | Forge connections and authentication for every tool.                   |
| [forge-repositories](forge-repositories/)         | Repositories, branches and tags across forges.                         |
| [forge-tools-management](forge-tools-management/) | Web interface to connect forges and inspect forge-tools.               |

## Structure

```text
<tool>/
  library/    core features and business rules
  service/    microservice exposing the library over REST and gRPC
  frontend/   reusable HTML/TypeScript interface, when provided
forge-core/   shared libraries
```

Each project builds, tests and deploys on its own.
Tools may have named services such as `broker/` and `proxy/` sharing one library.

## Examples

[examples/](examples/) runs the published images with Docker Compose and shows
how to use them with `curl` and Go.

## Contributing

Read [AGENTS.md](AGENTS.md). Its rules apply to agents and people alike.

## License

[MIT](LICENSE)
