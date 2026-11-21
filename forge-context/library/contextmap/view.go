package contextmap

// View is the set of nodes and recorded links found from a focus node.
// Its slices can be modified independently of the graph.
type View struct {
	// Focus identifies the node the query started from.
	Focus NodeID
	// Nodes starts with the focus and includes each discovered node once, in breadth-first order.
	Nodes []Node
	// Links contains recorded links with their original endpoints and direction.
	// Links incident to the focus identify its immediate neighbors.
	Links []Link
}

// DirectView returns the focus, its immediate neighbors, and the links
// incident to the focus in either direction. It returns false if the focus
// is absent.
func (g *Graph) DirectView(focus NodeID) (View, bool) {
	return g.view(focus, false)
}

// ExpandedView returns the focus and all nodes and links reachable by following
// recorded links in either direction. It returns false if the focus is absent.
func (g *Graph) ExpandedView(focus NodeID) (View, bool) {
	return g.view(focus, true)
}

func (g *Graph) view(focus NodeID, expand bool) (View, bool) {
	start, found := g.Node(focus)
	if !found {
		return View{}, false
	}

	view := View{Focus: focus, Nodes: []Node{start}}
	seenNodes := map[NodeID]bool{focus: true}
	seenLinks := make(map[int]bool)
	for i := 0; i < len(view.Nodes); i++ {
		id := view.Nodes[i].ID
		for _, index := range g.incident[id] {
			if seenLinks[index] {
				continue
			}
			seenLinks[index] = true
			link := g.links[index]
			view.Links = append(view.Links, link)

			neighbor := link.To
			if neighbor == id {
				neighbor = link.From
			}
			if !seenNodes[neighbor] {
				seenNodes[neighbor] = true
				view.Nodes = append(view.Nodes, g.nodes[g.index[neighbor]])
			}
		}
		if !expand {
			break
		}
	}
	return view, true
}
