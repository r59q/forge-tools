package compile

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
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

func input(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/go.github.yml")
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestDetect(t *testing.T) {
	cases := map[string]Format{
		"pipeprint: v1\njobs: []":    Pipeprint,
		`{"pipeprint": "v1"}`:        Pipeprint,
		"on: push\njobs: {}":         GitHub,
		"build:\n  script: [make]\n": GitLab,
	}

	for data, want := range cases {
		if got, err := Detect([]byte(data)); err != nil || got != want {
			t.Errorf("Detect(%q) = %q, %v", data, got, err)
		}
	}

	if _, err := Detect([]byte("[")); !errors.Is(err, pipeprint.ErrInvalid) {
		t.Errorf("Detect accepted bad YAML: %v", err)
	}
}

func TestGitHubToGitLab(t *testing.T) {
	out, warnings, err := Convert("", GitLab, input(t))
	if err != nil {
		t.Fatal(err)
	}

	golden(t, "go.gitlab.yml", out)
	golden(t, "go.gitlab.warnings", []byte(text(warnings)))
}

func TestGitLabBackToGitHub(t *testing.T) {
	gitlab, _, err := Convert(GitHub, GitLab, input(t))
	if err != nil {
		t.Fatal(err)
	}

	out, warnings, err := Convert(GitLab, GitHub, gitlab)
	if err != nil {
		t.Fatal(err)
	}

	golden(t, "go.github-again.yml", out)
	golden(t, "go.github-again.warnings", []byte(text(warnings)))
}

func TestPipeprintRoundTrip(t *testing.T) {
	for _, format := range []Format{Pipeprint, PipeprintJSON} {
		pp, _, err := Convert(GitHub, format, input(t))
		if err != nil {
			t.Fatal(err)
		}

		if format == Pipeprint {
			golden(t, "go.pipeprint.yml", pp)
		}

		back, warnings, err := Convert(format, Forgejo, pp)
		direct, _, _ := Convert(GitHub, GitHub, input(t))
		if err != nil || len(warnings) != 0 || string(back) != string(direct) {
			t.Fatalf("%s round trip: %v %v\n%s", format, err, warnings, back)
		}
	}
}

func TestUnknownFormat(t *testing.T) {
	if _, _, err := Convert(GitHub, "jenkins", input(t)); !errors.Is(err, ErrFormat) {
		t.Fatalf("to: %v", err)
	}

	if _, _, err := Read("jenkins", input(t)); !errors.Is(err, ErrFormat) {
		t.Fatalf("read: %v", err)
	}

	if _, _, err := Write("jenkins", &pipeprint.Pipeline{}); !errors.Is(err, ErrFormat) {
		t.Fatalf("write: %v", err)
	}

	if _, _, err := Convert("", GitHub, []byte("a: [")); !errors.Is(err, pipeprint.ErrInvalid) {
		t.Fatalf("bad input: %v", err)
	}
}

func TestFormats(t *testing.T) {
	for _, info := range Formats() {
		if info.Title == "" || info.Path == "" || !info.Format.Known() {
			t.Errorf("format %+v", info)
		}
	}
}

func text(w pipeprint.Warnings) string {
	var b strings.Builder
	for _, warning := range w {
		b.WriteString(warning.String() + "\n")
	}

	return b.String()
}

func TestCheck(t *testing.T) {
	format, problems := Check("", input(t))
	if format != GitHub || problems != nil {
		t.Fatalf("valid: %s %v", format, problems)
	}

	format, problems = Check("", []byte("pipeprint: v1\njobs: [{id: a, needs: [b]}, {id: a}]"))
	if format != Pipeprint || len(problems) != 2 || problems[0].Path != "jobs.a" {
		t.Fatalf("invalid: %s %v", format, problems)
	}

	if _, problems = Check("", []byte("[")); len(problems) != 1 || problems[0].Path != "" {
		t.Fatalf("syntax: %v", problems)
	}

	if _, problems = Check("jenkins", input(t)); len(problems) != 1 {
		t.Fatalf("format: %v", problems)
	}
}
