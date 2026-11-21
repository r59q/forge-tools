# Context map library

`forge-context` models repository locations and concepts for consumers such
as chat and review. Import
`github.com/r59q/forge-tools/forge-context/library/contextmap`.
See the [runnable Go example](contextmap/example_test.go) for a review linked
to lines in a file.

## Node identity

- A repository has a caller-supplied key. Use a stable key unique across
  all repositories in the graph, including repositories on different forges.
  The repository node also represents the root directory.
- A folder or file is identified by its kind, repository key, and path. Paths
  are case-sensitive and relative to the repository root, with `/` separators.
  Use clean paths such as `docs/guide.md` with no leading or trailing slash,
  empty segments, `.` or `..` segments, or NUL bytes.
- A line range adds both line numbers to the file's repository and path.
  Lines start at 1 and both ends are inclusive. Equal ends identify a single
  line. Interpret coordinates against the revision used to build the graph.
  The same repository, path, and endpoints have the same identity across revisions.
- A concept has its own caller-supplied key, which is unique among concepts
  in the graph. It can relate to locations in any repository. A node's optional
  display label is independent of its identity.

Set unused identity fields to their zero values.

## Direct relationships

Links are directed from `From` to `To`. `LinkContains` points from a
repository or folder to an immediate child folder or file, or from a file to
a range in that file. `LinkChanges` points from a node representing a change
to an affected node. Callers can define additional non-empty link kinds for
relationships such as references or concept groupings.

`NewGraph` accepts explicit nodes and links, including partial maps. It
validates IDs, checks that every link endpoint exists, and checks that each
containment link follows the location hierarchy. `Nodes` and `Links` return
copies in input order. Use `Node` to resolve either endpoint by ID.

## Focused views

Pass any node's `NodeID` to `DirectView` to get the focus, its immediate
neighbors, and the links incident to the focus. `ExpandedView` follows
links repeatedly to return every reachable node and recorded link. Both
return only the focus for an isolated node and report `false` for an absent
focus.

Both views follow incoming and outgoing links and preserve each link's
original direction and kind. From a line, the direct view follows the
file-to-line containment link back to the file. The expanded view also
follows the folder-to-file link back to the folder.

In the expanded result, a link with `link.From == view.Focus` or
`link.To == view.Focus` identifies an immediate neighbor at the other
endpoint. A node reached only through other nodes has no link to the
focus. Each node appears once, identified by its `NodeID`.

## Build and test

Run from this directory with Go 1.27 and golangci-lint v2.15.0.

```sh
gofmt -l .
go mod tidy -diff
CGO_ENABLED=0 go build ./...
go vet ./...
golangci-lint run ./...
go test -race ./...
```
