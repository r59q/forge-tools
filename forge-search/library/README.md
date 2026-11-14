# Search library

The forge-search model and literal search in two modes: unindexed, over
documents the caller supplies, and indexed, over named indexes kept behind a
storage interface, in memory or in an embedded SQLite database. No network
access, no cgo. See the [tool overview](../README.md).

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

An adapter for another database plugs in the same way: implement the nine
methods with one transaction each, keep the terms of every entry so that the
entries holding all terms of a selection can be found, and pass the suite.
The search rules stay in the engine.

### SQLite store

Package `sqlite` keeps every index in one SQLite database, through the
pure-Go `modernc.org/sqlite` driver, so it builds with `CGO_ENABLED=0`:

```go
store, err := sqlite.Open(ctx, dataDir) // an existing directory
defer store.Close()

engine := search.NewEngine(store, search.IndexLimits{})
```

Import `github.com/r59q/forge-tools/forge-search/library/sqlite`.

- **Files.** `search.db` holds all indexes. While the store is open, SQLite
  keeps `search.db-wal` and `search.db-shm` beside it. Index names are rows,
  never paths. Use one process per directory.
- **Private.** The files hold the content and terms of every document at
  rest. They are created with mode 0600, and `Open` resets the mode of the
  database file; the directory and the disk are the caller's to protect.
- **Schema.** The version is `PRAGMA user_version`. `Open` migrates an older
  database; a newer one returns `ErrStorage`.
- **Removed content.** With `secure_delete`, the content of a replaced or
  removed document, or of a deleted index, is overwritten in the database
  file when the change commits. Until a checkpoint, the write-ahead log may
  still hold it, and the full-text index keeps its terms behind delete
  markers; the store never returns either. `Checkpoint` merges the full-text
  index and empties the log, after which neither file holds them. `Close`
  empties the log but does not merge. The file does not shrink: freed pages
  are zeroed and reused, and there is no `VACUUM`. Copies outside the files,
  such as backups and file system snapshots, are out of reach.
- **Checkpoint cost.** `Checkpoint` rewrites the full-text index of all
  indexes: 0.7 s for the benchmark corpus. Updates wait for it; searches do
  not. Call it after removing content that must leave the disk, or
  periodically.
- **Concurrency.** Updates run one at a time on one connection, each in one
  transaction. Searches use a pool of `max(4, GOMAXPROCS)` read-only
  connections and read one snapshot each, so they wait neither for updates
  nor for each other.
- **Durability.** The database uses a write-ahead log with
  `synchronous=NORMAL`: a committed change survives the process ending. A
  power loss may lose the latest commits, but does not corrupt the file.
- **Errors.** Besides the search errors and context errors, the store
  returns `ErrStorage`, `ErrDiskFull` and `ErrReadOnly`, without SQL, paths
  or content. A change that does not fit on the disk leaves the index as it
  was.
- **Memory map.** Readers map up to 1 GiB of the file. An I/O error on a
  mapped page ends the process instead of returning an error.

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

### SQLite

`CGO_ENABLED=0 go test ./sqlite -run '^$' -bench . -cpu 6` on the same host
and corpus, with the database in a temporary directory on an SSD. Unindexed
times are as above. Indexed times are right after filling, and after a
`Checkpoint` has merged the full-text index.

| Query                                | Indexed | After `Checkpoint` | Speedup |
| ------------------------------------ | ------- | ------------------ | ------- |
| Identifier, in 3 documents           | 990 µs  | 630 µs             | 2.5–4x  |
| Line of common words, in 1 document  | 12 ms   | 5.7 ms             | 1.4–3x  |
| Three-letter word, in 644 documents  | 11 ms   | 11 ms              | 1.4x    |
| Two bytes, in 3 documents            | 20 ms   | 20 ms              | 0.13x   |

The target of 10x is not met. A search pays for SQLite's full-text query
and for reading every candidate's content from the database, which
`MemoryStore` hands over in memory. A two-byte query reads all 32 MiB.

Filling the index takes 12 s. The database file is then 80 MiB, with up to
65 MiB more in the write-ahead log, and 85 MiB after `Checkpoint`.

`BenchmarkSearchDuringUpdates` searches for a line on 256 documents of
16 KiB. Compared with one shared connection, as in the forge-connector
broker:

| Search                       | Reader pool | One connection |
| ---------------------------- | ----------- | -------------- |
| Alone                        | 2.4 ms      | 2.4 ms         |
| 6 at a time, wall per search | 0.55 ms     | 2.4 ms         |
| During updates, 115 per s    | 5.3 ms      | 9.0 ms         |
