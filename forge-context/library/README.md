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
- A concept has its own key, which is unique among concepts
  in the graph. It can relate to locations in any repository. A node's optional
  display label is independent of its identity.

Set unused identity fields to their zero values.

## Direct relationships

Links are directed from `From` to `To`. `LinkContains` points from a
repository or folder to an immediate child folder or file, or from a file to
a range in that file. `LinkChanges` points from a node representing a change
to an affected node. `LinkSupportedBy` points from a concept to its evidence.
`LinkGroups` points from a parent concept to a child concept. Callers can
define additional non-empty link kinds for relationships such as references.

`NewGraph` accepts explicit nodes and links, including partial maps. It
validates IDs, checks that every link endpoint exists, and checks that each
containment link follows the location hierarchy. `Nodes` and `Links` return
copies in input order. Use `Node` to resolve either endpoint by ID.

## Discovering concepts from Markdown titles

Pass a complete snapshot of file IDs and their text to `DiscoverConcepts`.
It returns a fresh graph with every supplied file. In a `.md` file
(case-insensitive), discovery reads the first nonblank line if it starts
with `# ` in column one. Two or more distinct files with the same title
support one concept, regardless of repository or path. The concept key is
`heading:` followed by the lowercased title with whitespace collapsed to
single spaces. The label keeps the spelling from the first supporting file
in repository and path order.

The [billing fixtures](contextmap/testdata) follow refund and statement guides
in the `checkout` and `ledger` repositories. Initially all four guides open
with `# Billing` and support the concept
`NodeID{Kind: KindConcept, Concept: "heading:billing"}`.

| Evidence change | Result |
| --- | --- |
| Retitle the refund guides `# Billing refunds` and the statement guides `# Billing statements`. | Billing splits into two concepts. Each links to its two guides. |
| Add a `# Billing` overview in each repository. | Billing groups both concepts. Its own support links point to the overviews. |
| Remove one overview. | Billing loses its second supporting file and disappears with both grouping links. |
| Restore the four original titles. | The guides and remaining overview now support one Billing concept. |

Grouping uses the longest discovered title that is a whole-word prefix of
the child title. Rebuild from the current files after each edit or deletion.
The same snapshot yields the same ordered nodes and links.

The rule uses literal titles. Generic titles such as “Overview” can group
unrelated files, and synonyms such as “Invoicing” and “Billing” stay separate.
Front matter, alternate heading syntax, and topics mentioned only in body
text fall outside this rule.

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
