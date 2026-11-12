# Symbols library

Finds definitions and references in source bytes with Tree-sitter tag
queries. Stateless and heuristic: no build, no type checking, no file or
network access, no cgo. See the [tool overview](../README.md).

```go
analyzer := symbols.New(symbols.Limits{}) // zero fields use the defaults
defer analyzer.Close(ctx)

analysis, err := analyzer.Analyze(ctx, symbols.File{Path: "main.go", Content: source})
for _, definition := range analysis.Definitions {
	fmt.Println(definition.Kind, definition.Name, definition.Container, definition.Range)
}
for _, reference := range analysis.References {
	enclosing, _ := analysis.Enclosing(reference)
	fmt.Println(reference.Kind, reference.Name, "in", enclosing.Name)
}
```

Import `github.com/r59q/forge-tools/forge-symbols/library/symbols`.
From this directory: `CGO_ENABLED=0 go build ./...`, `go vet ./...`,
`go test -race ./...`, `go mod tidy -diff`.

## Results

- Supported languages are Go (`go`, `.go`) and Java (`java`, `.java`). The
  language comes from the file extension, or from `File.Language` when set.
  Anything else returns `ErrUnsupported`.
- A definition has a name, a kind, the range of its name and the range of the
  whole declaration with its body. `Parent` is the index of the innermost
  definition around it, or `None`. `Container` names what it belongs to: the
  receiver type of a Go method, or else the parent.
- A reference has a name, a kind, the range of the name and `Enclosing`, the
  index of the innermost definition around it, or `None`.
- Kinds are the suffixes of the query captures. Go gives definitions of kind
  `function`, `method`, `struct`, `interface`, `type`, `field`, `constant` and
  `variable`, and references of kind `call` and `type`. Local variables and
  constants are not definitions; local types are.
- Java gives definitions of kind `class`, `interface`, `enum`, `record`,
  `annotation`, `method`, `constructor`, `field` (also record components)
  and `constant` (enum constants and interface fields). References are
  `call` for method calls and method references such as `Text::trim`, `new`
  for object creation and constructor references such as `Text::new`, and
  `type` for other type names, annotations included. Overloads are separate
  definitions with the same name.
- In Java, methods of an anonymous class belong to the definition around the
  class, and methods in the body of an enum constant to the constant. Calls
  of `this(…)` and `super(…)` are not references, nor are names used as
  expressions, such as `Math` in `Math.max(a, b)`. Each part of a qualified
  type such as `java.util.List` is a type reference, and so is a type
  parameter where it is declared.
- Ranges use one-based lines and UTF-16 columns with an exclusive end column,
  as in forge-explorer. Lines end at `\n`.
- Invalid or partial source is not an error. Results come from the tree the
  parser recovers, so they can be incomplete or odd near the damage.
- Nothing is resolved: a reference is a name, not a link to its definition.
  Names of builtins such as `string` and `len` are references too.

## Limits

| Limit         | Default | Exceeded                               |
| ------------- | ------- | -------------------------------------- |
| `FileBytes`   | 1 MiB   | `ErrTooLarge`                          |
| `Files`       | 1000    | `ErrTooLarge` from `AnalyzeFiles`      |
| `TotalBytes`  | 16 MiB  | `ErrTooLarge` from `AnalyzeFiles`      |
| `ParseTime`   | 5 s     | `ErrTimeout`, per file                 |
| `MemoryBytes` | 256 MiB | `ErrMemory`, per file (at least 4 MiB) |

A context that ends first returns its own error. Errors never contain paths
or source text. Parse time and memory matter for hostile input: 1 MiB of `(`
would otherwise run for over 20 s and 190 MiB.

## Runtime

