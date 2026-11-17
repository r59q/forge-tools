package folder

import (
	"bytes"
	"context"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
	"github.com/r59q/forge-tools/forge-evidence/library/storage"
)

func TestAddThenRead(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir, evidence.Limits{})

	added, err := store.Add(context.Background(), exampleDraft())
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	read, err := store.Entry(context.Background(), exampleID())
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if !reflect.DeepEqual(read, added) {
		t.Errorf("Entry = %+v, want %+v", read, added)
	}

	for _, want := range exampleDraft().Files {
		content, err := store.ReadFile(context.Background(), exampleID(), want.Name)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", want.Name, err)
		}
		if !bytes.Equal(content, want.Data) {
			t.Errorf("ReadFile %s = %q, want %q", want.Name, content, want.Data)
		}
	}
}

func TestAddWritesLayout(t *testing.T) {
	dir, _ := added(t)

	metadata, err := evidence.Encode(exampleDraft().Entry(), evidence.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		".evidence":                             "/",
		".evidence/" + commit:                   "/",
		entryPath:                               "/",
		entryPath + "/" + evidence.MetadataFile: string(metadata),
		entryPath + "/" + textFile:              textContent,
		entryPath + "/" + binaryFile:            string(binaryContent()),
	}

	if got := tree(t, dir); !maps.Equal(got, want) {
		t.Errorf("tree = %q, want %q", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
}

func TestAddIsDeterministic(t *testing.T) {
	first, _ := added(t)
	second, _ := added(t)

	if !maps.Equal(tree(t, first), tree(t, second)) {
		t.Errorf("trees differ:\n%q\n%q", tree(t, first), tree(t, second))
	}
}

func TestAddRejectsUnsafeNames(t *testing.T) {
	unsafe := []string{"..", "../outside", "/tmp/outside", "a/b", `a\b`, ".hidden", "", "con"}

	for _, value := range unsafe {
		entryName := exampleDraft()
		entryName.Name = value

		fileName := exampleDraft()
		fileName.Files[0].Name = value

		for _, draft := range []storage.Draft{entryName, fileName} {
			parent := t.TempDir()
			dir := filepath.Join(parent, "repository")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}

			_, err := openStore(t, dir, evidence.Limits{}).Add(context.Background(), draft)
			wantError(t, err, evidence.ErrInvalid)

			if got := tree(t, parent); !maps.Equal(got, map[string]string{"repository": "/"}) {
				t.Errorf("name %q: tree = %q, want nothing created", value, got)
			}
		}
	}
}

func TestAddRejectsExistingEntry(t *testing.T) {
	dir, store := added(t)
	before := tree(t, dir)

	draft := exampleDraft()
	draft.Title = "Other claim"
	draft.Files = []storage.Content{{Name: "other.txt", Data: []byte("other")}}

	_, err := store.Add(context.Background(), draft)
	wantError(t, err, storage.ErrExist)

	if after := tree(t, dir); !maps.Equal(after, before) {
		t.Errorf("tree changed:\n%q\nwant\n%q", after, before)
	}
}

func TestAddRejectsSymlinkAtEntry(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	write(t, dir, ".evidence/"+commit+"/keep", nil)
	replace(t, dir, entryPath, outside)

	_, err := openStore(t, dir, evidence.Limits{}).Add(context.Background(), exampleDraft())
	wantError(t, err, storage.ErrExist)

	if got := tree(t, outside); len(got) != 0 {
		t.Errorf("outside = %q, want empty", got)
	}
}

func TestAddRejectsLimits(t *testing.T) {
	tests := map[string]evidence.Limits{
		"file":     {FileBytes: int64(len(binaryContent())) - 1},
		"files":    {Files: 1},
		"entry":    {EntryBytes: int64(len(textContent))},
		"metadata": {MetadataBytes: 64},
	}

	for test, limits := range tests {
		t.Run(test, func(t *testing.T) {
			dir := t.TempDir()

			_, err := openStore(t, dir, limits).Add(context.Background(), exampleDraft())
			wantError(t, err, evidence.ErrTooLarge)

			if got := tree(t, dir); len(got) != 0 {
				t.Errorf("tree = %q, want nothing written", got)
			}
		})
	}
}

func TestAddRejectsSymlinkedFolders(t *testing.T) {
	tests := map[string]string{
		"evidence folder": ".evidence",
		"commit folder":   ".evidence/" + commit,
	}

	for test, link := range tests {
		t.Run(test, func(t *testing.T) {
			dir := t.TempDir()
			outside := t.TempDir()
			write(t, dir, ".evidence/keep", nil)
			replace(t, dir, link, outside)

			_, err := openStore(t, dir, evidence.Limits{}).Add(context.Background(), exampleDraft())
			wantError(t, err, evidence.ErrInvalid)
			wantClean(t, err, dir)

			if got := tree(t, outside); len(got) != 0 {
				t.Errorf("outside = %q, want empty", got)
			}
		})
	}
}

func TestAddRejectsEvidenceLinkedInsideRepository(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "src/main.go", []byte("package main\n"))
	replace(t, dir, ".evidence", "src")

	_, err := openStore(t, dir, evidence.Limits{}).Add(context.Background(), exampleDraft())
	wantError(t, err, evidence.ErrInvalid)

	if got := tree(t, filepath.Join(dir, "src")); len(got) != 1 {
		t.Errorf("src = %q, want only main.go", got)
	}
}

func TestFailedAddLeavesNoEntry(t *testing.T) {
	dir := t.TempDir()
	store := openStore(t, dir, evidence.Limits{})

	// The context ends after the first file is written.
	ctx := &endingContext{Context: context.Background(), checks: 2}
	_, err := store.Add(ctx, exampleDraft())
	wantError(t, err, context.Canceled)

	_, err = store.Entry(context.Background(), exampleID())
	wantError(t, err, storage.ErrNotExist)

	for path := range tree(t, dir) {
		if strings.Contains(path, stagingPrefix) || strings.HasPrefix(path, entryPath) {
			t.Errorf("failed Add left %q", path)
		}
	}
}

func TestPartialEntryIsNotComplete(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, entryPath+"/"+textFile, []byte(textContent))

	_, err := openStore(t, dir, evidence.Limits{}).Entry(context.Background(), exampleID())
	wantError(t, err, storage.ErrNotExist)
}

func TestAddErrorHasNoHostPath(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".evidence", []byte("secret"))

	_, err := openStore(t, dir, evidence.Limits{}).Add(context.Background(), exampleDraft())
	wantError(t, err, evidence.ErrInvalid)
	wantClean(t, err, dir)
}

func TestOpenRejectsMissingRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")

	_, err := Open(dir, evidence.Limits{})
	wantError(t, err, storage.ErrNotExist)
	wantClean(t, err, dir)
}

// endingContext ends after its Err method is called checks times.
type endingContext struct {
	context.Context

	checks int32
	calls  atomic.Int32
}

func (c *endingContext) Err() error {
	if c.calls.Add(1) > c.checks {
		return context.Canceled
	}
	return nil
}
