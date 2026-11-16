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
From this directory: `CGO_ENABLED=0 go build ./...`, `go vet ./...`,
`go test -race ./...`, `go mod tidy -diff`.

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
| `Entries`       | 10000   | Storage, per evidence folder        | `ErrTooLarge` |

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
