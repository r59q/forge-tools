//go:build unix

package folder

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
	"github.com/r59q/forge-tools/forge-evidence/library/storage"
)

func TestReadRejectsFIFO(t *testing.T) {
	for _, file := range []string{textFile, evidence.MetadataFile} {
		dir, store := added(t)
		path := filepath.Join(dir, filepath.FromSlash(entryPath), file)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := store.ReadFile(context.Background(), exampleID(), textFile)
		wantError(t, err, evidence.ErrInvalid)
	}
}

func TestValidateSpecialFile(t *testing.T) {
	for _, path := range []string{metadataPath, textPath} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			root, store := added(t)
			removePath(path)(t, root)
			if err := syscall.Mkfifo(filepath.Join(root, filepath.FromSlash(path)), 0o600); err != nil {
				t.Fatal(err)
			}
			wantProblem(t, store, path, storage.SpecialFile)
		})
	}
}