Parsing runs the reference Tree-sitter C runtime, compiled to WebAssembly
together with one grammar and a small shim, inside
[wazero](https://github.com/tetratelabs/wazero), a pure Go WebAssembly
runtime. Parsers are pooled per language and reused.

Chosen because it is the upstream parser, so trees, error recovery and
queries behave as in editors; it needs no cgo; the sandbox gives a hard
memory cap; and the Tree-sitter progress callbacks stop a parse when the
context ends. Queries and modules are embedded with `go:embed`.

Rejected in the spike (AMD Ryzen 9 5950X, valid 1 MiB Go file unless noted):

| Option | Why not |
| ------ | ------- |
| `github.com/odvcencio/gotreesitter` v0.57.0, a pure Go reimplementation | Parse 600–670 ms and about 165 MB allocated, against 230 ms in wazero. Error recovery degrades badly: 68 s for a file with a garbage tail, 12 s and 3 GB allocated for concatenated broken files. Rejects `#set-adjacent!`. Single maintainer, 61 MB module. |
| cgo bindings (`tree-sitter/go-tree-sitter`, `smacker/go-tree-sitter`) | Need cgo and a C toolchain, which the requirement excludes. |
| `github.com/malivvan/tree-sitter`, a wazero wrapper | A single v0.0.1 release from January 2025, not maintained since. |
| wazero `WithCloseOnContextDone` for cancellation | Checks the context in every loop: parse 570 ms instead of 230 ms. |
| Language parsers such as `go/parser` | One model per language and no path to the other planned languages. |

Costs of the choice: WebAssembly runs slower than a native build would,
a module takes 90 to 100 ms to compile on first use, and each language adds
a checked-in binary: 380 KB for Go, 580 KB for Java.

## Grammars

`internal/grammars` holds, per language, `<name>.wasm` and the tag query
`<name>.scm`. Nothing is read from analyzed input or the filesystem.

| Part                | Version | Source                                    |
| ------------------- | ------- | ----------------------------------------- |
| Tree-sitter runtime | v0.27.2 | `github.com/tree-sitter/tree-sitter`      |
| Go grammar          | v0.25.0 | `github.com/tree-sitter/tree-sitter-go`   |
| Java grammar        | v0.23.5 | `github.com/tree-sitter/tree-sitter-java` |
| Compiler and libc   | 34      | `github.com/WebAssembly/wasi-sdk`         |

Rebuild with `internal/grammars/build/build.sh`. It downloads the pinned
sources, checks their SHA-256 and writes the modules. It needs `curl`, `tar`
and `sha256sum`; outside x86_64 Linux also `WASI_SDK_PATH` pointing to the
pinned wasi-sdk. The build is reproducible: a rebuild leaves `git status`
clean. SHA-256 of the modules:

| Module      | SHA-256                                                            |
| ----------- | ------------------------------------------------------------------ |
| `go.wasm`   | `bcc95509f49f71697df1bd68b305719bd1f4ea3a522eeaed09cff72bd30b1c74` |
| `java.wasm` | `e9971660371c2348b61a88d808ce2d6e5d998b59e148b1eb66508f289ebdaa8e` |

The modules contain MIT-licensed Tree-sitter code and wasi-libc.

To add a language: add a `grammar` line to `build.sh` and run it, write
`<name>.scm`, add a row to `Languages` in `grammars.go`, and add golden tests.

Tag queries use `@name` with `@definition.<kind>` or `@reference.<kind>`,
and `@container` for a definition that belongs to a type it is not nested
in. Predicates `#eq?`, `#match?` (Go regular expressions) and `#any-of?`
work, also with `not-`. Other predicates fail when the query loads. When
several patterns tag the same name, the first pattern in the query wins, so
put specific patterns before general ones such as `(type_identifier)`.

## Golden tests

`symbols/testdata/<language>/<case>.input` is analyzed as `<language>` and
compared with `<case>.golden`. After a change, review and accept with:

```sh
go test ./symbols -run TestGolden -update
```

## Benchmarks

`go test ./symbols -run '^$' -bench .` on an AMD Ryzen 9 5950X, Go 1.27,
`CGO_ENABLED=0`, warm analyzer, generated Go code without comments:

| File                 | Time per file |
| -------------------- | ------------- |
| 4 KiB                | 3 ms          |
| 64 KiB               | 41 ms         |
| 1 MiB                | 690 ms        |
| First call (compile) | 90 ms extra   |

Real code parses faster because comments are cheap: `net/http/server.go`
repeated to 1 MiB takes about 320 ms.

Java on an Intel Core i7-12700 with 2 CPUs, Go 1.27, `CGO_ENABLED=0`, warm
analyzer, generated Java classes with Javadoc comments, and the Go case on
the same machine for comparison:

| File                 | Java    | Go     |
| -------------------- | ------- | ------ |
| 4 KiB                | 1.6 ms  | 2.5 ms |
| 64 KiB               | 24 ms   | 34 ms  |
| 1 MiB                | 390 ms  | 600 ms |
| First call (compile) | 100 ms  | 90 ms  |
