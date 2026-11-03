---
name: fix-lint
description: Run and fix golangci-lint, jscpd, buf and go mod tidy findings the way CI does. Use when lint, duplication or protobuf checks fail, or before committing Go or protobuf changes.
---

# Fix lint

Make the lint and duplication checks pass by fixing the code, not the rules.

## Run the checks

Use the versions CI uses.

```sh
# golangci-lint, from each changed Go project folder
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...

# go.mod, from each changed Go project folder
go mod tidy -diff

# jscpd, from the repository root
npx --yes jscpd@5.4.1 .

# buf 1.74.0, from the repository root, for each changed proto/ folder
buf lint <project>/proto
buf format --diff --exit-code <project>/proto
buf breaking <project>/proto --against '.git#branch=master,subdir=<project>/proto'

# generated code, from the project folder
buf generate
```

`.golangci.yml`, `.jscpd.json` and `buf.yaml` at the root configure them.

## Fix findings

| Finding | Fix |
|---|---|
| `depguard` | Fix the import. Change the rule only when the boundary in AGENTS.md changed too. |
| `file-length-limit` | Split the file into smaller, coherent packages, not just more files. |
| `funlen`, `gocognit`, `nestif` | Extract small, well-named helpers. Return early to cut nesting. |
| `errcheck` | Handle or return the error. In `defer`, check `Close` errors on writes. |
| `errorlint` | Use `errors.Is`, `errors.As` and `%w`. |
| `noctx`, `contextcheck` | Pass the caller's `context.Context` through. |
| `gochecknoglobals` | Move state into a struct, or make it a constant or a function. |
| `ireturn` | Return the concrete type. |
| `goconst` | Name the repeated value as a constant. |
| `gosec` | Fix the cause, e.g. tighter file modes or validated paths. |

Use `//nolint:<linter> // <reason>` only when the code is right and the linter
is wrong. Name the linter and give the reason.

## Fix go.mod and protobuf

- Untidy `go.mod`: run `go mod tidy` and commit the result.
- `buf format`: run `buf format -w <project>/proto`.
- `buf lint`: fix the proto file. Proto files live in
  `proto/<package path>/`, e.g. `proto/forge/issues/v1/`. Each RPC `Foo`
  takes `FooRequest` and returns `FooResponse`. To return a bare message over
  REST, wrap it and set `response_body` in the HTTP option.
- `buf breaking`: never renumber, rename or delete published fields, messages,
  RPCs or services. Add new ones and deprecate the old instead.
- Stale generated code: run `buf generate` in the project folder and commit it.

## Fix clones

Apply the duplication rule in AGENTS.md:

- Domain-free, in three or more tools, and needing the same fix everywhere:
  move it to `forge-core/`.
- Duplicated within one project: extract a helper.
- Looks alike but changes for different reasons: keep it, and mark it.

```go
// jscpd:ignore-start - each library owns its client.
...
// jscpd:ignore-end
```

Keep the marked block small and always give a reason. Add `.jscpd.json`
ignores only for whole groups of files, such as generated code, tests and
fakes.

## Finish

Re-run both checks, then `go vet ./...` and `go test ./...` in each changed
project, until all pass.
