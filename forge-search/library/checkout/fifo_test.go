//go:build unix

package checkout

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadSkipsFIFO(t *testing.T) {
	dir, root := checkout(t, map[string]string{"a.txt": "a"})
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}

	if tree := read(t, root, Options{}); len(tree.Documents) != 1 || tree.State.Unsupported != 1 {
		t.Errorf("tree = %d documents, %+v, want the FIFO unsupported", len(tree.Documents), tree.State)
	}
}

func TestReadFileReplacedByFIFODoesNotBlock(t *testing.T) {
	dir, root := checkout(t, map[string]string{"pipe": "file"})
	listed, err := list(context.Background(), root, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}

	remove(t, dir, "pipe")
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := readFile(context.Background(), root, listed.files[0]); !errors.Is(err, ErrChanged) {
		t.Errorf("error = %v, want ErrChanged", err)
	}
}
