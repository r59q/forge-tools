package folder

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
	"github.com/r59q/forge-tools/forge-evidence/library/storage"
)

const (
	commit = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	name   = "unit-tests"

	// entryPath is the folder of the example entry.
	entryPath = ".evidence/" + commit + "/" + name

	textFile   = "go-test.log"
	binaryFile = "screen.bin"
)

// textContent has CRLF and LF line ends, which must be kept as they are.
const textContent = "ok  \tunit\r\nPASS\n"

// binaryContent has NUL bytes and invalid UTF-8.
func binaryContent() []byte {
	return []byte{0x89, 'P', 'N', 'G', 0x00, 0x00, 0xff, 0xfe, 0xc3, 0x28, '\r', '\n', 0x00}
}

func exampleID() evidence.ID {
	return evidence.ID{Commit: commit, Name: name}
}

func exampleDraft() storage.Draft {
	return storage.Draft{
		Name:    name,
		Title:   "Unit tests pass",
		Kind:    evidence.KindTestResult,
		Subject: evidence.Subject{Commit: commit, Path: "forge-evidence/library"},
		Files: []storage.Content{
			{Name: textFile, Data: []byte(textContent)},
			{Name: binaryFile, Data: binaryContent()},
		},
	}
}

// openStore opens the store of a repository root and closes it after the
// test.
func openStore(t *testing.T, dir string, limits evidence.Limits) *Store {
	t.Helper()

	store, err := Open(dir, limits)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// added returns a repository root holding the example entry, and its store.
func added(t *testing.T) (string, *Store) {
	t.Helper()

	dir := t.TempDir()
	store := openStore(t, dir, evidence.Limits{})
	if _, err := store.Add(context.Background(), exampleDraft()); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return dir, store
}

// tree returns every path below dir with its content, or "/" for a folder
// and "-> target" for a symlink.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()

	result := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		relative, _ := filepath.Rel(dir, path)
		result[filepath.ToSlash(relative)] = describe(t, path, entry)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func describe(t *testing.T, path string, entry fs.DirEntry) string {
	t.Helper()

	switch {
	case entry.Type()&fs.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			t.Fatal(err)
		}
		return "-> " + target
	case entry.IsDir():
		return "/"
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// write creates a file below dir, with its folders.
func write(t *testing.T, dir, name string, content []byte) {
	t.Helper()

	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

// replace removes a path below dir and puts a symlink to target there.
func replace(t *testing.T, dir, name, target string) {
	t.Helper()

	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// copyDir copies the files of a folder to a new folder.
func copyDir(t *testing.T, from, to string) {
	t.Helper()

	for name, content := range tree(t, from) {
		if content != "/" {
			write(t, to, name, []byte(content))
		}
	}
}

// wantError fails unless err matches want.
func wantError(t *testing.T, err, want error) {
	t.Helper()

	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

// wantClean fails if an error message holds a host path or file content.
func wantClean(t *testing.T, err error, dir string) {
	t.Helper()

	message := err.Error()
	for _, secret := range []string{dir, filepath.Dir(dir), textContent, "PNG", "secret"} {
		if strings.Contains(message, secret) {
			t.Errorf("error %q holds %q", message, secret)
		}
	}
}
