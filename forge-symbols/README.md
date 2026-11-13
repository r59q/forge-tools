# Forge Symbols

Find definitions and references in source code, the way an editor outlines a
file. Useful on its own and for code navigation in other tools.

## Layout

- [`library/`](library/README.md): the analysis and navigation over it as
  a Go library.
- [`service/`](service/README.md): REST and gRPC service exposing the
  library.

The [full-stack example](../examples/fullstack/#code-symbols) runs the
service with the rest of the stack.

## Design

- **Stateless.** A request carries the file paths and bytes to analyze and
  gets their definitions and references back. Nothing is indexed, cached
  between requests or stored.
- **Heuristic.** Tree-sitter parses each file alone and tag queries pick out
  names. There is no build, type checking or cross-file resolution, so
  results are fast and work on broken code, but a reference is only a name.
- **No I/O.** The library reads no files, repositories or network. Grammars
  and queries are compiled in. Callers decide where content comes from; the
  tool needs no forge connection.
- **Bounded.** File size, file count, total bytes, parse time and memory are
  limited, so untrusted source cannot exhaust a host.
- **Sandboxed, no cgo.** Parsers run as WebAssembly in a pure Go runtime.

Supported languages: Go, Java, JavaScript, TypeScript, TSX and Python.
