package contextmap_test

import (
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-context/library/contextmap"
)

func TestReviseConceptsFromEvidence(t *testing.T) {
	billing := headingID("billing")
	refunds := headingID("billing refunds")
	statements := headingID("billing statements")

	refundGuides := []contextmap.NodeID{fileID("checkout", "refunds.md"), fileID("ledger", "refunds.md")}
	statementGuides := []contextmap.NodeID{fileID("checkout", "statements.md"), fileID("ledger", "statements.md")}
	allGuides := slices.Concat(refundGuides, statementGuides)
	overviews := []contextmap.NodeID{fileID("checkout", "README.md"), fileID("ledger", "README.md")}

	broad := readFixture(t, "broad")
	files := maps.Clone(broad)
	checkMemberships(t, files, map[contextmap.NodeID][]contextmap.NodeID{billing: allGuides})

	// Retitling the same four files splits Billing into two cross-repository concepts.
	maps.Copy(files, readFixture(t, "split"))
	split := map[contextmap.NodeID][]contextmap.NodeID{refunds: refundGuides, statements: statementGuides}
	checkMemberships(t, files, split)

	// Independent overviews provide evidence for a shared parent.
	maps.Copy(files, readFixture(t, "overviews"))
	grouped := maps.Clone(split)
	grouped[billing] = overviews
	graph := checkMemberships(t, files, grouped,
		contextmap.Link{Kind: contextmap.LinkGroups, From: billing, To: refunds},
		contextmap.Link{Kind: contextmap.LinkGroups, From: billing, To: statements},
	)
	again := discover(t, files)
	if !slices.Equal(again.Nodes(), graph.Nodes()) || !slices.Equal(again.Links(), graph.Links()) {
		t.Fatal("reprocessing the same evidence changed the graph")
	}

	// Removing one overview drops the parent and both grouping links.
	delete(files, overviews[1])
	checkMemberships(t, files, split)

	// Restoring the broad titles merges both sets of guides with the remaining overview.
	maps.Copy(files, broad)
	checkMemberships(t, files, map[contextmap.NodeID][]contextmap.NodeID{billing: append(allGuides, overviews[0])})
}

func TestGroupingUsesClosestWholeWordPrefix(t *testing.T) {
	files := make(map[contextmap.NodeID]string)
	members := make(map[contextmap.NodeID][]contextmap.NodeID)
	for _, title := range []string{"pay", "payment", "payment card", "payment card issuer"} {
		concept := headingID(title)
		for _, repo := range []string{"checkout", "ledger"} {
			id := fileID(repo, title+".md")
			files[id] = "# " + title
			members[concept] = append(members[concept], id)
		}
	}
	checkMemberships(t, files, members,
		contextmap.Link{Kind: contextmap.LinkGroups, From: headingID("payment"), To: headingID("payment card")},
		contextmap.Link{Kind: contextmap.LinkGroups, From: headingID("payment card"), To: headingID("payment card issuer")},
	)
}

func readFixture(t *testing.T, revision string) map[contextmap.NodeID]string {
	t.Helper()
	root := os.DirFS("testdata/" + revision)
	names, err := fs.Glob(root, "*/*.md")
	if err != nil || len(names) == 0 {
		t.Fatalf("cannot load fixture %q: files %v, error %v", revision, names, err)
	}
	files := make(map[contextmap.NodeID]string, len(names))
	for _, name := range names {
		content, err := fs.ReadFile(root, name)
		if err != nil {
			t.Fatal(err)
		}
		repo, path, _ := strings.Cut(name, "/")
		files[fileID(repo, path)] = string(content)
	}
	return files
}

func checkMemberships(t *testing.T, files map[contextmap.NodeID]string,
	members map[contextmap.NodeID][]contextmap.NodeID, groups ...contextmap.Link,
) *contextmap.Graph {
	t.Helper()
	graph := discover(t, files)
	if got, want := len(graph.Nodes()), len(files)+len(members); got != want {
		t.Fatalf("got %d nodes, want %d: %v", got, want, graph.Nodes())
	}
	for file := range files {
		if _, ok := graph.Node(file); !ok {
			t.Errorf("missing file %v", file)
		}
	}
	wantLinks := slices.Clone(groups)
	for concept, support := range members {
		for _, file := range support {
			wantLinks = append(wantLinks, contextmap.Link{Kind: contextmap.LinkSupportedBy, From: concept, To: file})
		}
	}
	links := graph.Links()
	if len(links) != len(wantLinks) {
		t.Errorf("got %d links, want %d: %v", len(links), len(wantLinks), links)
	}
	for _, want := range wantLinks {
		if !slices.Contains(links, want) {
			t.Errorf("missing link %v", want)
		}
	}
	return graph
}
