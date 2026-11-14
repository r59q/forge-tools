# Search library

The forge-search model and unindexed literal search over documents the caller
supplies. No file, network or storage access, no cgo. See the
[tool overview](../README.md).

```go
documents := []search.Document{{
	Ref:     search.Ref{Kind: search.KindCode, Source: "checkout-1", ID: "main.go"},
	Content: source,
}}

results, err := search.Unindexed(ctx, search.Query{Text: "main"}, documents, search.Limits{})
for _, match := range results.Matches {
	fmt.Println(match.Document.ID, match.Range, match.Preview)
}
```

Import `github.com/r59q/forge-tools/forge-search/library/search`.
From this directory: `CGO_ENABLED=0 go build ./...`, `go vet ./...`,
`go test -race ./...`, `go mod tidy -diff`.

## Matching

These rules hold for every search mode.

- A query is literal and case-sensitive: valid UTF-8, 1 to 256 bytes, no NUL,
  `\r` or `\n`. Anything else returns `ErrInvalid`.
- A document is identified by `Kind`, `Source` and `ID`; `Kind` and `ID` are
  required. Each is valid UTF-8 without NUL, at most 4096 bytes. `Version` is
  returned as given. Two documents with the same identity return `ErrInvalid`.
- Lines end at `\n`. A line gives at most one match: its leftmost occurrence.
- Matches are ordered by `Kind`, `Source` and `ID`, compared as bytes, then
  by line. `Query.Limit` keeps the first matches in that order.
- Ranges use one-based lines and UTF-16 columns, with an exclusive end
  column, as in forge-explorer and forge-symbols.
- A preview is the matching line, at most 240 UTF-16 units, without a
  trailing `\r`. When the match would not fit, the preview starts up to 40
  units before it. Cut text is marked with `…`, and `PreviewColumn` is the
  column where the preview text starts.
- Documents with a NUL byte or invalid UTF-8 are binary: they are skipped and
  counted in `State.Skipped`.

## Limits

| Limit           | Default | Exceeded           |
| --------------- | ------- | ------------------ |
| `DocumentBytes` | 1 MiB   | `ErrTooLarge`      |
| `Documents`     | 10000   | `ErrTooLarge`      |
| `TotalBytes`    | 64 MiB  | `Truncated` is set |
| `Results`       | 1000    | `Truncated` is set |

Zero fields use the default. `Query.Limit` may lower `Results`, not raise it.
Documents are scanned in match order; once `TotalBytes` would be exceeded, the
rest are not scanned. Skipped documents count towards it. `Truncated` is set
only when a match or a document was left out.

A context that ends returns the matches found so far with `State.Canceled`.
Errors never contain document content.
