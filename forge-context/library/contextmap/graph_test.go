package contextmap_test

import (
	"slices"
	"testing"

	"github.com/r59q/forge-tools/forge-context/library/contextmap"
)

func TestGraphLocationIdentity(t *testing.T) {
	const sharedPath = "guide.md"
	first := contextmap.NodeID{Kind: contextmap.KindFile, Repository: "forge-a/acme/widget", Path: sharedPath}
	second := first
	second.Repository = "forge-b/acme/widget"
	single := first
	single.Kind = contextmap.KindLineRange
	single.Lines = contextmap.LineRange{Start: 2, End: 2}
	span := single
	span.Lines.End = 5
	review := contextmap.NodeID{Kind: contextmap.KindConcept, Concept: "review:42"}

	nodes := []contextmap.Node{
		{ID: first, Label: "First file"},
		{ID: second, Label: "Second file"},
		{ID: single, Label: "Single line"},
		{ID: span, Label: "Several lines"},
		{ID: review, Label: "Review"},
	}
	links := []contextmap.Link{
		{Kind: contextmap.LinkContains, From: first, To: single},
		{Kind: contextmap.LinkContains, From: first, To: span},
		{Kind: contextmap.LinkChanges, From: review, To: span},
		{Kind: "references", From: span, To: second},
	}
	graph := makeGraph(t, nodes, links)
	if !slices.Equal(graph.Nodes(), nodes) || !slices.Equal(graph.Links(), links) {
		t.Fatal("exported graph differs from the input")
	}

	wantEndpoints := [][2]contextmap.Node{
		{nodes[0], nodes[2]}, {nodes[0], nodes[3]}, {nodes[4], nodes[3]}, {nodes[3], nodes[1]},
	}
	for i, link := range graph.Links() {
		from, foundFrom := graph.Node(link.From)
		to, foundTo := graph.Node(link.To)
		if !foundFrom || !foundTo || [2]contextmap.Node{from, to} != wantEndpoints[i] {
			t.Errorf("link %d resolves to (%q, %q), want (%q, %q)",
				i, from.Label, to.Label, wantEndpoints[i][0].Label, wantEndpoints[i][1].Label)
		}
	}
}

func TestGraphRejectsInvalidLinks(t *testing.T) {
	repo := contextmap.NodeID{Kind: contextmap.KindRepository, Repository: "one"}
	folder := contextmap.NodeID{Kind: contextmap.KindFolder, Repository: "one", Path: "docs"}
	file := contextmap.NodeID{Kind: contextmap.KindFile, Repository: "one", Path: "docs/guide.md"}
	otherFolder := folder
	otherFolder.Repository = "two"
	missing := contextmap.NodeID{Kind: contextmap.KindRepository, Repository: "missing"}
	nodes := []contextmap.Node{{ID: repo}, {ID: folder}, {ID: file}, {ID: otherFolder}}

	tests := []struct {
		name string
		link contextmap.Link
	}{
		{"missing source", contextmap.Link{Kind: contextmap.LinkChanges, From: missing, To: file}},
		{"missing destination", contextmap.Link{Kind: contextmap.LinkChanges, From: file, To: missing}},
		{"empty kind", contextmap.Link{From: folder, To: file}},
		{"reversed containment", contextmap.Link{Kind: contextmap.LinkContains, From: file, To: folder}},
		{"indirect containment", contextmap.Link{Kind: contextmap.LinkContains, From: repo, To: file}},
		{"cross-repository containment", contextmap.Link{Kind: contextmap.LinkContains, From: otherFolder, To: file}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := contextmap.NewGraph(nodes, []contextmap.Link{tt.link}); err == nil {
				t.Fatal("NewGraph accepted an invalid link")
			}
		})
	}
}

func TestGraphOwnsItsData(t *testing.T) {
	repo := contextmap.Node{ID: contextmap.NodeID{Kind: contextmap.KindRepository, Repository: "one"}}
	file := contextmap.Node{ID: contextmap.NodeID{Kind: contextmap.KindFile, Repository: "one", Path: "guide.md"}}
	link := contextmap.Link{Kind: contextmap.LinkContains, From: repo.ID, To: file.ID}
	nodes, links := []contextmap.Node{repo, file}, []contextmap.Link{link}
	graph := makeGraph(t, nodes, links)

	nodes[0], links[0] = contextmap.Node{}, contextmap.Link{}
	graph.Nodes()[0], graph.Links()[0] = contextmap.Node{}, contextmap.Link{}
	got, found := graph.Node(repo.ID)
	if !found || got != repo || graph.Nodes()[0] != repo || graph.Links()[0] != link {
		t.Fatal("changing input or exported slices altered the graph")
	}
}

func makeGraph(t *testing.T, nodes []contextmap.Node, links []contextmap.Link) *contextmap.Graph {
	t.Helper()
	graph, err := contextmap.NewGraph(nodes, links)
	if err != nil {
		t.Fatal(err)
	}
	return graph
}
