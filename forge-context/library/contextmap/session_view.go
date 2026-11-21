package contextmap

// SessionView is a focused graph annotated with activity from one chat.
type SessionView struct {
	// Focus identifies the first node in the view.
	Focus NodeID
	// SessionSnapshot contains nodes in breadth-first order and their connecting links.
	SessionSnapshot
}

// DirectView returns the focus and its immediate neighbors in the underlying graph
// with their recorded activities. It returns false if the focus is absent.
func (s *Session) DirectView(focus NodeID) (SessionView, bool) {
	return s.view(focus, false)
}

// ExpandedView follows links in either direction from the focus, including
// unobserved nodes. It returns false if the focus is absent.
func (s *Session) ExpandedView(focus NodeID) (SessionView, bool) {
	return s.view(focus, true)
}

func (s *Session) view(focus NodeID, expand bool) (SessionView, bool) {
	if s.graph == nil {
		return SessionView{}, false
	}
	view, found := s.graph.view(focus, expand)
	if !found {
		return SessionView{}, false
	}

	snapshot := SessionSnapshot{Nodes: make([]SessionNode, len(view.Nodes)), Links: view.Links}
	if snapshot.Links == nil {
		snapshot.Links = []Link{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i, node := range view.Nodes {
		snapshot.Nodes[i] = s.sessionNode(node)
	}
	return SessionView{Focus: focus, SessionSnapshot: snapshot}, true
}
