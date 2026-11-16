# Forge Evidence

Stores evidence, such as test results, screenshots, logs or analysis
documents, that backs a claim like "this test passes" or "the UI looks like
this" in change requests and code analysis.

## Entries

An entry is one or more files plus machine-readable metadata:

- a **title** or claim, such as "login form renders on mobile";
- a **kind**, such as a test result, screenshot, log or document;
- a **subject**: the commit SHA and the path the evidence is about.

## Storage

Storage is a strategy. Repository storage is the one built now; S3 is
planned.

Repository storage keeps evidence in `.evidence/` at the repository root, as
ordinary files on the same branch as the code. It uses no separate ref and no
Git LFS.

## Layout

- [`library/`](library/README.md): entries and repository storage, as a Go
  library. Its README sets the folder layout, metadata format and version,
  limits, and how to build and test.

[FUTURE.md](FUTURE.md) lists ideas that are not built yet.

## Design

- **Independent.** forge-evidence imports no other tool. A caller passes in
  a checkout path.
- **Untrusted content.** Reads and writes stay inside the evidence folder.
  Sizes are bounded, and a limit that is hit fails visibly.
- **Stable.** Binary files round-trip byte for byte. Listing order and
  generated metadata are stable, so the same evidence gives the same diff.
- **Conservative limits.** Binary evidence committed to a branch stays in Git
  history for good, and
  [forge-checkouts](../forge-checkouts/library/README.md) does not download
  Git LFS content.
- **Not CI artifacts.** forge-actions already handles CI artifacts. Evidence
  differs because it is versioned with the repository.
- **No side effects.** The library does not commit, push or call forge APIs.
