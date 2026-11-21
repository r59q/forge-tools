package contextmap_test

import (
	"fmt"

	"github.com/r59q/forge-tools/forge-context/library/contextmap"
)

func ExampleGraph() {
	const repo = "github.com/acme/widget"
	const filePath = "docs/guide.md"

	repository := contextmap.NodeID{Kind: contextmap.KindRepository, Repository: repo}
	folder := contextmap.NodeID{Kind: contextmap.KindFolder, Repository: repo, Path: "docs"}
	file := contextmap.NodeID{Kind: contextmap.KindFile, Repository: repo, Path: filePath}
	lines := contextmap.NodeID{
		Kind: contextmap.KindLineRange, Repository: repo, Path: filePath,
		Lines: contextmap.LineRange{Start: 3, End: 5},
	}
	review := contextmap.NodeID{Kind: contextmap.KindConcept, Concept: "review:widget:42"}

	graph, err := contextmap.NewGraph(
		[]contextmap.Node{
			{ID: repository, Label: "Widget"},
			{ID: folder, Label: "docs"},
			{ID: file, Label: "guide.md"},
			{ID: lines, Label: "lines 3–5"},
			{ID: review, Label: "Review 42"},
		},
		[]contextmap.Link{
			{Kind: contextmap.LinkContains, From: repository, To: folder},
			{Kind: contextmap.LinkContains, From: folder, To: file},
			{Kind: contextmap.LinkContains, From: file, To: lines},
			{Kind: contextmap.LinkChanges, From: review, To: lines},
		},
	)
	if err != nil {
		panic(err)
	}

	for _, node := range graph.Nodes() {
		fmt.Printf("%s %s\n", node.ID.Kind, node.Label)
	}
	for _, link := range graph.Links() {
		from, _ := graph.Node(link.From)
		to, _ := graph.Node(link.To)
		fmt.Printf("%s %s %s\n", from.Label, link.Kind, to.Label)
	}

	// Output:
	// repository Widget
	// folder docs
	// file guide.md
	// line-range lines 3–5
	// concept Review 42
	// Widget contains docs
	// docs contains guide.md
	// guide.md contains lines 3–5
	// Review 42 changes lines 3–5
}
