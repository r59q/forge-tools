package checkout

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-search/library/search"
)

// write creates the files below dir, with their directories.
func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()

	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// remove deletes files below dir.
func remove(t *testing.T, dir string, names ...string) {
	t.Helper()

	for _, name := range names {
		if err := os.RemoveAll(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
}

func symlink(t *testing.T, target, name string) {
	t.Helper()

	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

// checkout returns a new directory holding files, and its open root.
func checkout(t *testing.T, files map[string]string) (string, *os.Root) {
	t.Helper()

	dir := t.TempDir()
	write(t, dir, files)

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return dir, root
}

func read(t *testing.T, root *os.Root, options Options) Tree {
	t.Helper()

	tree, err := Read(context.Background(), root, options)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return tree
}

func ids(documents []search.Document) []string {
	paths := []string{}
	for _, document := range documents {
		paths = append(paths, document.ID)
	}
	return paths
}

func contents(tree Tree) string {
	var all strings.Builder
	for _, document := range tree.Documents {
		all.Write(document.Content)
	}
	return all.String()
}

// noHostPath fails when an error names the directory or holds content.
func noHostPath(t *testing.T, err error, dir string, content ...string) {
	t.Helper()

	message := err.Error()
	if strings.Contains(message, dir) || strings.Contains(message, os.TempDir()) {
		t.Errorf("error %q names a host path", message)
	}
	for _, text := range content {
		if strings.Contains(message, text) {
			t.Errorf("error %q holds content", message)
		}
	}
}
