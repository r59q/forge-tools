# forge-issues

A federated issue tracker for forges.

forge-issues gives you one API for issues on GitHub, GitLab, Forgejo and other
forges. It maps each forge's issues to a shared model, so your code works the
same no matter which forge an issue lives on.

> Status: planning. There is no code yet.

## Use it to

- Read and manage issues on any supported forge through one API.
- Track issues spread across several forges in one place.
- Build forge-agnostic tools on top of issues.

## Concepts

- **Issue**: the shared model for an issue, with common fields such as title,
  body, state, labels, assignees and comments.
- **Forge adapter**: connects to one forge and translates between its API and
  the shared model.

## Projects

| Project    | Description                                                         |
| ---------- | ------------------------------------------------------------------- |
| `library/` | Go library with the issue model, business rules and forge adapters. |
| `service/` | Microservice exposing the library over REST, gRPC and WebSockets.   |

Both projects are planned.

## License

[MIT](../LICENSE)
