package contextmap_test

import (
	"slices"
	"testing"

	"github.com/r59q/forge-tools/forge-context/library/contextmap"
)

func TestDiscoverConcepts(t *testing.T) {
	guide := fileID("store", "docs/integrations.md")
	api := fileID("warehouse", "reference.MD")
	files := map[contextmap.NodeID]string{
		guide:                              "\n# Order   tracking\r\n\nFind the status of a delivery.",
		api:                                "# ORDER TRACKING\n\nLook up an order by ID.",
		fileID("store", "notes.txt"):       "# Order tracking",
		fileID("store", "docs/example.md"): "Example output\n\n# Order tracking",
		fileID("store", "docs/setup.md"):   "# Installation\n\n# Installation",
	}
	graph := discover(t, files)
	concept := headingID("order tracking")
	node, ok := graph.Node(concept)
	if !ok || node.Label != "Order tracking" || len(graph.Nodes()) != len(files)+1 {
		t.Fatalf("unexpected discovered nodes: %v", graph.Nodes())
	}
	want := []contextmap.Link{
		{Kind: contextmap.LinkSupportedBy, From: concept, To: guide},
		{Kind: contextmap.LinkSupportedBy, From: concept, To: api},
	}
	if !slices.Equal(graph.Links(), want) {
		t.Errorf("got links %v, want %v", graph.Links(), want)
	}
}

func TestDiscoverConceptsRejectsInvalidEvidence(t *testing.T) {
	for _, id := range []contextmap.NodeID{
		headingID("manual"),
		fileID("", "guide.md"),
		fileID("store", "../guide.md"),
	} {
		graph, err := contextmap.DiscoverConcepts(map[contextmap.NodeID]string{id: "# Orders"})
		if graph != nil || err == nil {
			t.Errorf("expected invalid file ID %v to fail", id)
		}
	}
}

func fileID(repo, path string) contextmap.NodeID {
	return contextmap.NodeID{Kind: contextmap.KindFile, Repository: repo, Path: path}
}

func headingID(title string) contextmap.NodeID {
	return contextmap.NodeID{Kind: contextmap.KindConcept, Concept: "heading:" + title}
}

func discover(t *testing.T, files map[contextmap.NodeID]string) *contextmap.Graph {
	t.Helper()
	graph, err := contextmap.DiscoverConcepts(files)
	if err != nil {
		t.Fatal(err)
	}
	return graph
}
