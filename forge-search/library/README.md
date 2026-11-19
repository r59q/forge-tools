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
| `Read`             | Visits the documents that may hold a text         |

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

## Checkout directories

Package `checkout`, imported from
`github.com/r59q/forge-tools/forge-search/library/checkout`, turns a
directory tree into code documents whose IDs are paths relative to the root,
with `/` separators. `Read` returns the documents, `Search` searches them
unindexed and `Sync` keeps an index up to date with them.

```go
root, err := os.OpenRoot(dir)

results, err := checkout.Search(ctx, root, search.Query{Text: "main"},
	checkout.Options{Source: "checkout-1"})

result, err := checkout.Sync(ctx, root, engine, "checkout-1", commit,
	checkout.Options{Source: "checkout-1"})
fmt.Println(result.ChangedPaths, result.RemovedPaths)
revision, err := checkout.Revision(ctx, engine, "checkout-1")
```

- Every read goes through the `*os.Root`, so nothing outside it is read,
  even when the tree changes during a read.
- Symlinks, special files such as FIFOs, entries named `.git` and paths that
  are not valid document IDs are skipped. Ignored and vendored files are read
  like any other: the tool sees the checkout as it is on disk. Binary files
  follow the matching rules.
- A file that changes while it is read returns `ErrChanged`; one that cannot
  be opened returns `ErrUnreadable`. Errors name root-relative paths, never
  host paths or content.
- `Options.Prefix` limits `Read` and `Search` to the paths that start with
  it, as `Filter.IDPrefix` does for an index, and visits only the
  directories that can hold them. `Sync` rejects it.

| Limit        | `Read`, `Search` | `Sync` | Exceeded                             |
| ------------ | ---------------- | ------ | ------------------------------------ |
| `FileBytes`  | 1 MiB            | 1 MiB  | File skipped, counted in `TooLarge`  |
| `Entries`    | 10000            | 100000 | `ErrTooLarge`                        |
| `Depth`      | 64               | 64     | `ErrTooLarge`                        |
| `TotalBytes` | 64 MiB           | 1 GiB  | `Truncated`; `ErrTooLarge` in `Sync` |

Files are read in match order, so `TotalBytes` leaves out the last ones.
`Sync` returns `ErrTooLarge` before it changes the index.

### Sync

A document's version is the SHA-256 of its content, in both modes, so a
synced index and an unindexed search of the same tree give the same results.
The first sync adds every file; later ones add, replace and remove only what
changed, removals first. Documents of other kinds or sources are left
alone. The caller creates the index.

A sync clears the index marker before its first change and records the
revision only when it completes, so a set marker names the revision the index
holds. A sync that fails or is canceled returns what it changed so far, and
the next sync corrects the index. Syncs of one index must not overlap.

## Issues

Package `issue`, imported from
`github.com/r59q/forge-tools/forge-search/library/issue`, turns forge-issues
issues into issue documents. `Search` searches a list unindexed,
`SearchRepository` lists a repository through a `Reader` (such as
`issues.Client`) and searches it unindexed, `Sync` keeps an index up to date
with a repository and `SearchIndex` searches what it synced.

```go
repository := issues.RepositoryRef{Source: "github", Repository: "owner/name"}

results, err := issue.SearchRepository(ctx, client, repository,
	search.Query{Text: "crash"}, issue.Options{Filter: issue.Filter{State: issues.Open}})
for _, match := range results.Matches {
	fmt.Println(match.Issue.Number, match.Issue.State, match.Field, match.Range, match.Preview)
}

result, err := issue.Sync(ctx, client, engine, "issues-1", repository, issue.Limits{})
results, err = issue.SearchIndex(ctx, engine, "issues-1", search.Query{Text: "crash"},
	issue.Options{Filter: issue.Filter{Repository: "owner/name"}})
synced, err := issue.LastSynced(ctx, engine, "issues-1", repository)
```

- An issue is one document: its title, with line breaks turned into spaces,
  on the first line, then its body. A match reports `Field` `title` or
  `body`, with the range and preview relative to that field. Comments,
  labels and other fields are not indexed.
