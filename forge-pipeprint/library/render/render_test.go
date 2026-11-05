package render

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/r59q/forge-tools/forge-pipeprint/library/pipeprint"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if got != string(want) {
		t.Fatalf("%s differs:\n%s", name, got)
	}
}

func pipeline() *pipeprint.Pipeline {
	return &pipeprint.Pipeline{
		Name: "CI",
		Triggers: pipeprint.Triggers{
			Push:        &pipeprint.Push{Branches: []string{"main"}, Tags: []string{"v*"}},
			PullRequest: &pipeprint.PullRequest{},
			Schedule:    []pipeprint.Schedule{{Cron: "0 3 * * *"}, {}},
			Manual:      &pipeprint.Manual{},
			Native:      pipeprint.Native{pipeprint.GitHub: {"release": nil}},
		},
		Jobs: []pipeprint.Job{
			{ID: "lint", Stage: "check", Image: "golang:1.27", Steps: []pipeprint.Step{{Checkout: true}, {Run: pipeprint.Commands{"go vet ./..."}}}},
			{ID: "build", Stage: "check", RunsOn: []string{"ubuntu-latest"}, Matrix: map[string][]string{"go": {"1.27"}}},
			{ID: "test", Stage: "test", Needs: []string{"lint", "build"}},
			{ID: "deploy", Name: "Deploy to production with a very long name", Needs: []string{"test"},
				If: pipeprint.Condition{pipeprint.GitHub: "github.ref == 'refs/heads/main'"}},
			{ID: "docs", Stage: "test", Needs: []string{"lint"}},
		},
	}
}

func TestText(t *testing.T) {
	golden(t, "unicode.txt", Text(pipeline(), Options{Steps: true}))
	golden(t, "ascii.txt", Text(pipeline(), Options{ASCII: true, Width: 40}))
}

func TestUnnamed(t *testing.T) {
	p := &pipeprint.Pipeline{Jobs: []pipeprint.Job{{ID: "a"}}}
	if got := Text(p, Options{}); got[:9] != "pipeline\n" {
		t.Fatalf("got %q", got)
	}
}
