# Search library

The forge-search model and literal search in two modes: unindexed, over
documents the caller supplies, and indexed, over named indexes kept behind a
storage interface. No file or network access, no cgo. See the
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

An engine keeps documents in named indexes and searches them with the same
rules:

```go
engine := search.NewEngine(&search.MemoryStore{}, search.IndexLimits{})

err := engine.CreateIndex(ctx, "checkout-1")
err = engine.Put(ctx, "checkout-1", documents[0])

results, err := engine.Search(ctx, "checkout-1", search.Query{Text: "main"},
	search.Filter{Kind: search.KindCode, IDPrefix: "cmd/"})
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

## Indexes

| Method             | Effect                                            |
| ------------------ | ------------------------------------------------- |
| `CreateIndex`      | Adds an empty index, or returns `ErrExists`       |
| `DeleteIndex`      | Removes an index and everything stored for it     |
| `Indexes`, `Index` | Name, marker, document count and content bytes    |
| `SetMarker`        | Stores a caller value, such as a synced revision  |
| `Put`              | Adds a document, or replaces it by identity       |
| `Remove`           | Deletes a document, or returns `ErrNotFound`      |
| `Documents`        | Lists identities and versions, without content    |
| `Search`           | Finds a query in the documents a filter selects   |

- An index name is 1 to 64 bytes of `a-z`, `0-9`, `.`, `-` and `_`, starts
  with a letter or digit and holds no `..`. Any other name returns
  `ErrInvalid` before the store is reached, so a name is safe to use as a
  file name. A missing index returns `ErrNotFound`.
- A `Filter` selects by `Kind`, `Source`, `ID` and `IDPrefix`; empty fields
  select everything.
- A search sees each document entirely as it was before an update or entirely
  as it is after. Replaced and removed documents, and deleted indexes, leave
  no content or terms behind.
- The content of a binary document is not stored. The document is listed and
  searches count it in `State.Skipped`.

### Mode equivalence

`Engine.Search` returns what `Unindexed` returns for the same documents,
query and result limit: the same matches, order, previews and state. The
index holds the three-byte sequences of every line. A search narrows to the
documents that hold every sequence of the query, then checks them with the
matcher `Unindexed` uses. A query of one or two bytes cannot be narrowed, so
every selected document is checked: slower, never incomplete.

Indexed search has no `TotalBytes` budget, because the index limits bound
the work. The modes agree whenever the `Documents` and `TotalBytes` limits of
`Unindexed` admit all the documents.

### Index limits

| Limit           | Default | Exceeded           |
| --------------- | ------- | ------------------ |
| `DocumentBytes` | 1 MiB   | `ErrTooLarge`      |
| `Documents`     | 100000  | `ErrFull`          |
| `Bytes`         | 1 GiB   | `ErrFull`          |
| `Results`       | 1000    | `Truncated` is set |

Zero fields use the default. `Documents` and `Bytes` apply to each index. A
`Put` that returns an error leaves the index as it was. The number of indexes
is not limited; the caller decides who may create them.

## Storage

An engine keeps everything in a `Store`: nine methods that create and delete
indexes, store an entry (a document and its terms), remove one, and visit the
entries a filter and a set of terms select, in match order. A store needs no
knowledge of the search rules. Each method is atomic, so one transaction per
call is enough. `MemoryStore` is the reference implementation.

An adapter proves itself with the conformance suite and reruns the benchmark:

```go
func open(t testing.TB) search.Store { return newStore(t) }

func TestStore(t *testing.T) { storetest.Run(t, open) }

func BenchmarkSearch(b *testing.B) { storetest.Benchmark(b, open) }
```

`storetest.Run` checks the contract, that removed content is gone, that
updates are atomic under concurrent readers, and that indexed search returns
what `Unindexed` returns over random documents, updates and queries.

## Benchmarks

`CGO_ENABLED=0 go test ./search -run '^$' -bench .` on an AMD Ryzen 9 5950X
with 6 CPUs, Go 1.27, `MemoryStore`. The corpus is generated: 2048 text
documents of 16 KiB, 32 MiB in all, with words drawn from 2000 with a skewed
distribution. Both modes search documents that are already in memory.

| Query                                | Unindexed | Indexed | Speedup |
| ------------------------------------ | --------- | ------- | ------- |
| Identifier, in 3 documents           | 2.5 ms    | 4 µs    | 620x    |
| Line of common words, in 1 document  | 17 ms     | 130 µs  | 130x    |
| Three-letter word, in 644 documents  | 15 ms     | 4.6 ms  | 3.3x    |
| Two bytes, in 3 documents            | 2.4 ms    | 2.6 ms  | 0.9x    |

The target of 10x on a selective query is met for the identifier and the
line. It is not met for the rarest word of the corpus, which a third of the
documents hold: checking the candidates dominates. A two-byte query gains
nothing, as designed.

Filling the index takes 1.7 s. `MemoryStore` then holds about 165 MiB of
terms beside the 32 MiB of content: random words give each document far more
distinct sequences than code does, and the store favours simplicity over
size.
