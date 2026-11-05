# forge-pipeprint

One pipeline language for every forge. **Pipeprint** describes a CI/CD
pipeline once; forge-pipeprint compiles it to and from GitHub Actions,
Forgejo Actions and GitLab CI/CD.

Take a GitHub workflow, import it as Pipeprint, edit it, and export it to
GitHub again or to another forge when you move. Settings a forge cannot
express are dropped for that forge, and every drop is reported as a
warning. Pipeprint also exposes jobs and needs as a graph, so other tools
can draw pipelines.

- [library](library/): Go model, compilers, graph and text drawing.
- [service](service/): REST and gRPC from one protobuf definition.
- [cli](cli/): the `pipeprint` command, with a text pipeline view.

forge-pipeprint reads only the files you give it. It never contacts a forge, so
it needs no forge-connector. To read workflow files from a repository, use
[forge-code](../forge-code/).

See [LIMITATIONS.md](LIMITATIONS.md) for what the first version leaves out.

## Format

Pipeprint is structured YAML (or the same structure as JSON).

```yaml
pipeprint: v1
name: CI
triggers:
  push:
    branches: [main]
  pull_request: {}
jobs:
  - id: build
    image: golang:1.27
    steps:
      - checkout: true
      - run: go build ./...
  - id: test
    needs: [build]
    image: golang:1.27
    timeout_minutes: 15
    steps:
      - checkout: true
      - run:
          - go vet ./...
          - go test ./...
      - run: echo done
        always: true
  - id: deploy
    needs: [test]
    if:
      github: github.ref == 'refs/heads/main'
      gitlab: $CI_COMMIT_BRANCH == "main"
    environment: {name: production}
    steps:
      - run: ./deploy.sh
    native:
      github:
        permissions: {contents: read}
```

**Portable fields** mean the same on every forge:

| Level    | Fields                                                                                                                      |
| -------- | --------------------------------------------------------------------------------------------------------------------------- |
| pipeline | `name`, `triggers` (`push`, `pull_request`, `schedule`, `manual`), `env`, `stages`, `jobs`                                   |
| job      | `id`, `name`, `stage`, `needs`, `runs_on`, `image`, `services`, `env`, `if`, `timeout_minutes`, `continue_on_error`, `matrix`, `environment`, `steps` |
| step     | `name`, one of `checkout`, `uses` (+ `with`) or `run`, and `always`                                                          |

`run` is one command or a list of commands that share one shell session.

**Dialects** are language families: `github` (GitHub and Forgejo Actions)
and `gitlab`. Two things are written per dialect:

- `if` holds one expression per dialect, because expression languages differ.
- `native` holds settings Pipeprint does not model, such as `permissions`
  or `artifacts`. A forge writes its own dialect's native settings back
  verbatim and drops the others with a warning.

Importing never loses a setting: what Pipeprint cannot model is kept native.
A file imported and exported to the same forge keeps its meaning, though
comments, anchors and key order are not kept.

## Graphs

`needs` form a graph. A job's level is 1 without needs, and one more than
its highest need otherwise. The library's `graph` package, the service's
`Graph` RPC and `pipeprint graph --json` return nodes and edges;
`pipeprint graph` draws them:

```text
CI
on: push [main], pull_request

level 1
┌───────────────────┐
│ build             │
│ image golang:1.27 │
└───────────────────┘
  │
  ▼
level 2
┌───────────────────┐
│ test              │
│ ← build           │
│ image golang:1.27 │
└───────────────────┘
```

## License

[MIT](../LICENSE)
