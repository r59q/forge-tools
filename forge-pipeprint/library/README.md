# forge-pipeprint library

Go module: `github.com/r59q/forge-tools/forge-pipeprint/library`.
Its only dependency is `go.yaml.in/yaml/v3`. It reads and writes bytes; it
never contacts a forge.

## Packages

| Package     | Use                                                                 |
| ----------- | ------------------------------------------------------------------- |
| `pipeprint` | The model, Pipeprint YAML and JSON, validation and needs levels.    |
| `github`    | GitHub and Forgejo Actions workflows to and from the model.         |
| `gitlab`    | `.gitlab-ci.yml` to and from the model.                             |
| `compile`   | Formats, detection, `Read`, `Write`, `Convert` and `Check`.         |
| `graph`     | Nodes and edges for visualization.                                  |
| `render`    | Pipelines drawn as text.                                            |

## Use

```go
out, warnings, err := compile.Convert("", compile.GitLab, workflow)
for _, w := range warnings {
	log.Println(w) // jobs.test.steps[1]: action actions/cache@v4 cannot run on GitLab; dropped
}

p, _, err := compile.Read(compile.GitHub, workflow)
g := graph.Build(p)                      // g.Nodes, g.Edges
fmt.Print(render.Text(p, render.Options{Steps: true}))
```

Invalid input returns an error wrapping `pipeprint.ErrInvalid`.
`pipeprint.ProblemsOf(err)` lists the problems with their paths.

See the [tool README](../README.md) for the format and
[LIMITATIONS.md](../LIMITATIONS.md) for what is left out.

## Build and test

Requires Go 1.27 or newer. From this directory:

```sh
go vet ./...
go test ./...
go test ./... -update   # rewrite testdata golden files after a reviewed change
```

## License

[MIT](../../LICENSE)
