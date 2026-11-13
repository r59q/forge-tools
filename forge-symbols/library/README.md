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

- Supported languages, with the identifier for `File.Language`:

  | Language   | Identifier   | Extensions              |
  | ---------- | ------------ | ----------------------- |
  | Go         | `go`         | `.go`                   |
  | Java       | `java`       | `.java`                 |
  | JavaScript | `javascript` | `.js .mjs .cjs .jsx`    |
  | TypeScript | `typescript` | `.ts .mts .cts`         |
  | TSX        | `tsx`        | `.tsx`                  |
  | Python     | `python`     | `.py .pyi`              |

  The language comes from the file extension, or from `File.Language` when
  set. Anything else returns `ErrUnsupported`. TSX is separate from
  TypeScript because the TypeScript grammar cannot parse JSX, and `<T>x`
  casts mean something else in TSX. JavaScript includes JSX.
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
- JavaScript, TypeScript and TSX give definitions of kind `class`,
  `constructor`, `method` (class and object-literal methods, getters and
  setters, fields holding a function), `function` (declarations, named
  function expressions, and arrow functions or function expressions assigned
  to a variable, property or object key), `field`, and top-level `constant`
  (`const`) and `variable` (`let`, `var`). TypeScript adds `interface`,
  `type` (type aliases), `enum`, `constant` for enum members, `module`
  (namespaces and `declare module`), and fields and methods of interfaces
  and object type aliases. Overload signatures are separate definitions with
  the same name, also for constructors.
- Their references are `call` for calls, decorators and JSX components such
  as `<Button />` (lower-case elements are HTML, not references), `new` for
  object creation, and `type` for superclasses and, in TypeScript, every
  type name. `require(…)` is not a reference. Only the last part of a
  member name counts: `a.b.c()` calls `c`, `models.User` refers to `User`.
- Python gives definitions of kind `class`, `method` (a `def` directly in a
  class body), `function` (any other `def`, also nested in a function),
  `field` (a single-name assignment directly in a class body, also
  annotated without a value), top-level `constant` (a single-name
  assignment to an upper-case name such as `MAX_SIZE`) and `variable`
  (other single names), and `type` (`type` aliases). Redefinitions, such
  as `@overload` stubs, are separate definitions with the same name. The
  range of a decorated function or class starts at its first decorator,
  so calls in decorators are enclosed by the decorated definition.
- Python references are `call` for calls and decorators, and `type` for
  superclasses. As in JavaScript, only the last part of a member name
  counts: `self.db.query()` calls `query`. Attributes set on `self`,
  tuple assignments and type annotations are not tags. A `def` in an `if`
  block of a class body is a function.
- Ranges use one-based lines and UTF-16 columns with an exclusive end column,
  as in forge-explorer. Lines end at `\n`.
- Invalid or partial source is not an error. Results come from the tree the
  parser recovers, so they can be incomplete or odd near the damage.
  Python damage reaches further, because blocks are indentation: after an
  unclosed bracket or a missing colon, later definitions can be lost or a
  `def` can read as a call.
- Nothing is resolved: a reference is a name, not a link to its definition.
  Names of builtins such as `string` and `len` are references too.

## Navigation

Package `navigation`, imported as
`github.com/r59q/forge-tools/forge-symbols/library/navigation`, answers
IDE-style questions over analyses the caller supplies, for example the
files a literal search found. It reads no files and keeps no index or
cache. It works the same for every supported language.

```go
analyses, err := analyzer.AnalyzeFiles(ctx, files)
target := navigation.Target{Path: "store.go", Line: 5, Column: 18}

calls, err := navigation.Outgoing(analyses, target, navigation.Limits{})
callers, err := navigation.Incoming(analyses, target, navigation.Limits{})
definitions, err := navigation.Definitions(analyses, target, navigation.Limits{})
```

- `Outgoing` returns the references inside a target definition, in source
  order. References inside definitions nested in it, such as a local type
  or a function assigned to a local variable, are included and marked
  `Nested`.
- `Incoming` returns the references named like the target, of any kind,
  grouped by the innermost definition around each. References outside any
  definition form one group per file, whose caller has index `None`.
- `Definitions` returns the definitions named like the target, of
  `Target.Kind` when set.

A target is one of:

| Target                           | Selects                                             |
| -------------------------------- | --------------------------------------------------- |
| `Name`                           | that name; nothing ranks as same file or container  |
| `Name`, `Path`                   | that name; results in `Path` rank first             |
| `Path`, `Line`, `Column`         | what is at the position, see below                  |
| `Name`, `Path`, `Line`, `Column` | as above, but only tags with that name              |

A position selects, in order: the innermost definition whose name it is on,
the reference it is on, or the innermost definition around it. Its end
counts, so a cursor just after a name selects it. `Kind` also narrows the
definitions a position selects. In `Outgoing`, a selected definition is the
only target; a name or a reference targets every definition of the name,
with one group each.

Results of `Incoming` and `Definitions`, and the groups of `Outgoing` for a
name, are ranked in tiers, then by path, then by position:

1. `TierFile`: in the target's file.
2. `TierContainer`: in another file, with the target's `Container`. A
   selected reference takes the container of the definition around it.
   An empty container matches nothing.
3. `TierOther`: the rest.

An incoming group takes the tier of its caller and the position of its
first reference.

Every result has precision `Heuristic`: names match, nothing is resolved.
Overloads, same-named methods of different types and unrelated symbols
with the same name are all returned and ranked, not filtered. `Incoming`
cannot tell which of them a reference means.

| Limit        | Default | Exceeded           |
| ------------ | ------- | ------------------ |
| `Files`      | 1000    | `ErrTooLarge`      |
| `TotalBytes` | 16 MiB  | `ErrTooLarge`      |
| `Results`    | 1000    | `Truncated` is set |