- An issue is identified by source ID, repository and number. Its document
  ID is the repository, `#` and the number zero-padded to 19 digits, so
  matches come by source, repository and number, then title before body.
  `SearchRepository` and `Sync` name issues by the requested repository, so
  their results agree even when a forge reports a different spelling.
- The version holds the state, title, web URL and SHA-256 of the content, so
  a match in an index carries the same issue metadata as an unindexed one,
  and a sync rewrites an issue only when one of them changed.
- Filters select by source, repository and state. The state is checked after
  the search, among its first `Results` matches in all states. When those
  were not all the matches, `Truncated` is set, in both modes alike.
- Issue text is untrusted: titles and bodies are only matched and previewed,
  never interpreted. Errors name issue numbers, never issue text.

| Limit       | `Search`, `SearchRepository` | `Sync`  | Exceeded                                  |
| ----------- | ---------------------------- | ------- | ----------------------------------------- |
| `BodyBytes` | 256 KiB                      | 256 KiB | Issue left out, counted in `TooLarge`     |
| `Issues`    | 10000                        | 50000   | `ErrTooLarge`, before the index changes   |
| `Results`   | 1000                         | 1000    | `Truncated` is set                        |

Titles over 1024 bytes and web URLs over 2048 bytes are left out like long
bodies. A sync removes issues left out from the index.

### Issue sync

The first sync adds every open and closed issue; later ones add, replace and
remove only what changed, removals first. Other repositories, kinds and
sources are left alone, so one index can hold several repositories and a
checkout. Each repository's last completed sync is recorded in a document of
kind `issue-sync`. A sync removes that record before it lists issues and
writes it only when it completes, so a reader error, a store error or a
cancellation leaves the repository marked as not synced, and `LastSynced`
returns zero until a later sync converges. Syncs of one repository into one
index must not overlap.

## Symbols

Package `symbol`, imported from
`github.com/r59q/forge-tools/forge-search/library/symbol`, finds definitions
in code documents by name: functions, methods, types and the like, as the
[forge-symbols library](../../forge-symbols/library/README.md) reports them.
`Search` analyzes the documents a call carries and stores nothing, `Update`
stores the symbols of changed documents in an index and drops those of
removed ones, and `SearchIndex` searches what it stored.

```go
analyzer := symbols.New(symbols.Limits{})
defer analyzer.Close(ctx)

results, err := symbol.Search(ctx, analyzer, documents,
	symbol.Query{Text: "Handler", Filter: symbol.Filter{Kind: "function"}}, symbol.Limits{})
for _, found := range results.Symbols {
	fmt.Println(found.Document.ID, found.Name, found.Kind, found.NameRange, found.Preview)
}

result, err := symbol.Update(ctx, analyzer, engine, "checkout-1",
	symbol.Change{Source: "checkout-1", Documents: changed, Removed: removedPaths}, symbol.Limits{})
results, err = symbol.SearchIndex(ctx, engine, "checkout-1", symbol.Query{Text: "Handler"}, symbol.Limits{})
```

- A query matches symbol names that contain its text, literally and
  case-sensitively, or equal it with `Exact`. The text follows the matching
  rules. Filters select by source, path, path prefix, kind and language.
- Symbols come by source and path, compared as bytes, then in declaration
  order. `Query.Limit` keeps the first ones.
- Each symbol has its name, kind, language, the range of its name and of the
  whole definition, and its container, such as the type of a method. Ranges
  follow the matching rules.
- The preview is the declaration line: the line of the name, from the start
  of the definition when it starts on that line, to its end when it ends
  there, without leading indentation. It follows the preview rules for
  length and `…`, and `PreviewColumn` is the column where it starts. A
  preview is a hint for display, never code to run.
- The language comes from the path, as `symbols.DetectLanguage` finds it.
  `symbols.Languages` lists the supported languages; the forge-symbols README
  lists their extensions. Other documents, and binary ones, are skipped and
  counted in `Skipped.Unsupported`, without an error.
- Errors name no content and no host paths.

