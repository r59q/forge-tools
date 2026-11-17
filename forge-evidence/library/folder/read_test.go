package folder

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
	"github.com/r59q/forge-tools/forge-evidence/library/storage"
)

func TestReadMissingEntry(t *testing.T) {
	empty := openStore(t, t.TempDir(), evidence.Limits{})
	_, err := empty.Entry(context.Background(), exampleID())
	wantError(t, err, storage.ErrNotExist)

	_, store := added(t)
	other := evidence.ID{Commit: commit, Name: "other"}

	_, err = store.Entry(context.Background(), other)
	wantError(t, err, storage.ErrNotExist)

	_, err = store.ReadFile(context.Background(), other, textFile)
	wantError(t, err, storage.ErrNotExist)
}

func TestReadFileNotListed(t *testing.T) {
	dir, store := added(t)
	write(t, dir, entryPath+"/extra.txt", []byte("extra"))

	for _, file := range []string{"extra.txt", "missing.txt", evidence.MetadataFile} {
		_, err := store.ReadFile(context.Background(), exampleID(), file)
		wantError(t, err, storage.ErrNotExist)
	}
}

func TestReadRejectsUnsafeNames(t *testing.T) {
	_, store := added(t)

	for _, value := range []string{"..", "../" + name, "/etc/passwd", "a/b", ".hidden", ""} {
		_, err := store.Entry(context.Background(), evidence.ID{Commit: commit, Name: value})
		wantError(t, err, evidence.ErrInvalid)

		_, err = store.ReadFile(context.Background(), exampleID(), value)
		wantError(t, err, evidence.ErrInvalid)
	}

	_, err := store.Entry(context.Background(), evidence.ID{Commit: "../" + commit[3:], Name: name})
	wantError(t, err, evidence.ErrInvalid)
}

func TestReadRejectsSymlinks(t *testing.T) {
	tests := map[string]string{
		"evidence folder": ".evidence",
		"commit folder":   ".evidence/" + commit,
		"entry folder":    entryPath,
		"file":            entryPath + "/" + textFile,
		"metadata":        entryPath + "/" + evidence.MetadataFile,
	}

	for test, link := range tests {
		t.Run(test, func(t *testing.T) {
			dir, store := added(t)

			// The target is a copy, so only the symlink makes the read fail.
			outside := filepath.Join(t.TempDir(), "copy")
			if err := os.Rename(filepath.Join(dir, filepath.FromSlash(link)), outside); err != nil {
				t.Fatal(err)
			}
			replace(t, dir, link, outside)

			_, err := store.ReadFile(context.Background(), exampleID(), textFile)
			wantError(t, err, evidence.ErrInvalid)
			wantClean(t, err, dir)
		})
	}
}

func TestReadRejectsSymlinkInsideFolder(t *testing.T) {
	dir, store := added(t)
	copyDir(t, filepath.Join(dir, entryPath), filepath.Join(dir, ".evidence", commit, "copy"))
	replace(t, dir, entryPath, "copy")

	_, err := store.Entry(context.Background(), exampleID())
	wantError(t, err, evidence.ErrInvalid)
}

func TestReadRejectsOversizedFile(t *testing.T) {
	dir, _ := added(t)
	limits := evidence.Limits{FileBytes: 1 << 10}
	write(t, dir, entryPath+"/"+textFile, bytes.Repeat([]byte("x"), 1<<10+1))

	_, err := openStore(t, dir, limits).ReadFile(context.Background(), exampleID(), textFile)
	wantError(t, err, evidence.ErrTooLarge)
}

func TestReadRejectsLimits(t *testing.T) {
	tests := map[string]evidence.Limits{
		"file":     {FileBytes: int64(len(binaryContent())) - 1},
		"files":    {Files: 1},
		"entry":    {EntryBytes: int64(len(textContent))},
		"metadata": {MetadataBytes: 64},
	}

	for test, limits := range tests {
		t.Run(test, func(t *testing.T) {
			dir, _ := added(t)
			store := openStore(t, dir, limits)

			_, err := store.Entry(context.Background(), exampleID())
			wantError(t, err, evidence.ErrTooLarge)

			_, err = store.ReadFile(context.Background(), exampleID(), textFile)
			wantError(t, err, evidence.ErrTooLarge)
		})
	}
}

func TestReadRejectsOversizedMetadata(t *testing.T) {
	dir, store := added(t)
	padded := append(bytes.Repeat([]byte(" "), 64<<10), []byte("{}")...)
	write(t, dir, entryPath+"/"+evidence.MetadataFile, padded)

	_, err := store.Entry(context.Background(), exampleID())
	wantError(t, err, evidence.ErrTooLarge)
}

func TestReadRejectsMalformedMetadata(t *testing.T) {
	dir, store := added(t)
	write(t, dir, entryPath+"/"+evidence.MetadataFile, []byte(`{"version": 1, "secret": true}`))

	_, err := store.Entry(context.Background(), exampleID())
	wantError(t, err, evidence.ErrInvalid)
	wantClean(t, err, dir)
}

func TestReadRejectsMetadataOfAnotherEntry(t *testing.T) {
	dir, store := added(t)
	copyDir(t, filepath.Join(dir, entryPath), filepath.Join(dir, ".evidence", commit, "copy"))

	_, err := store.Entry(context.Background(), evidence.ID{Commit: commit, Name: "copy"})
	wantError(t, err, evidence.ErrInvalid)
}

func TestReadRejectsChangedFile(t *testing.T) {
	dir, store := added(t)
	tests := map[string][]byte{
		"content": []byte("ok  \tunit\r\nFAIL\n"),
		"size":    []byte("ok"),
	}

	for test, content := range tests {
		write(t, dir, entryPath+"/"+textFile, content)

		_, err := store.ReadFile(context.Background(), exampleID(), textFile)
		if err == nil {
			t.Fatalf("%s: ReadFile succeeded, want an error", test)
		}
		wantError(t, err, evidence.ErrInvalid)
	}
}

func TestReadRejectsCanceledContext(t *testing.T) {
	_, store := added(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := store.Entry(ctx, exampleID())
	wantError(t, err, context.Canceled)

	_, err = store.ReadFile(ctx, exampleID(), textFile)
	wantError(t, err, context.Canceled)

	_, err = store.Add(ctx, exampleDraft())
	wantError(t, err, context.Canceled)
}
