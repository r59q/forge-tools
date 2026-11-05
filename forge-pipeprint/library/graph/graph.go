// Package graph turns a pipeline into nodes and edges for visualization.
package graph

import (
	"cmp"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
)

// Graph is a pipeline's jobs and the needs between them.
type Graph struct {
	// Name is the pipeline name.
	Name string `json:"name,omitempty"`
	// Nodes are the jobs, by level and then file order.
	Nodes []Node `json:"nodes"`
	// Edges point from a needed job to the job that needs it.
	Edges []Edge `json:"edges"`
	// Levels is the highest node level.
	Levels int `json:"levels"`
}

// Node is one job.
type Node struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Stage string `json:"stage,omitempty"`
	// Level is 1 for jobs without needs, and one more than the highest
	// needed job otherwise.
	Level  int      `json:"level"`
	RunsOn []string `json:"runs_on,omitempty"`
	Image  string   `json:"image,omitempty"`
	// Steps are short step labels.
	Steps []string `json:"steps,omitempty"`
	// Matrix reports whether the job runs once per matrix combination.
	Matrix bool `json:"matrix,omitempty"`
	// If is the job's condition in the first dialect that has one.
	If string `json:"if,omitempty"`
}

// Label returns the name, or the ID when there is no name.
func (n Node) Label() string {
	return cmp.Or(n.Name, n.ID)
}

// Edge is one need.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Build returns p's graph. p should be valid.
func Build(p *pipeprint.Pipeline) Graph {
	levels := p.Levels()
	g := Graph{Name: p.Name, Nodes: []Node{}, Edges: []Edge{}}
	for _, job := range p.Jobs {
		g.Nodes = append(g.Nodes, node(job, levels[job.ID]))
		g.Levels = max(g.Levels, levels[job.ID])
		for _, need := range job.Needs {
			g.Edges = append(g.Edges, Edge{From: need, To: job.ID})
		}
	}

	slices.SortStableFunc(g.Nodes, func(a, b Node) int { return a.Level - b.Level })

	return g
}

// Level returns the nodes on level l, in order.
func (g Graph) Level(l int) []Node {
	var nodes []Node
	for _, n := range g.Nodes {
		if n.Level == l {
			nodes = append(nodes, n)
		}
	}

	return nodes
}

// Needs returns the IDs a node needs.
func (g Graph) Needs(id string) []string {
	var needs []string
	for _, e := range g.Edges {
		if e.To == id {
			needs = append(needs, e.From)
		}
	}

	return needs
}

func node(job pipeprint.Job, level int) Node {
	n := Node{
		ID:     job.ID,
		Name:   job.Name,
		Stage:  job.Stage,
		Level:  level,
		RunsOn: job.RunsOn,
		Image:  job.Image,
		Matrix: len(job.Matrix) > 0,
		If:     condition(job.If),
	}

	for _, step := range job.Steps {
		n.Steps = append(n.Steps, StepLabel(step))
	}

	return n
}

// StepLabel returns a short label: the name, the action, checkout, or the
// first command line.
func StepLabel(s pipeprint.Step) string {
	switch {
	case s.Name != "":
		return s.Name
	case s.Checkout:
		return "checkout"
	case s.Uses != "":
		return s.Uses
	case len(s.Run) > 0:
		first, _, _ := strings.Cut(strings.TrimSpace(s.Run[0]), "\n")
		return "$ " + first
	}

	return "step"
}

// condition returns the expression of the first dialect that has one.
func condition(c pipeprint.Condition) string {
	for _, d := range pipeprint.Dialects() {
		if cond, ok := c[d]; ok {
			return cond
		}
	}

	return ""
}