| Limit        | `Search` | `Update` | Exceeded                                   |
| ------------ | -------- | -------- | ------------------------------------------ |
| `FileBytes`  | 1 MiB    | 1 MiB    | Document skipped, counted in `TooLarge`    |
| `Symbols`    | 10000    | 10000    | Document skipped, counted in `TooLarge`    |
| `Files`      | 1000     | 10000    | `ErrTooLarge`, before anything is analyzed |
| `TotalBytes` | 16 MiB   | 256 MiB  | `ErrTooLarge`, before anything is analyzed |
| `Results`    | 1000     | 1000     | `Truncated` is set                         |

Zero fields use the default. `Files` and `TotalBytes` count only the
documents that would be analyzed. A document whose analysis times out or runs
out of memory is counted in `Skipped.Failed`, and one whose symbols take more
than `MaxRecordBytes` in `TooLarge`. Names, kinds and containers over 256
bytes, or with line breaks, are left out. A context that ends returns the
symbols found so far with `State.Canceled`.

### Symbol index

`Update` stores one document of kind `symbol` for each code document with
symbols, with the same source, ID and version; its content is one line per
symbol, the name, a tab and the rest of the record. `SearchIndex` reads the
documents whose content holds the query with `Engine.Read`, so it uses the
index terms and sees each document as one update left it. Literal searches
of the same index filter by kind to leave symbols out.

A changed document replaces its symbols; one with no symbols, or skipped,
removes them. `UpdateResult.SkippedPaths` names each skipped document and
why, so a caller can keep totals across updates. Removed paths drop theirs,
and paths never stored are ignored. Other kinds and sources are left alone, and `DeleteIndex` removes
everything. The caller creates the index and passes what changed, such as
the paths of a checkout sync. The modes agree when the update and the search
use the same analyzer and `FileBytes` and `Symbols` limits.

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

- **Files.** `search.db` holds all indexes. SQLite uses a private rollback
  journal during writes and removes it at commit. Index names are rows.
  Use one process per directory.
- **Private.** The files hold the content and terms of every document at
  rest. They are created with mode 0600, and `Open` resets the mode of the
  database file; the directory and the disk are the caller's to protect.
- **Schema.** The version is `PRAGMA user_version`. `Open` migrates an older
  database; a newer one returns `ErrStorage`.
- **Removal.** SQLite and FTS5 secure deletion erase replaced or removed
  text and postings in the committing transaction. Freed pages are reused.
  Existing version 1 indexes migrate on open. Migration merges old posting
  segments and retires the write-ahead log. Backups have their own lifetime.
- **Concurrency.** Readers share a pool of `max(4, GOMAXPROCS)` connections.
  Writers wait for active readers and have exclusive access through commit.
  Waiting calls respect context cancellation. A visitor runs inside a read
  transaction and must return before starting a write.
- **Maintenance.** `Checkpoint` merges the full-text index segments.
  This optional operation takes time proportional to index size.
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

Go 1.27 on an AMD Ryzen 9 5950X, four CPUs, using the same 32 MiB corpus.
The unindexed column searches documents already in memory. The indexed
column searches the embedded database immediately after filling it.

| Query | Unindexed | Indexed | Speedup |
| --- | ---: | ---: | ---: |
| Identifier in three documents | 2.88 ms | 0.94 ms | 3.1× |
| Phrase in one document | 18.9 ms | 15.5 ms | 1.2× |
| Three-letter word in 644 documents | 18.6 ms | 13.6 ms | 1.4× |
| Two bytes | 3.08 ms | 25.6 ms | 0.12× |

Run `CGO_ENABLED=0 go test ./sqlite -run '^$' -bench '^BenchmarkSearch$' -benchtime=600ms -cpu 4`.
For inputs already in memory, SQLite falls short of the proposed 10× speedup.
Two-byte queries scan all selected documents.

### Checkout search

The checkout benchmark writes the same corpus to a directory. It compares
reading and searching that directory with querying SQLite immediately after
a sync. Both paths use the library entry points served by the checkout API.

| Query | Unindexed checkout | SQLite | Speedup |
| --- | ---: | ---: | ---: |
| Identifier in three files | 100 ms | 1.25 ms | 80× |
| Phrase in one file | 115 ms | 13.2 ms | 8.7× |
| Two bytes | 95.1 ms | 24.2 ms | 3.9× |

Run `CGO_ENABLED=0 go test ./checkout -run '^$' -bench '^BenchmarkCheckout$' -benchtime=600ms -cpu 4`.
