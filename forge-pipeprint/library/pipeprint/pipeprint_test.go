package pipeprint

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const sample = `pipeprint: v1
name: CI
triggers:
  push:
    branches: [main]
jobs:
  - id: build
    steps:
      - checkout: true
      - run: go build ./...
  - id: test
    needs: [build]
    steps:
      - run:
          - go vet ./...
          - go test ./...
`

func TestParseAndWrite(t *testing.T) {
	p, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}

	if p.Name != "CI" || len(p.Jobs) != 2 || len(p.Jobs[1].Steps[0].Run) != 2 {
		t.Fatalf("parsed %+v", p)
	}

	for name, write := range map[string]func() ([]byte, error){"yaml": p.YAML, "json": p.JSON} {
		out, err := write()
		if err != nil {
			t.Fatal(err)
		}

		again, err := Parse(out)
		if err != nil {
			t.Fatalf("%s does not parse back: %v\n%s", name, err, out)
		}

		if again.Jobs[0].Steps[1].Run[0] != "go build ./..." || !again.Jobs[0].Steps[0].Checkout {
			t.Fatalf("%s lost steps:\n%s", name, out)
		}
	}
}

func TestCommandsForms(t *testing.T) {
	one, _ := json.Marshal(Commands{"a"})
	many, _ := json.Marshal(Commands{"a", "b"})
	if string(one) != `"a"` || string(many) != `["a","b"]` {
		t.Fatalf("json forms: %s %s", one, many)
	}

	var c Commands
	if err := json.Unmarshal([]byte(`{"x":1}`), &c); err == nil {
		t.Fatal("accepted an object")
	}

	if _, err := Parse([]byte("pipeprint: v1\njobs:\n  - id: a\n    steps:\n      - run: {x: 1}\n")); err == nil {
		t.Fatal("accepted a mapping as run")
	}
}

func TestProblems(t *testing.T) {
	cases := map[string]string{
		"pipeprint: v2\njobs: [{id: a, steps: [{run: x}]}]":                   "pipeprint: must be v1",
		"pipeprint: v1\njobs: []":                                             "jobs: need at least one job",
		"pipeprint: v1\njobs: [{id: a}, {id: a}]":                             "jobs.a: duplicate job id",
		"pipeprint: v1\njobs: [{id: a, needs: [b]}]":                          `unknown job "b"`,
		"pipeprint: v1\njobs: [{id: a, needs: [a]}]":                          "cannot need itself",
		"pipeprint: v1\njobs: [{id: a, needs: [b]}, {id: b, needs: [a]}]":     "needs form a cycle",
		"pipeprint: v1\njobs: [{id: a, steps: [{run: x, uses: y}]}]":          "set only one",
		"pipeprint: v1\njobs: [{id: a, steps: [{name: x}]}]":                  "set checkout, uses or run",
		"pipeprint: v1\njobs: [{id: a, steps: [{run: x, with: {a: b}}]}]":     "needs uses",
		"pipeprint: v1\njobs: [{id: a, native: {jenkins: {x: 1}}}]":           `unknown dialect "jenkins"`,
		"pipeprint: v1\njobs: [{id: a, if: {jenkins: x}}]":                    `unknown dialect "jenkins"`,
		"pipeprint: v1\njobs: [{id: a, matrix: {os: []}}]":                    "needs at least one value",
		"pipeprint: v1\njobs: [{id: a, services: [{name: db}]}]":              "image: is required",
		"pipeprint: v1\njobs: [{id: a, timeout_minutes: -1}]":                 "must not be negative",
		"pipeprint: v1\njobs: [{id: ' a'}]":                                   "must not start or end",
		"pipeprint: v1\njobs: [{id: a, environment: {url: x}}]":               "environment.name: is required",
		"pipeprint: v1\ntriggers: {push: {native: {x: {}}}}\njobs: [{id: a}]": `unknown dialect "x"`,
		"pipeprint: v1\njobs: [{id: a, typo: true}]":                          "field typo not found",
		"": "empty file",
	}

	for input, want := range cases {
		_, err := Parse([]byte(input))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) = %v, want %q", input, err, want)
		}
	}
}

func TestLevels(t *testing.T) {
	p := &Pipeline{Jobs: []Job{
		{ID: "a"}, {ID: "b"}, {ID: "c", Needs: []string{"a", "b"}}, {ID: "d", Needs: []string{"c", "a", "missing"}},
	}}

	levels := p.Levels()
	if levels["a"] != 1 || levels["b"] != 1 || levels["c"] != 2 || levels["d"] != 3 {
		t.Fatalf("levels %v", levels)
	}

	if p.Job("c") == nil || p.Job("x") != nil {
		t.Fatal("Job lookup")
	}
}

func TestWarnings(t *testing.T) {
	var w Warnings
	w.Foreign("jobs.a", Native{GitLab: {"cache": 1, "artifacts": 2}, GitHub: {"x": 1}}, GitHub)
	if len(w) != 2 || w[0].String() != "jobs.a.native.gitlab.artifacts: gitlab-only setting dropped" {
		t.Fatalf("warnings %v", w)
	}

	var n Native
	n.Set(GitHub, "k", "v")
	if n[GitHub]["k"] != "v" {
		t.Fatal("Set")
	}
}