`Files` and `TotalBytes` default as in `symbols`; bytes are summed over
`Analysis.Bytes`. `Results` counts every definition and reference
returned, group heads included. Ranking comes first, so the best results
are kept. A group is kept only if its head and a first reference fit. A
target without a name or position returns `ErrInvalidTarget`; a position
that selects nothing returns `ErrNotFound`.

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
a module takes 90 to 180 ms to compile on first use, and each language adds
a checked-in binary: 380 KB for Go, 580 KB each for Java and JavaScript,
1.6 MB each for TypeScript and TSX, 610 KB for Python.

## Grammars

`internal/grammars` holds, per language, `<name>.wasm` and the `.scm` files
of its tag query, listed in `grammars.go`. Nothing is read from analyzed
input or the filesystem.

| Part                    | Version | Source                                          |
| ----------------------- | ------- | ----------------------------------------------- |
| Tree-sitter runtime     | v0.27.2 | `github.com/tree-sitter/tree-sitter`            |
| Go grammar              | v0.25.0 | `github.com/tree-sitter/tree-sitter-go`         |
| Java grammar            | v0.23.5 | `github.com/tree-sitter/tree-sitter-java`       |
| JavaScript grammar      | v0.25.0 | `github.com/tree-sitter/tree-sitter-javascript` |
| TypeScript, TSX grammar | v0.23.2 | `github.com/tree-sitter/tree-sitter-typescript` |
| Python grammar          | v0.25.0 | `github.com/tree-sitter/tree-sitter-python`     |
| Compiler and libc       | 34      | `github.com/WebAssembly/wasi-sdk`               |

Rebuild with `internal/grammars/build/build.sh`. It downloads the pinned
sources, checks their SHA-256 and writes the modules. It needs `curl`, `tar`
and `sha256sum`; outside x86_64 Linux also `WASI_SDK_PATH` pointing to the
pinned wasi-sdk. The build is reproducible: a rebuild leaves `git status`
clean. SHA-256 of the modules:

| Module            | SHA-256                                                            |
| ----------------- | ------------------------------------------------------------------ |
| `go.wasm`         | `bcc95509f49f71697df1bd68b305719bd1f4ea3a522eeaed09cff72bd30b1c74` |
| `java.wasm`       | `e9971660371c2348b61a88d808ce2d6e5d998b59e148b1eb66508f289ebdaa8e` |
| `javascript.wasm` | `f841512e99562f6262073c8a4b34811b1924227fdcb35b7ea99376e9b044ad01` |
| `typescript.wasm` | `a257971a1b5d766fe282abd1175c7cdca42a72ce538241ce88a5f3d117292768` |
| `tsx.wasm`        | `99f9bccc367e0dd9b08689f3e3e775848bd3b028f43589b55092a981d00cd306` |
| `python.wasm`     | `dcd6243cbbf9f56524b0f4877caa3b135556224ef17bbb71272ad33fcf202986` |

The modules contain MIT-licensed Tree-sitter code and wasi-libc.

To add a language: add a `grammar` line to `build.sh` and run it, write its
query, add a row to `Languages` in `grammars.go`, and add golden tests.

A query can be joined from several files, in order, so related grammars
share patterns instead of copying them: `ecmascript.scm` serves JavaScript,
TypeScript and TSX, `typescript.scm` TypeScript and TSX, `jsx.scm`
JavaScript and TSX, and `javascript.scm` JavaScript only. Tree-sitter
rejects patterns with nodes a grammar lacks, so a shared file uses only
nodes common to all its grammars.

Tag queries use `@name` with `@definition.<kind>` or `@reference.<kind>`,
and `@container` for a definition that belongs to a type it is not nested
in. Predicates `#eq?`, `#match?` (Go regular expressions) and `#any-of?`
work, also with `not-`. Other predicates fail when the query loads. When
several patterns tag the same name, the first pattern in the query wins, so
put specific patterns before general ones such as `(type_identifier)`.
This holds also when they tag different nodes around the name, so a
pattern can widen a definition, as the Python query does with decorators.

## Golden tests

`symbols/testdata/<language>/<case>.input` is analyzed as `<language>` and
compared with `<case>.golden`. After a change, review and accept with:

```sh
go test ./symbols -run TestGolden -update
```

## Benchmarks

`go test ./symbols -run '^$' -bench .` on an AMD Ryzen 9 5950X, Go 1.27,
`CGO_ENABLED=0`, warm analyzer, generated Go code without comments and
generated JavaScript and TypeScript classes with doc comments:

| File                 | Go     | JavaScript | TypeScript |
| -------------------- | ------ | ---------- | ---------- |
| 4 KiB                | 3 ms   | 3 ms       | 3.3 ms     |
| 64 KiB               | 41 ms  | 40 ms      | 43 ms      |
| 1 MiB                | 690 ms | 680 ms     | 750 ms     |
| First call (compile) | 90 ms  | 135 ms     | 180 ms     |

Real code parses faster because comments are cheap: `net/http/server.go`
repeated to 1 MiB takes about 320 ms.

Java and Python on an Intel Core i7-12700 with 2 CPUs, Go 1.27,
`CGO_ENABLED=0`, warm analyzer, generated Java classes with Javadoc
comments, generated Python classes with docstrings and decorated methods,
and the Go case on the same machine for comparison:

| File                 | Java    | Python  | Go     |
| -------------------- | ------- | ------- | ------ |
| 4 KiB                | 1.6 ms  | 2.3 ms  | 2.5 ms |
| 64 KiB               | 24 ms   | 32 ms   | 34 ms  |
| 1 MiB                | 390 ms  | 570 ms  | 600 ms |
| First call (compile) | 100 ms  | 105 ms  | 90 ms  |
