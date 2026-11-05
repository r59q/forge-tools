package github

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != string(want) {
		t.Fatalf("%s differs:\n%s", name, got)
	}
}

func parseFile(t *testing.T, name string) *pipeprint.Pipeline {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	p, _, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func TestParseModel(t *testing.T) {
	p := parseFile(t, "ci.yml")
	tr := p.Triggers
	if !slices.Equal(tr.Push.Branches, []string{"main", "release/**"}) || tr.Push.Native[dialect]["paths-ignore"] == nil {
		t.Fatalf("push %+v", tr.Push)
	}

	if tr.PullRequest.Native[dialect]["types"] == nil || tr.Schedule[0].Cron != "0 3 * * 1" ||
		tr.Native[dialect]["release"] == nil || tr.Manual.Native[dialect]["inputs"] == nil {
		t.Fatalf("triggers %+v", tr)
	}

	lint, test, deploy := p.Job("lint"), p.Job("test"), p.Job("deploy")
	if lint.Image != "" || lint.Native[dialect]["container"] == nil || !slices.Equal(lint.RunsOn, []string{"self-hosted", "linux"}) {
		t.Fatalf("lint %+v", lint)
	}

	if test.Image != "golang:1.27" || test.TimeoutMinutes != 20 || test.ContinueOnError || test.Native[dialect]["continue-on-error"] == nil ||
		len(test.Matrix["go"]) != 2 || test.Services[0].Native[dialect]["ports"] == nil || !test.Steps[2].Always {
		t.Fatalf("test %+v", test)
	}

	if deploy.Name != "Deploy site" || deploy.If[dialect] == "" || deploy.Environment.URL != "https://example.org" ||
		!deploy.ContinueOnError || deploy.Steps[1].Native[dialect]["if"] != "failure()" {
		t.Fatalf("deploy %+v", deploy)
	}
}

func TestRoundTrip(t *testing.T) {
	out, warnings, err := Emit(parseFile(t, "ci.yml"))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("emit: %v %v", err, warnings)
	}

	golden(t, "ci.golden.yml", out)

	again, _, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}

	second, _, _ := Emit(again)
	if string(second) != string(out) {
		t.Fatalf("not stable:\n%s", second)
	}
}

func TestTriggerForms(t *testing.T) {
	cases := map[string]string{
		"on: push":                          "on: push",
		"on: [push, pull_request]":          "on:\n  - push\n  - pull_request",
		"on: {push: {branches: [main]}}":    "on:\n  push:\n    branches:\n      - main",
		"on: {push: , workflow_dispatch: }": "on:\n  - push\n  - workflow_dispatch",
	}

	for input, want := range cases {
		p, _, err := Parse([]byte(input + "\njobs: {a: {runs-on: x, steps: [{run: y}]}}"))
		if err != nil {
			t.Fatal(err)
		}

		out, _, _ := Emit(p)
		if !strings.HasPrefix(string(out), want+"\n") {
			t.Errorf("%q wrote\n%s", input, out)
		}
	}
}

func TestEmitWarnings(t *testing.T) {
	p := &pipeprint.Pipeline{
		Pipeprint: pipeprint.Version,
		Native:    pipeprint.Native{pipeprint.GitLab: {"include": "x.yml"}},
		Triggers:  pipeprint.Triggers{Schedule: []pipeprint.Schedule{{}}},
		Jobs: []pipeprint.Job{{
			ID:    "unit tests",
			If:    pipeprint.Condition{pipeprint.GitLab: "$CI_COMMIT_BRANCH"},
			Steps: []pipeprint.Step{{Checkout: true}, {Run: pipeprint.Commands{"echo $CI_COMMIT_SHA"}}},
		}},
	}

	out, warnings, err := Emit(p)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"pipeline.native.gitlab.include", "triggers.schedule: schedule without cron", "triggers: no triggers",
		"jobs.unit tests.if: no github expression", "jobs.unit tests.runs_on: no runner", "uses GitLab CI_ variables",
	} {
		if !strings.Contains(warningText(warnings), want) {
			t.Errorf("missing warning %q in %v", want, warnings)
		}
	}

	for _, want := range []string{"unit-tests:", "name: unit tests", "uses: actions/checkout@v4", "on: push"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestAlwaysCombinesConditions(t *testing.T) {
	step := pipeprint.Step{Always: true, Run: pipeprint.Commands{"x"}, Native: pipeprint.Native{dialect: {"if": "${{ env.A }}"}}}
	p := &pipeprint.Pipeline{Pipeprint: pipeprint.Version, Jobs: []pipeprint.Job{{ID: "a", RunsOn: []string{"x"}, Steps: []pipeprint.Step{step}}}}

	out, _, _ := Emit(p)
	if !strings.Contains(string(out), "if: always() && (env.A)") || step.Native[dialect]["if"] == nil {
		t.Fatalf("combined condition:\n%s", out)
	}
}

func TestJobIDs(t *testing.T) {
	ids := jobIDs([]pipeprint.Job{{ID: "build"}, {ID: "test:unit"}, {ID: "test-unit"}, {ID: "1st"}, {ID: "a b"}})
	want := map[string]string{"build": "build", "test:unit": "test-unit-2", "test-unit": "test-unit", "1st": "_1st", "a b": "a-b"}
	for id, safe := range want {
		if ids[id] != safe {
			t.Errorf("%q -> %q, want %q", id, ids[id], safe)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, input := range []string{"on: push", "jobs: [a]", "jobs: {a: [x]}", "a: ["} {
		if _, _, err := Parse([]byte(input)); !errors.Is(err, pipeprint.ErrInvalid) {
			t.Errorf("Parse(%q) = %v", input, err)
		}
	}
}

func warningText(w pipeprint.Warnings) string {
	var lines []string
	for _, warning := range w {
		lines = append(lines, warning.String())
	}

	return strings.Join(lines, "\n")
}
