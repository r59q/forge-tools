package graph

import (
	"slices"
	"testing"

	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
)

func TestBuild(t *testing.T) {
	p := &pipeprint.Pipeline{Name: "CI", Jobs: []pipeprint.Job{
		{ID: "deploy", Needs: []string{"test"}, If: pipeprint.Condition{pipeprint.GitLab: "$X"}},
		{ID: "lint", Name: "Lint", Image: "go", Matrix: map[string][]string{"go": {"1"}}},
		{ID: "test", Needs: []string{"lint"}, Steps: []pipeprint.Step{
			{Name: "Named"}, {Checkout: true}, {Uses: "a/b@v1"}, {Run: pipeprint.Commands{"  make\nmore"}}, {},
		}},
	}}

	g := Build(p)
	var order []string
	for _, n := range g.Nodes {
		order = append(order, n.ID)
	}

	if !slices.Equal(order, []string{"lint", "test", "deploy"}) || g.Levels != 3 || g.Name != "CI" {
		t.Fatalf("graph %+v", g)
	}

	test := g.Level(2)[0]
	if !slices.Equal(test.Steps, []string{"Named", "checkout", "a/b@v1", "$ make", "step"}) {
		t.Fatalf("steps %v", test.Steps)
	}

	if !slices.Equal(g.Needs("deploy"), []string{"test"}) || len(g.Edges) != 2 {
		t.Fatalf("edges %v", g.Edges)
	}

	lint := g.Level(1)[0]
	if lint.Label() != "Lint" || !lint.Matrix || g.Level(3)[0].If != "$X" || g.Level(3)[0].Label() != "deploy" {
		t.Fatalf("nodes %+v", g.Nodes)
	}
}
