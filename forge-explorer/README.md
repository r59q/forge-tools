# Forge Explorer

Explore configured live worktrees through a Go library, REST/gRPC service, or
reusable TypeScript interface. Each project builds and tests independently:
[library](library/README.md), [service](service/README.md),
[frontend and embedding](frontend/README.md).

## Sources and identities

The initial source is an explicitly configured local directory. It includes dirty,
untracked **and ignored** files: `.gitignore` is not an access-control boundary.
Only expose roots whose entire contents callers may read. Source IDs are operator
names; no host paths are returned. Reopening a root creates a new source incarnation.
A file location contains source ID, SHA-256 content version, slash path and range.
Versions include source incarnation, path and exact returned bytes, not just HEAD.
An expected version mismatch rejects navigation as stale. Reverting bytes within
one incarnation restores the same file version. Deleted files return not-found.

Reads and listings are live observations, **not immutable snapshots**. Metadata
checks detect ordinary in-place edits during reads; atomic replacement may return
the old open file. Undetectable concurrent writes can produce an observation that
never existed as a complete file. Its hash still identifies exactly the returned
bytes. Refresh reads current content; no historical content is retained.

Access is confined with Go `os.Root` on the supported Linux service platform,
including concurrent symlink replacements. Relative symlinks within the root may
be read directly; absolute and escaping links are rejected. Discovery does not
follow symlinks. Direct `.git` paths are rejected and discovery skips `.git`;
this is a discovery policy, not a security boundary against in-root aliases.
Mounts and hard links remain part of the configured root. Special files are never
read; nonblocking opens prevent substituted FIFOs from hanging a request.

## Limits and semantics

- UTF-8 regular files only, at most 1 MiB. NUL-containing and invalid UTF-8 files
  are unsupported. Extensions do not decide whether content is text.
- Browse returns at most 1000 children. Overfull directories return a sorted
  OS-enumeration prefix with `truncated`; there is no pagination yet.
- Search visits at most 10,000 entries, depth 64, 64 MiB of accepted text, and
  200 matching lines (caller may lower this). The final attempted read is at
  most 1 MiB. Service calls also have a request deadline.
- Text search is literal, case-sensitive, one result for the first match per
  line. Queries are 1–256 bytes, single-line. Paths use optional Go `path.Match`
  globs: `*` does not cross `/`; `**` is not recursive. Filename lookup uses
  substring matching and returns only readable text files.
- Ranges use one-based lines and UTF-16 columns, exclusive end columns. Zero
  range means whole file. Previews are capped at 240 UTF-16 units plus an ellipsis.
- Results report skipped entries, cancellation and truncation. Skips include
  `.git`, symlinks, special/binary/large files, excessive depth and read failures
  (including files removed or changed during search). Filtered paths are not skips.
- Search cancellation returns accumulated library results. A canceled HTTP/gRPC
  transport may discard them; the UI still displays cancellation honestly.

Symbols, definitions, references and history have capability contracts but are
unsupported for this provider. There are no subscriptions or pinned-revision
adapters yet. Future forge adapters must use forge-connector sessions; integrations
with checkouts or other tools must be explicit composition adapters. Editing,
comparisons, reviews and running repository code are outside this tool.
