# Forge Search

Literal text search over several aggregates: code from checkouts, issues
and symbols. One set of query rules serves two modes.

## Layout

- [`library/`](library/README.md): the search model, unindexed search and
  the index engine with its storage interface, as a Go library.

## Design

- **Two modes, one contract.** Unindexed search scans the documents a call
  carries and stores nothing. Indexed search keeps documents in named
  indexes behind a storage interface, as the fast path. Both return the same
  matches in the same order.
- **Not tied to files.** A document is identified by its aggregate kind, a
  source and an ID, such as a path in a checkout or an issue number.
- **Composes with other tools.** Ranges use one-based lines and UTF-16
  columns with an exclusive end column, as forge-explorer and forge-symbols
  do.
- **Bounded.** Document size, document count, total bytes, index size, query
  length and result count are limited, so untrusted content cannot exhaust a
  host. An exceeded limit is an error or a `truncated` flag, never a silent
  miss.

forge-search combines other tools: it may use forge-checkouts, forge-issues
and forge-symbols, and no other tool.
