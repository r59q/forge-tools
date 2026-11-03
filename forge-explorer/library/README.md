# Explorer library

Standalone live-worktree browsing and bounded search. No forge, Git binary,
checkout service, repository code execution, or transport dependency.

```go
w, err := explorer.Open("local", "My worktree", "/configured/root")
// Handle err; defer w.Close() after outstanding operations finish.
file, err := w.Read(ctx, "src/main.go", "")
matches, err := w.Search(ctx, explorer.Query{Text: "main", PathGlob: "src/*.go"})
```

Import `github.com/r59q/forge-tools/forge-explorer/library/explorer`.
From this directory: `go build ./...`, `go vet ./...`, `go test -race ./...`,
`go mod tidy -diff`. See [source and limit rules](../README.md).
