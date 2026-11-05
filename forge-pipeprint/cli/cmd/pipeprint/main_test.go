package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const gitlabCI = `build:
  stage: build
  script: [make]
test:
  stage: test
  tags: [docker]
  script: [make test]
`

// result is one run's exit code and output.
type result struct {
	code           int
	stdout, stderr string
}

func cmd(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(stdin), &stdout, &stderr)

	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func TestConvert(t *testing.T) {
	r := cmd(t, gitlabCI, "convert", "--to", "github")
	if r.code != exitOK || !strings.Contains(r.stdout, "test:\n    needs: build\n    runs-on: docker\n") {
		t.Fatalf("%+v", r)
	}

	if !strings.Contains(r.stderr, "jobs.build.runs_on: no runner; defaulted to ubuntu-latest") {
		t.Fatalf("missing warning: %s", r.stderr)
	}

	r = cmd(t, gitlabCI, "convert", "--to", "github", "--strict")
	if r.code != exitFail || r.stdout != "" || !strings.Contains(r.stderr, "output not written") {
		t.Fatalf("strict: %+v", r)
	}
}

func TestImportExportFiles(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, ".gitlab-ci.yml")
	pp := filepath.Join(dir, "ci.pipeprint.yml")
	if err := os.WriteFile(in, []byte(gitlabCI), 0o600); err != nil {
		t.Fatal(err)
	}

	if r := cmd(t, "", "import", in, "-o", pp); r.code != exitOK {
		t.Fatalf("import: %+v", r)
	}

	r := cmd(t, "", "export", pp, "--to", "gitlab")
	want := "build:\n  stage: build\n  script:\n    - make\ntest:\n  stage: test\n  tags:\n    - docker\n  script:\n    - make test\n"
	if r.code != exitOK || r.stdout != want {
		t.Fatalf("export: %+v", r)
	}

	r = cmd(t, "", "import", "--json", in)
	var doc map[string]any
	if r.code != exitOK || json.Unmarshal([]byte(r.stdout), &doc) != nil || doc["pipeprint"] != "v1" {
		t.Fatalf("json: %+v", r)
	}
}

func TestCheck(t *testing.T) {
	if r := cmd(t, gitlabCI, "check"); r.code != exitOK || r.stdout != "ok: valid gitlab pipeline\n" {
		t.Fatalf("valid: %+v", r)
	}

	r := cmd(t, "pipeprint: v1\njobs: [{id: a, needs: [b]}]", "check")
	if r.code != exitFail || !strings.Contains(r.stderr, `jobs.a.needs: unknown job "b"`) {
		t.Fatalf("invalid: %+v", r)
	}
}

func TestGraph(t *testing.T) {
	r := cmd(t, gitlabCI, "graph", "--ascii", "--steps")
	if r.code != exitOK || !strings.Contains(r.stdout, "level 2 - test") || !strings.Contains(r.stdout, "| * $ make test") {
		t.Fatalf("text: %+v", r)
	}

	r = cmd(t, gitlabCI, "graph", "--json")
	var g struct {
		Edges []struct{ From, To string }
	}
	if r.code != exitOK || json.Unmarshal([]byte(r.stdout), &g) != nil || len(g.Edges) != 1 || g.Edges[0].From != "build" {
		t.Fatalf("json: %+v", r)
	}
}

func TestFormatsAndHelp(t *testing.T) {
	if r := cmd(t, "", "formats"); r.code != exitOK || !strings.Contains(r.stdout, ".gitlab-ci.yml") {
		t.Fatalf("formats: %+v", r)
	}

	if r := cmd(t, "", "help"); r.code != exitOK || !strings.Contains(r.stdout, "Usage:") {
		t.Fatalf("help: %+v", r)
	}

	if r := cmd(t, "", "version"); r.code != exitOK || r.stdout != "pipeprint dev\n" {
		t.Fatalf("version: %+v", r)
	}
}

func TestUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"no command":    {},
		"unknown":       {"compile"},
		"no target":     {"convert"},
		"bad flag":      {"graph", "--nope"},
		"two files":     {"check", "a", "b"},
		"formats extra": {"formats", "x"},
	}

	for name, args := range cases {
		if r := cmd(t, gitlabCI, args...); r.code != exitUsage {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestFailures(t *testing.T) {
	cases := map[string][]string{
		"missing file":  {"check", filepath.Join(t.TempDir(), "missing.yml")},
		"bad format":    {"convert", "--to", "jenkins"},
		"bad input":     {"graph", "--from", "pipeprint"},
		"bad out path":  {"import", "-o", filepath.Join(t.TempDir(), "no", "such", "dir")},
		"missing input": {"export", "--to", "github", filepath.Join(t.TempDir(), "x")},
	}

	for name, args := range cases {
		if r := cmd(t, gitlabCI, args...); r.code != exitFail || !strings.HasPrefix(r.stderr, "pipeprint: ") {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestInputLimit(t *testing.T) {
	r := cmd(t, strings.Repeat("a", maxInput+1), "check")
	if r.code != exitFail || !strings.Contains(r.stderr, "16 MiB") {
		t.Fatalf("%+v", r)
	}
}
