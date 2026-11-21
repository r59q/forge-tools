package contextmap_test

import (
	"testing"

	"github.com/r59q/forge-tools/forge-context/library/contextmap"
)

func TestGraphRejectsInvalidNodeIDs(t *testing.T) {
	const repo = "one"
	const file = "guide.md"
	tests := []struct {
		name string
		id   contextmap.NodeID
	}{
		{"unknown kind", contextmap.NodeID{Kind: "unknown", Repository: repo}},
		{"missing repository", contextmap.NodeID{Kind: contextmap.KindFile, Path: file}},
		{"repository with path", contextmap.NodeID{Kind: contextmap.KindRepository, Repository: repo, Path: "."}},
		{"file with concept key", contextmap.NodeID{Kind: contextmap.KindFile, Repository: repo, Path: file, Concept: "review"}},
		{"missing concept key", contextmap.NodeID{Kind: contextmap.KindConcept}},
		{"located concept", contextmap.NodeID{Kind: contextmap.KindConcept, Repository: repo, Concept: "review"}},
		{"root folder", contextmap.NodeID{Kind: contextmap.KindFolder, Repository: repo, Path: "."}},
		{"absolute path", contextmap.NodeID{Kind: contextmap.KindFile, Repository: repo, Path: "/guide.md"}},
		{"path with aliases", contextmap.NodeID{Kind: contextmap.KindFile, Repository: repo, Path: "docs//guide.md"}},
		{"path outside repository", contextmap.NodeID{Kind: contextmap.KindFile, Repository: repo, Path: "../guide.md"}},
		{"missing range", contextmap.NodeID{Kind: contextmap.KindLineRange, Repository: repo, Path: file}},
		{"reversed range", contextmap.NodeID{
			Kind: contextmap.KindLineRange, Repository: repo, Path: file, Lines: contextmap.LineRange{Start: 5, End: 2},
		}},
		{"file with range", contextmap.NodeID{
			Kind: contextmap.KindFile, Repository: repo, Path: file, Lines: contextmap.LineRange{Start: 2, End: 5},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := contextmap.NewGraph([]contextmap.Node{{ID: tt.id}}, nil); err == nil {
				t.Fatal("NewGraph accepted an invalid ID")
			}
		})
	}
}

func TestGraphRejectsDuplicateID(t *testing.T) {
	id := contextmap.NodeID{Kind: contextmap.KindConcept, Concept: "docs"}
	nodes := []contextmap.Node{{ID: id, Label: "Docs"}, {ID: id, Label: "Documentation"}}
	if _, err := contextmap.NewGraph(nodes, nil); err == nil {
		t.Fatal("NewGraph accepted a duplicate ID")
	}
}
