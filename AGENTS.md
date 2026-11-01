# AGENTS.md

Rules for agents and contributors working in this repository.

## About

Forge-Tools is a monorepo of building blocks for forges: code sharing and
collaboration platforms such as GitHub, GitLab and Forgejo. Use the tools to
integrate with forges or to build new ones.

The repository is open source under the MIT license. Everything committed is
public.

## Layout

- `<tool>/`: one directory per forge-tool, e.g. `forge-issues/`.
- `<tool>/<project>/`: one folder per deployable unit, e.g.
  `forge-issues/library/` and `forge-issues/service/`.
- `forge-core/`: shared libraries used by several tools.
- Root config files, such as `.editorconfig`, are shared by all projects.

## Tools

- Each tool must be useful on its own, together with forge-connector.
- Tools must not depend on each other, except on forge-connector, or when
  combining them is the point of the tool.
- forge-connector is how tools reach forges. It owns connections, credentials
  and authentication, and supplies ready sessions. Tools ask it for
  connections and make forge API calls without handling tokens.
- forge-connector knows no tool domain, such as repositories or issues, and
  exposes no domain APIs. Tools own their domain and know nothing about how
  connections are made.
- Code needed by several tools goes in `forge-core/`. `forge-core/` must not
  depend on any tool.

## Projects

- Each project builds, tests and deploys on its own, from its own folder.
- Each project has its own build files (`go.mod`, `package.json`) and a short
  README on what it is and how to build, test and run it.
- Projects may share config, assets and `forge-core/` libraries, but must run
  their own checks without building other projects.

## Library and service

- The library (`<tool>/library/`) holds the core features and business rules.
  Make it public if it is useful on its own.
- The service (`<tool>/service/`) is a microservice that exposes the library
  over APIs. Keep business logic in the library.
- A tool may have several named services, e.g. `<tool>/broker/` and
  `<tool>/proxy/`, that share one `<tool>/library/`. All library and service
  rules still apply to each named service.
- Services support both REST and gRPC with protobuf. Keep them in sync; prefer
  one protobuf definition as the source of truth.
- Services may use WebSockets for real-time updates and subscriptions.
- A service provides at least what the library does. It may provide more, but
  put as much as possible in the library.
- Each service has a `Dockerfile` in its folder that builds its image. Build
  from the repository root so `forge-core/` and the library are in context.

## Languages

- Libraries and services default to Go.
- Go module paths match the folder:
  `github.com/r59q/forge-tools/<tool>/<project>`.
- Frontend tools use HTML and TypeScript. Keep the core framework-agnostic and
  add framework adapters only when needed, e.g. for a diff viewer.

## Quality

- Commit only high quality code. Add or update tests with every code change.
- Go code is `gofmt`-formatted and passes `go vet ./...` and `go test ./...`,
  run from the project folder.
- Keep dependencies few and well maintained.
- Never commit secrets, tokens or private data.

## Documentation

- Document all public interfaces: exported Go identifiers, exported
  TypeScript APIs, protobuf services and messages, and REST endpoints.
- Go doc comments start with the name of the thing they describe.
- Keep all docs short, simple and easy to read.

## Workflows

Each tool has its own pipelines that build, test and deploy its projects:
one workflow per project, `.github/workflows/<tool>-<project>.yml`, that runs
only when that project or its dependencies change. Put shared steps in
`reusable-*.yml` workflows. Services deploy their image on pushes to `master`.

Workflows must not run on unauthorized requests.

- Use `pull_request`, not `pull_request_target`, to run code from pull
  requests. Never expose secrets to pull requests from forks.
- Workflows triggered by issues or comments must check that the actor has
  write access before doing anything.
- Set `permissions:` to the minimum needed, defaulting to `contents: read`.
- Pass event data to scripts through `env:`, not `${{ }}` inside `run:`.
- Pin third-party actions to a full commit SHA.

## Agent output

Keep everything you write short, concise and easy to understand: code
comments, docs, commit messages, pull request descriptions and replies.
