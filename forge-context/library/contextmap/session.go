package contextmap

import (
	"fmt"
	"slices"
	"sync"
)

// Activity describes how a chat session used a graph node.
type Activity string

const (
	// ActivityRead records a file whose contents the agent read.
	ActivityRead Activity = "read"
	// ActivityTouched records a file the agent changed.
	ActivityTouched Activity = "touched"
	// ActivityConsidered records a file the agent considered for the task.
	ActivityConsidered Activity = "considered"
	// ActivityIncluded records a concept the caller identified as part of the session.
	ActivityIncluded Activity = "included"
)

// Observation records a caller-supplied activity on an existing graph node.
type Observation struct {
	// ID identifies a file or concept in the session's graph.
	ID NodeID
	// Activity describes what the caller observed.
	Activity Activity
}

// Session accumulates observations for one chat over a read-only graph.
// It supports concurrent calls and must not be copied after first use.
type Session struct {
	graph      *Graph
	mu         sync.RWMutex
	activities map[NodeID][]Activity
}

// NewSession starts an empty session over graph. Sessions can share a graph.
func NewSession(graph *Graph) *Session {
	return &Session{graph: graph}
}

// Record adds a batch of observations. Repeating an observation has no effect.
// File activities require a file ID, and inclusion requires a concept ID.
// If any observation is invalid, the batch has no effect.
func (s *Session) Record(observations ...Observation) error {
	for i, observation := range observations {
		if err := s.validate(observation); err != nil {
			return fmt.Errorf("observation %d: %w", i, err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activities == nil {
		s.activities = make(map[NodeID][]Activity)
	}
	for _, observation := range observations {
		activities := s.activities[observation.ID]
		if !slices.Contains(activities, observation.Activity) {
			s.activities[observation.ID] = append(activities, observation.Activity)
		}
	}
	return nil
}

func (s *Session) validate(observation Observation) error {
	if s.graph != nil {
		if _, found := s.graph.Node(observation.ID); found {
			return observation.validateActivity()
		}
	}
	return fmt.Errorf("unknown node ID %+v", observation.ID)
}

func (observation Observation) validateActivity() error {
	switch observation.Activity {
	case ActivityRead, ActivityTouched, ActivityConsidered:
		if observation.ID.Kind == KindFile {
			return nil
		}
	case ActivityIncluded:
		if observation.ID.Kind == KindConcept {
			return nil
		}
	}
	return fmt.Errorf("invalid activity %q for %s node", observation.Activity, observation.ID.Kind)
}

// SessionNode is a graph node with the activity recorded for it in one session.
type SessionNode struct {
	Node
	// Activities lists distinct observations in first-seen order.
	// A node shown only as a link endpoint has no activities.
	Activities []Activity `json:",omitempty"`
}

// SessionSnapshot is a renderable graph with session activity on its nodes.
// Every link has both endpoints in the snapshot.
type SessionSnapshot struct {
	// Nodes holds the selected graph nodes with their session activities.
	Nodes []SessionNode
	// Links holds the original graph relationships selected for the session.
	Links []Link
}

// Snapshot returns an independent copy of the current session graph. It keeps
// links between observed nodes and direct links incident to included concepts.
// Nodes and links follow graph input order.
func (s *Session) Snapshot() SessionSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := SessionSnapshot{Nodes: []SessionNode{}, Links: []Link{}}
	if len(s.activities) == 0 {
		return snapshot
	}

	endpoints := make(map[NodeID]bool)
	for _, link := range s.graph.links {
		if s.includesLink(link) {
			snapshot.Links = append(snapshot.Links, link)
			endpoints[link.From] = true
			endpoints[link.To] = true
		}
	}
	for _, node := range s.graph.nodes {
		if len(s.activities[node.ID]) != 0 || endpoints[node.ID] {
			snapshot.Nodes = append(snapshot.Nodes, s.sessionNode(node))
		}
	}
	return snapshot
}

// sessionNode copies activity while the caller holds the session lock.
func (s *Session) sessionNode(node Node) SessionNode {
	return SessionNode{Node: node, Activities: slices.Clone(s.activities[node.ID])}
}

func (s *Session) includesLink(link Link) bool {
	from, to := s.activities[link.From], s.activities[link.To]
	return (len(from) != 0 && len(to) != 0) ||
		slices.Contains(from, ActivityIncluded) || slices.Contains(to, ActivityIncluded)
}
