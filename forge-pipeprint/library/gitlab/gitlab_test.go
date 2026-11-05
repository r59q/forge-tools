package gitlab

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

func parse(t *testing.T, input string) (*pipeprint.Pipeline, pipeprint.Warnings) {
	t.Helper()
	p, w, err := Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}

	return p, w
}

func TestParseModel(t *testing.T) {
	data, _ := os.ReadFile("testdata/ci.yml")
	p, warnings := parse(t, string(data))
	if p.Name != "Demo" || !slices.Equal(p.Triggers.Push.Branches, []string{"main"}) || p.Triggers.PullRequest == nil {
		t.Fatalf("workflow %+v", p.Triggers)
	}

	build, test, pages := p.Job("build"), p.Job("test"), p.Job("pages")
	if build.Image != "golang:1.27" || !build.Steps[0].Checkout || build.Needs != nil {
		t.Fatalf("build %+v", build)
	}

	if !slices.Equal(test.Needs, []string{"build", "lint"}) || test.TimeoutMinutes != 90 || !test.Steps[2].Always {
		t.Fatalf("test %+v", test)
	}

	if pages.Steps[0].Checkout || !pages.ContinueOnError || len(pages.Matrix["SITE"]) != 2 || !slices.Equal(pages.Needs, []string{"build"}) {
		t.Fatalf("pages %+v", pages)
	}

	if len(warnings) != 1 || !strings.Contains(warnings[0].String(), `options of "build" dropped`) {
		t.Fatalf("warnings %v", warnings)
	}
}

func TestRoundTrip(t *testing.T) {
	data, _ := os.ReadFile("testdata/ci.yml")
	p, _ := parse(t, string(data))
	out, warnings, err := Emit(p)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("emit: %v %v", err, warnings)
	}

	golden(t, "ci.golden.yml", out)

	again, _ := parse(t, string(out))
	second, _, _ := Emit(again)
	if string(second) != string(out) {
		t.Fatalf("not stable:\n%s", second)
	}
}

func TestImplicitNeeds(t *testing.T) {
	p, _ := parse(t, `
a: {stage: build, script: [x]}
b: {script: [x]}
c: {stage: deploy, script: [x]}
d: {stage: deploy, needs: [], script: [x]}
`)

	if p.Job("a").Needs != nil || !slices.Equal(p.Job("b").Needs, []string{"a"}) ||
		!slices.Equal(p.Job("c").Needs, []string{"b"}) || p.Job("d").Needs == nil || len(p.Job("d").Needs) != 0 {
		t.Fatalf("needs %+v", p.Jobs)
	}

	out, _, _ := Emit(p)
	if !strings.Contains(string(out), "d:\n  stage: deploy\n  needs: []\n") || strings.Contains(string(out), "stages:") {
		t.Fatalf("emitted\n%s", out)
	}
}

func TestLevelStages(t *testing.T) {
	p := &pipeprint.Pipeline{Pipeprint: pipeprint.Version, Jobs: []pipeprint.Job{
		{ID: "a", Steps: run("x")}, {ID: "b", Steps: run("x")}, {ID: "c", Needs: []string{"a"}, Steps: run("x")},
	}}

	out, _, _ := Emit(p)
	want := "stages:\n  - stage-1\n  - stage-2\na:\n  stage: stage-1\n  script:\n    - x\nb:\n  stage: stage-1\n" +
		"  script:\n    - x\nc:\n  stage: stage-2\n  needs:\n    - a\n  script:\n    - x\n"
	if string(out) != want {
		t.Fatalf("got\n%s", out)
	}

	single := &pipeprint.Pipeline{Pipeprint: pipeprint.Version, Jobs: p.Jobs[:2]}
	out, _, _ = Emit(single)
	if strings.Contains(string(out), "stage") {
		t.Fatalf("single level wrote stages:\n%s", out)
	}
}

func TestUnknownStageIsAdded(t *testing.T) {
	p := &pipeprint.Pipeline{Pipeprint: pipeprint.Version, Stages: []string{"build"}, Triggers: pushAll(), Jobs: []pipeprint.Job{
		{ID: "a", Stage: "lint", Steps: run("x")},
	}}

	out, warnings, _ := Emit(p)
	if !strings.Contains(string(out), "stages:\n  - build\n  - lint\n") || len(warnings) != 1 {
		t.Fatalf("got %v\n%s", warnings, out)
	}
}

func TestTriggerRules(t *testing.T) {
	triggers := pipeprint.Triggers{
		Push:        &pipeprint.Push{Branches: []string{"main", "release/**"}, Tags: []string{"v*"}, Paths: []string{"src/**"}},
		PullRequest: &pipeprint.PullRequest{Branches: []string{"main"}},
		Schedule:    []pipeprint.Schedule{{Cron: "0 3 * * *"}},
		Manual:      &pipeprint.Manual{},
	}
	p := &pipeprint.Pipeline{Pipeprint: pipeprint.Version, Triggers: triggers, Jobs: []pipeprint.Job{{ID: "a", Steps: run("x")}}}

	out, warnings, err := Emit(p)
	if err != nil {
		t.Fatal(err)
	}

	golden(t, "rules.golden.yml", out)
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "cron") {
		t.Fatalf("warnings %v", warnings)
	}

	back, _ := parse(t, string(out))
	got := back.Triggers
	if !slices.Equal(got.Push.Branches, triggers.Push.Branches) || !slices.Equal(got.Push.Tags, triggers.Push.Tags) ||
		!slices.Equal(got.Push.Paths, triggers.Push.Paths) || !slices.Equal(got.PullRequest.Branches, []string{"main"}) ||
		len(got.Schedule) != 1 || got.Manual == nil {
		t.Fatalf("parsed back %+v %+v", got.Push, got.PullRequest)
	}
}

