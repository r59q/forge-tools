# Forge-Tools

Building blocks for forges: code sharing and collaboration platforms such as
GitHub, GitLab and Forgejo.

Use them to integrate with existing forges or to build new ones. Each tool is
useful on its own.

## Tools

| Tool                                | Description                                  |
| ----------------------------------- | -------------------------------------------- |
| [forge-issues](forge-issues/)         | Federated issue tracker for forges.           |
| [forge-connector](forge-connector/)   | Stored forge connections and scoped tokens.   |
| [forge-repositories](forge-repositories/) | Repositories, branches and tags across forges. |
| [forge-tools-management](forge-tools-management/) | Read-only web inspection of forge-tools. |

## Structure

```text
<tool>/
  library/    core features and business rules
  service/    microservice exposing the library over REST and gRPC
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
