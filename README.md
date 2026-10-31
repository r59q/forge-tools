# Forge-Tools

Building blocks for forges: code sharing and collaboration platforms such as
GitHub, GitLab and Forgejo.

Use them to integrate with existing forges or to build new ones. Each tool is
useful on its own.

## Tools

| Tool                          | Description                         |
| ----------------------------- | ----------------------------------- |
| [forge-issues](forge-issues/) | Federated issue tracker for forges. |

## Structure

```text
<tool>/
  library/    core features and business rules
  service/    microservice exposing the library over REST and gRPC
forge-core/   shared libraries
```

Each project builds, tests and deploys on its own.

## Contributing

Read [AGENTS.md](AGENTS.md). Its rules apply to agents and people alike.

## License

[MIT](LICENSE)