func TestUnknownRulesStayNative(t *testing.T) {
	p, warnings := parse(t, `
workflow:
  auto_cancel: {on_new_commit: interruptible}
  rules:
    - if: $CI_COMMIT_MESSAGE =~ /skip/
      when: never
    - when: always
a: {script: [x]}
`)

	if len(warnings) != 1 || p.Triggers.Push == nil {
		t.Fatalf("warnings %v", warnings)
	}

	out, _, _ := Emit(p)
	if !strings.Contains(string(out), "when: never") || !strings.Contains(string(out), "auto_cancel") {
		t.Fatalf("native workflow lost:\n%s", out)
	}
}

func TestGlobs(t *testing.T) {
	for glob, re := range map[string]string{"release/**": `^release\/.*$`, "v*": `^v[^/]*$`, "a.b?": `^a\.b.$`} {
		got, ok := globRegex(glob)
		back, okBack := regexGlob(got)
		if !ok || got != re || !okBack || back != glob {
			t.Errorf("%q -> %q -> %q", glob, got, back)
		}
	}

	if _, ok := globRegex("[ab]"); ok {
		t.Error("translated a character class")
	}

	if _, ok := regexGlob("^a+$"); ok {
		t.Error("read an unknown expression")
	}
}

func TestDurations(t *testing.T) {
	for text, want := range map[string]int{"1h 30m": 90, "2 hours": 120, "45 minutes": 45, "1h30m": 90, "3h": 180} {
		if got, ok := parseDuration(text); !ok || got != want {
			t.Errorf("%q = %d", text, got)
		}
	}

	for _, text := range []string{"", "1 day", "h", "30s"} {
		if _, ok := parseDuration(text); ok {
			t.Errorf("accepted %q", text)
		}
	}

	if duration(90) != "1h 30m" || duration(120) != "2h" || duration(5) != "5m" {
		t.Error("duration")
	}
}

func TestEmitWarnings(t *testing.T) {
	p := &pipeprint.Pipeline{Pipeprint: pipeprint.Version, Jobs: []pipeprint.Job{{
		ID:     "a",
		RunsOn: []string{"ubuntu-latest", "gpu"},
		If:     pipeprint.Condition{pipeprint.GitHub: "github.ref == 'x'"},
		Native: pipeprint.Native{pipeprint.GitHub: {"permissions": "read-all"}},
		Steps: []pipeprint.Step{
			{Uses: "actions/checkout@v4", With: map[string]string{"fetch-depth": "0"}},
			{Uses: "actions/setup-go@v5"},
			{Run: pipeprint.Commands{"echo ${{ github.sha }}"}, Native: pipeprint.Native{pipeprint.GitHub: {"env": map[string]any{"A": "b"}}}},
			{Run: pipeprint.Commands{"cleanup"}, Always: true},
			{Run: pipeprint.Commands{"more"}},
		},
	}}}

	out, warnings, _ := Emit(p)
	text := ""
	for _, w := range warnings {
		text += w.String() + "\n"
	}

	for _, want := range []string{
		`runner "ubuntu-latest" dropped`, "no gitlab expression", "native.github.permissions", "checkout options dropped",
		"actions/setup-go@v5 cannot run", "${{ }} expressions", "steps[2].native.github.env", "moved to after_script",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing warning %q in\n%s", want, text)
		}
	}

	if !strings.Contains(string(out), "tags:\n    - gpu\n") || !strings.Contains(string(out), "after_script:\n    - cleanup\n") {
		t.Fatalf("emitted\n%s", out)
	}
}

func TestNoopScript(t *testing.T) {
	p := &pipeprint.Pipeline{Pipeprint: pipeprint.Version, Jobs: []pipeprint.Job{{ID: "a", Steps: []pipeprint.Step{{Uses: "x/y@v1"}}}}}
	out, _, _ := Emit(p)
	if !strings.Contains(string(out), "script:\n    - \"true\"\n") {
		t.Fatalf("emitted\n%s", out)
	}
}

func TestParseErrors(t *testing.T) {
	for _, input := range []string{"a: [", "stages: [a]", "spec:\n  inputs: {}", "- x"} {
		if _, _, err := Parse([]byte(input)); !errors.Is(err, pipeprint.ErrInvalid) {
			t.Errorf("Parse(%q) = %v", input, err)
		}
	}

	_, warnings := parse(t, "a: {needs: [included], script: [x]}")
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "unknown job") {
		t.Fatalf("warnings %v", warnings)
	}
}

func run(command string) []pipeprint.Step {
	return []pipeprint.Step{{Run: pipeprint.Commands{command}}}
}

func pushAll() pipeprint.Triggers {
	return pipeprint.Triggers{Push: &pipeprint.Push{}}
}

func TestReferenceTagsWarn(t *testing.T) {
	p, warnings := parse(t, ".setup: {script: [x]}\njob:\n  script:\n    - !reference [.setup, script]\n")
	if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "!reference on line 4") || p.Job("job").Native[dialect]["script"] == nil {
		t.Fatalf("warnings %v, job %+v", warnings, p.Job("job"))
	}
}
