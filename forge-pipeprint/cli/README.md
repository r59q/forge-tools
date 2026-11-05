# pipeprint CLI

`pipeprint` compiles pipeline files between forges through the
[Pipeprint format](../README.md), checks them and draws them as text. It
uses the [forge-pipeprint library](../library/) and works offline.

## Install

Download a binary for Linux, macOS or Windows from the
[releases](https://github.com/r59q/forge-tools/releases) tagged
`forge-pipeprint/cli/v*`, and check it against `SHA256SUMS`.

Or build it from a checkout with Go 1.27 or newer. `go install` does not
work, because the module uses a relative replacement for `../library`.

```sh
go build -o ~/.local/bin/pipeprint ./cmd/pipeprint
```

## Use

```sh
pipeprint import .github/workflows/ci.yml -o ci.pipeprint.yml   # forge → Pipeprint
pipeprint export --to gitlab ci.pipeprint.yml -o .gitlab-ci.yml  # Pipeprint → forge
pipeprint convert --to forgejo .gitlab-ci.yml                     # forge → forge
pipeprint check .gitlab-ci.yml
pipeprint graph --steps .github/workflows/ci.yml
pipeprint graph --json .gitlab-ci.yml                             # nodes and edges
pipeprint formats
```

Files default to standard input and output. Without `--from`, the format is
detected. Dropped settings are printed as warnings on standard error, such
as:

```text
pipeprint: warning: jobs.test.steps[1]: action actions/cache@v4 cannot run on GitLab; dropped
```

`--strict` turns warnings into a failure and writes nothing. `graph` takes
`--ascii` for plain characters and `--width` to wrap wide levels.

Exit codes: 0 success, 1 failure or problems found, 2 usage error.

## Test

```sh
go vet ./...
go test ./...
```

## Release

Push a tag such as `forge-pipeprint/cli/v0.1.0`. The
[workflow](../../.github/workflows/forge-pipeprint-cli.yml) tests the CLI, runs
[release.sh](release.sh) and publishes the binaries as a GitHub release. To
build the same files locally: `VERSION=v0.1.0 ./release.sh`.

## License

[MIT](../../LICENSE)
