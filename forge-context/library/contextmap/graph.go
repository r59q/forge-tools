package contextmap

import (
	"errors"
	"fmt"
	"slices"
)

// LinkKind names a direct relationship. Callers can define additional kinds.
type LinkKind string

const (
	// LinkContains points from an immediate source container to its child location.
	LinkContains LinkKind = "contains"
	// LinkChanges points from a node representing a change to an affected node.
	LinkChanges LinkKind = "changes"
)

// Link is a typed, directed relationship between two nodes in the graph.
type Link struct {
	// Kind is a non-empty relationship type.
	Kind LinkKind
	// From is the source node's identity.
	From NodeID
	// To is the destination node's identity.
	To NodeID
}

// Graph is a read-only collection of nodes and direct links. Its zero value is empty.
type Graph struct {
	nodes    []Node
	links    []Link
	index    map[NodeID]int
	incident map[NodeID][]int
}

// NewGraph copies nodes and links in input order. It rejects invalid or duplicate
// node IDs, missing endpoints, empty link kinds, and invalid containment links.
func NewGraph(nodes []Node, links []Link) (*Graph, error) {
	index := make(map[NodeID]int, len(nodes))
	for i, node := range nodes {
		if err := node.ID.validate(); err != nil {
			return nil, fmt.Errorf("node %d: %w", i, err)
		}
		if _, exists := index[node.ID]; exists {
			return nil, fmt.Errorf("node %d: duplicate ID", i)
		}
		index[node.ID] = i
	}
	incident := make(map[NodeID][]int)
	for i, link := range links {
		if err := validateLink(link, index); err != nil {
			return nil, fmt.Errorf("link %d: %w", i, err)
		}
		incident[link.From] = append(incident[link.From], i)
		if link.To != link.From {
			incident[link.To] = append(incident[link.To], i)
		}
	}
	return &Graph{nodes: slices.Clone(nodes), links: slices.Clone(links), index: index, incident: incident}, nil
}

func validateLink(link Link, index map[NodeID]int) error {
	if link.Kind == "" {
		return errors.New("missing link kind")
	}
	if _, exists := index[link.From]; !exists {
		return errors.New("source node is missing")
	}
	if _, exists := index[link.To]; !exists {
		return errors.New("destination node is missing")
	}
	if link.Kind == LinkContains && link.To.parent() != link.From {
		return errors.New("source node is not the destination's immediate container")
	}
	return nil
}

// Node returns the node with the given ID, if present.
func (g *Graph) Node(id NodeID) (Node, bool) {
	i, found := g.index[id]
	if !found {
		return Node{}, false
	}
	return g.nodes[i], true
}

// Nodes returns a copy of the nodes in input order.
func (g *Graph) Nodes() []Node {
	return slices.Clone(g.nodes)
}

// Links returns a copy of the direct links in input order.
func (g *Graph) Links() []Link {
	return slices.Clone(g.links)
}
