// Package testrepo creates local Git fixtures for service tests.
package testrepo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Git runs a fixture command with an isolated identity and configuration.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // test fixture with test-supplied arguments.
	cmd.Dir = dir
	cmd.Env = append(
		os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Fixture",
		"GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture",
		"GIT_COMMITTER_EMAIL=fixture@example.com",
	)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s %v", args, b, err)
	}

	return strings.TrimSpace(string(b))
}

// New creates two commits, an annotated tag v1 and the branch main.
func New(t *testing.T, initArgs ...string) string {
	t.Helper()
	dir := t.TempDir()
	Git(t, dir, append([]string{"init", "-b", "main"}, initArgs...)...)
	for _, text := range []string{"original\n", "second\n"} {
		if err := os.WriteFile(filepath.Join(dir, "file"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}

		Git(t, dir, "add", ".")
		Git(t, dir, "commit", "-m", strings.TrimSpace(text))
		if text == "original\n" {
			Git(t, dir, "tag", "-a", "v1", "-m", "tag")
		}
	}

	return dir
}
