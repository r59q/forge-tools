# Evidence library

The forge-evidence model: entries of evidence files with machine-readable
metadata, the versioned metadata format and the layout of the `.evidence/`
folder. The package is pure: it reads and writes no files, so every storage
strategy shares one definition. See the [tool overview](../README.md).

```go
entry := evidence.Entry{
	Name:    "unit-tests",
	Title:   "Unit tests pass",
	Kind:    evidence.KindTestResult,
	Subject: evidence.Subject{Commit: commit, Path: "forge-evidence/library"},
	Files:   []evidence.File{{Name: "junit.xml", Size: size, SHA256: digest}},
}

data, err := evidence.Encode(entry, evidence.Limits{})
path, err := entry.ID().MetadataPath() // .evidence/<commit>/unit-tests/entry.json

entry, err = evidence.Decode(data, evidence.Limits{})
id, file, err := evidence.ParsePath(path)
```

Import `github.com/r59q/forge-tools/forge-evidence/library/evidence`.
[Storage](#storage) keeps entries through the `storage` and `folder`
packages. From this directory: `CGO_ENABLED=0 go build ./...`,
`go vet ./...`, `go test -race ./...`, `go mod tidy -diff`.

## Entries

An entry is one piece of evidence: one or more files that back a claim.

| Field     | Required | Rule                                                           |
| --------- | -------- | -------------------------------------------------------------- |
| `Name`    | yes      | A [name](#names), unique among entries about one commit        |
| `Title`   | yes      | The claim, one line of 1 to 256 bytes of UTF-8                 |
| `Kind`    | yes      | 1 to 32 bytes of `a-z`, `0-9` and `-`, starting with a letter  |
| `Subject` | yes      | What the evidence is about: a commit and an optional path      |
| `Files`   | yes      | 1 to `Limits.Files` files, each with name, size and SHA-256    |

Known kinds are `test-result`, `screenshot`, `log` and `document`. Other
valid kinds are kept as they are, so a new kind needs no new format version.

### Subject

- `Commit` is **required**: a full commit SHA in lowercase hex, 40 digits
  for SHA-1 or 64 for SHA-256. Abbreviated SHAs and ref names are rejected.
- `Path` is **optional**; empty means the whole commit, as for a test run.
  It is repository-relative with `/` separators: valid UTF-8, at most 4096
  bytes, no leading or trailing `/`, no empty, `.` or `..` elements, no
  backslashes or control characters. It need not be a valid name.

The subject commit is the commit the evidence is **about**, not the commit
that adds it. An entry cannot name the commit that contains it, since that SHA
is not known before committing.

The identity of an entry, `ID`, is its subject commit and name.

### Names

Entry and file names follow one rule, checked by `ValidName`, so a name is a
single safe path element on every file system and object store:

- 1 to 64 bytes of `a-z`, `A-Z`, `0-9`, `.`, `-` and `_`;
- starts with a letter or digit, so no `.`, `..`, hidden files or flags;
- no `..` anywhere and no trailing `.`;
- not a Windows device name, such as `con`, `nul.txt` or `COM1.log`.

Separators, backslashes, NUL, control characters and anything else are
rejected. Two file names of one entry that differ only in case are
duplicates, since they collide on some file systems. A file cannot be named
`entry.json`, in any case.

## Layout

Entries live in `.evidence/` at the repository root, grouped by subject
commit. Each entry is one folder holding its metadata and its files, two
levels deep and never more:

```text
.evidence/
  4b825dc642cb6eb9a060e54bf8d69288fbee4904/
    unit-tests/
      entry.json
      go-test.log
      junit.xml
    login-mobile/
      entry.json
      login.png
```

| Path                                   | Helper                    |
| -------------------------------------- | ------------------------- |
| `.evidence/<commit>`                   | `CommitDir`               |
| `.evidence/<commit>/<name>`            | `ID.Dir`                  |
| `.evidence/<commit>/<name>/entry.json` | `ID.MetadataPath`         |
| `.evidence/<commit>/<name>/<file>`     | `ID.FilePath`             |

`ParsePath` maps an entry folder or file path back to its `ID` and file name.
Paths are relative to the repository root with `/` separators. The commit
folder is the full lowercase SHA, so the evidence about a commit is found by
its SHA alone, and the paths stay under 210 bytes.

## Metadata format

Each entry folder holds `entry.json`, in format version 1:

```json
{
  "version": 1,
  "name": "unit-tests",
  "title": "Unit tests pass",
  "kind": "test-result",
  "subject": {
    "commit": "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
    "path": "forge-evidence/library"
  },
  "files": [
    {
      "name": "go-test.log",
      "size": 0,
      "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    },
    {
      "name": "junit.xml",
      "size": 2048,
      "sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
    }
  ]
}
```

- **Readable.** Indented JSON in UTF-8 with a final newline, shown as is in a
  forge's file browser.
- **Deterministic.** `Encode` writes fields in this order and files sorted by
  name, so the same entry always gives the same bytes. The library adds no
  timestamps or random IDs. `path` is left out when empty.
- **Files are listed.** Each file has its name, size in bytes and SHA-256 in
  lowercase hex, so validation can find missing, extra and changed files.
  The metadata file itself is not listed.
- **Self-describing.** The name and commit are in the file as well as in
  its path, so the file means the same in any storage strategy.
- **Strict.** `Decode` rejects malformed JSON, invalid UTF-8, duplicate
  fields, field names in another case, **unknown fields**, missing required
  fields and invalid values with `ErrInvalid`. A new field means a new
  version.
- **Versioned.** `version` is read first. Any version other than 1 returns
  `ErrVersion`, whatever else the file holds.

## Limits

| Limit           | Default | Checked by                          | Exceeded      |
| --------------- | ------- | ----------------------------------- | ------------- |
| `MetadataBytes` | 64 KiB  | `Encode`, `Decode`, before parsing  | `ErrTooLarge` |
| `FileBytes`     | 4 MiB   | `Validate`, by recorded size        | `ErrTooLarge` |
| `Files`         | 32      | `Validate`, per entry               | `ErrTooLarge` |
| `EntryBytes`    | 16 MiB  | `Validate`, sum of recorded sizes   | `ErrTooLarge` |
| `Entries`       | 10000   | Children scanned by storage        | `ErrTooLarge` |
| `Problems`      | 1000    | Problems returned by storage       | `Truncated`   |

Zero or negative fields use the default; `WithDefaults` resolves them.
`Encode` and `Decode` call `Validate`. Storage checks the bytes it actually
reads and writes against the same limits.

The defaults are small on purpose. Evidence committed to a branch stays in
Git history for good, even after it is deleted, and every clone downloads
it. [forge-checkouts](../../forge-checkouts/library/README.md) does not
download Git LFS content, so evidence cannot move there either.

## Errors

| Error         | Meaning                                                    |
| ------------- | ---------------------------------------------------------- |
| `ErrInvalid`  | Malformed metadata, name, kind, title, subject or path     |
| `ErrVersion`  | Metadata in a format version this library does not read    |
| `ErrTooLarge` | Metadata, a file, an entry or a listing over a limit       |

Match them with `errors.Is`. Errors name the field and the reason, and a
file's position or a byte offset, but never metadata values or file content.

## Storage

Storage is a strategy. Package `storage` holds the boundary, `Store`, that
every strategy implements:

| Method     | Does                                                         |
| ---------- | ------------------------------------------------------------ |
| `Add`      | Stores a new entry from a `Draft`: its fields and file bytes |
| `Entry`    | Returns the metadata of an entry                             |
| `List`     | Returns matching entry metadata                              |
| `ReadFile` | Returns the bytes of one file listed in an entry's metadata  |
| `Validate` | Reports layout, metadata and file problems                 |

Callers name entries by `ID` and files by name, never by a path or object
key, so a strategy, such as a future S3 one, can be swapped without changing
them. `Draft.Entry` computes each file's size and SHA-256.

| Error           | Meaning                                                 |
| --------------- | ------------------------------------------------------- |
| `ErrExist`      | `Add` of an entry that already exists                   |
| `ErrNotExist`   | A missing entry, or a file not listed in its metadata   |
| `ErrUnreadable` | Storage that cannot be read or written, or that changed |

Strategies also return `evidence.ErrInvalid`, `ErrVersion` and
`ErrTooLarge`. Match all of them with `errors.Is`.

### Repository folder

Package `folder` keeps entries in the `.evidence/` folder of a repository
checkout, in the [layout](#layout) and [metadata format](#metadata-format)
above. The caller supplies the repository root, any folder:

```go
store, err := folder.Open(checkoutDir, evidence.Limits{})
defer store.Close()

entry, err := store.Add(ctx, storage.Draft{
	Name:    "unit-tests",
	Title:   "Unit tests pass",
	Kind:    evidence.KindTestResult,
	Subject: evidence.Subject{Commit: commit},
	Files:   []storage.Content{{Name: "junit.xml", Data: report}},
})

entry, err = store.Entry(ctx, entry.ID())
entries, err := store.List(ctx, storage.Filter{Commit: commit, Kind: evidence.KindTestResult})
report, err = store.ReadFile(ctx, entry.ID(), "junit.xml")
validation, err := store.Validate(ctx)
```

- **Add** checks the draft and encodes its metadata before it touches the
  folder, then creates `.evidence/` and the commit folder when missing. It
  writes the files, then `entry.json`, to a hidden `.add-*` folder beside the
  entry folder and renames it into place. An existing entry returns
  `ErrExist` and is left as it is. A failed `Add` leaves no entry; it may
  leave empty folders. The same entry always gives the same files.
- **Entry** reads `entry.json` and checks that it names the entry of its
  folder. A folder without it is not an entry.
- **ReadFile** returns the bytes as they were added, after checking their
  size and SHA-256 against the metadata.
- **Confined.** Names are checked before the folder is touched. Every read
  and write stays inside `.evidence/`: no symlink is followed, not even
  `.evidence/` itself or one pointing inside the folder, and special files,
  such as FIFOs, are not read.
- **No Git.** It does not commit, push or check that the subject commit
  exists. Errors name paths relative to the root, never host paths or file
  content.

| Limit           | `Add`                   | `Entry`, `List`, `ReadFile`              |
| --------------- | ----------------------- | ---------------------------------------- |
| `MetadataBytes` | Encoded metadata        | `entry.json` size, before it is read     |
| `FileBytes`     | Each file's bytes       | Recorded sizes; `ReadFile` file size too |
| `Files`         | Files in the draft      | Files in the metadata                    |
| `EntryBytes`    | Sum of the files' bytes | Sum of the recorded sizes                |

Sizes on disk are checked before content is read, and an exceeded limit
returns `ErrTooLarge`; nothing is truncated.

### Listing

`List` returns `[]evidence.Entry` in ascending bytewise order by subject
commit SHA, then by entry name. It reads `entry.json` only. A missing or
empty `.evidence/` returns an empty list.

`storage.Filter` has three case-sensitive, exact-match fields. `Commit`
matches the full subject SHA, `Path` matches the full repository-relative
subject path, and `Kind` matches the evidence kind. Empty fields match any
value. All set fields must match.

`Limits.Entries` caps the number of children in `.evidence/` and separately
the total number of children across its commit folders. These counts include
folders and files regardless of the filter, including Add's staging folders.
`Limits.MetadataBytes` caps each `entry.json`. Exceeding either limit returns
`ErrTooLarge` with no results.

Listing checks every entry before applying the filter. A malformed or
unreadable entry, an unsafe name, or a symlink at a commit or entry path
fails the list with an error naming the repository-relative path. Add's
temporary `.add-` directories with random base32 suffixes are ignored.

### Validation

`Validate(ctx)` checks the folder without changing it. A missing or empty
`.evidence/` is valid and returns an empty report. Each problem has a
repository-relative `Path`, a machine-readable `Reason` and a short
`Message`. The report is sorted by path and then by reason, both in
ascending bytewise order. When more than `Limits.Problems` problems are
found, the report keeps the first ones found in the deterministic scan and
sets `Truncated`. Messages omit file contents, metadata values and host
paths. A missing listed file is located by its validated file name.

Content problems go in the report, and the scan continues to other entries.
An unreadable path, a cancelled context or exceeding `Limits.Entries`
returns an error and an empty report because the check could not finish.
`Limits.Entries` applies as it does in listing.

Validation checks sizes on disk before reading. It reports unexpected
directories at their boundary and skips their contents. It also flags
`Add`'s temporary directories if present, so validate a stable checkout.
An entry over the file count limit gets a count problem and its payload
scan is skipped. An entry over the total byte limit is checked without
reading payload contents. File comparisons require valid metadata.

| Reason | Meaning |
| ------ | ------- |
| `missing_metadata` | The entry has no `entry.json`. |
| `malformed_metadata` | The metadata violates the JSON format, including duplicate or unknown members and wrong value types. |
| `oversized_metadata` | The metadata exceeds `Limits.MetadataBytes`. |
| `unknown_version` | The metadata format version is unsupported. |
| `invalid_field` | A required field is missing, a value is invalid or the ID differs from the folder's. |
| `unsafe_name` | A commit directory, entry directory or evidence file breaks the name rules. |
| `symlink` | The path is a symbolic link. |
| `special_file` | The path is a special file, such as a FIFO. |
| `stray_path` | The layout expects a different type of file or directory at this path. |
| `file_too_large` | An actual or recorded file size exceeds `Limits.FileBytes`. |
| `too_many_files` | An actual or recorded file count exceeds `Limits.Files`. |
| `entry_too_large` | An actual or recorded total exceeds `Limits.EntryBytes`. |
| `missing_file` | A listed file is absent. |
| `unlisted_file` | An evidence file is absent from the metadata. |
| `file_mismatch` | A listed file differs from its recorded size or SHA-256. |
