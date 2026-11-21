package contextmap_test

import (
	"slices"
	"testing"

	"github.com/r59q/forge-tools/forge-context/library/contextmap"
)

func TestViewsFromLine(t *testing.T) {
	const repo = "widget"
	const path = "docs/guide.md"
	folder := contextmap.Node{ID: contextmap.NodeID{Kind: contextmap.KindFolder, Repository: repo, Path: "docs"}}
	file := contextmap.Node{ID: contextmap.NodeID{Kind: contextmap.KindFile, Repository: repo, Path: path}}
	line := contextmap.Node{ID: contextmap.NodeID{
		Kind: contextmap.KindLineRange, Repository: repo, Path: path, Lines: contextmap.LineRange{Start: 3, End: 3},
	}}
	links := []contextmap.Link{
		{Kind: contextmap.LinkContains, From: folder.ID, To: file.ID},
		{Kind: contextmap.LinkContains, From: file.ID, To: line.ID},
	}
	graph := makeGraph(t, []contextmap.Node{folder, file, line}, links)

	checkView(t, graph.DirectView, line.ID, []contextmap.Node{line, file}, links[1:])
	checkView(t, graph.ExpandedView, line.ID, []contextmap.Node{line, file, folder}, []contextmap.Link{links[1], links[0]})
}

func TestViewsFromConcept(t *testing.T) {
	focus := concept("focus")
	first := concept("first")
	second := concept("second")
	shared := concept("shared")
	other := concept("other")
	disconnected := concept("disconnected")
	links := []contextmap.Link{
		{Kind: "references", From: focus.ID, To: first.ID},
		{Kind: "references", From: second.ID, To: focus.ID},
		{Kind: "references", From: first.ID, To: shared.ID},
		{Kind: "references", From: first.ID, To: second.ID},
		{Kind: "references", From: second.ID, To: shared.ID},
		{Kind: "references", From: other.ID, To: disconnected.ID},
	}
	graph := makeGraph(t, []contextmap.Node{focus, first, second, shared, other, disconnected}, links)

	checkView(t, graph.DirectView, focus.ID, []contextmap.Node{focus, first, second}, links[:2])
	checkView(t, graph.ExpandedView, focus.ID, []contextmap.Node{focus, first, second, shared}, links[:5])
}

func TestViewsFromIsolatedAndMissingNodes(t *testing.T) {
	isolated := concept("isolated")
	graph := makeGraph(t, []contextmap.Node{isolated}, nil)

	for name, query := range map[string]func(contextmap.NodeID) (contextmap.View, bool){
		"direct":   graph.DirectView,
		"expanded": graph.ExpandedView,
	} {
		t.Run(name, func(t *testing.T) {
			checkView(t, query, isolated.ID, []contextmap.Node{isolated}, nil)
			if _, found := query(concept("missing").ID); found {
				t.Fatal("query found an absent focus")
			}
		})
	}
}

func concept(key string) contextmap.Node {
	return contextmap.Node{ID: contextmap.NodeID{Kind: contextmap.KindConcept, Concept: key}, Label: "Concept"}
}

func checkView(
	t *testing.T,
	query func(contextmap.NodeID) (contextmap.View, bool),
	focus contextmap.NodeID,
	nodes []contextmap.Node,
	links []contextmap.Link,
) {
	t.Helper()
	view, found := query(focus)
	if !found || view.Focus != focus {
		t.Fatalf("query for focus %+v returned %+v (found %t)", focus, view.Focus, found)
	}
	if !slices.Equal(view.Nodes, nodes) {
		t.Errorf("nodes = %+v, want %+v", view.Nodes, nodes)
	}
	if !slices.Equal(view.Links, links) {
		t.Errorf("links = %+v, want %+v", view.Links, links)
	}
}
