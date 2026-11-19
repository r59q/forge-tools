package folder

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
	"github.com/r59q/forge-tools/forge-evidence/library/storage"
)

const laterCommit = "fb825dc642cb6eb9a060e54bf8d69288fbee4904"

func TestListOrderAndFilters(t *testing.T) {
	root := t.TempDir()
	var store storage.Store = openStore(t, root, evidence.Limits{})
	ctx := context.Background()

	drafts := []storage.Draft{
		listDraft(laterCommit, "Alpha", "cmd", evidence.KindScreenshot),
		listDraft(commit, "preview", "cmd/serve", evidence.KindScreenshot),
		listDraft(commit, "Alpha", "cmd", evidence.KindTestResult),
		listDraft(commit, "build", "cmd", evidence.KindScreenshot),
	}
	added := make([]evidence.Entry, len(drafts))
	for i, draft := range drafts {
		entry, err := store.Add(ctx, draft)
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		added[i] = entry
	}

	// Listing depends only on metadata, even when a payload is missing.
	if err := os.Remove(filepath.Join(root, evidence.Root, commit, "Alpha", textFile)); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		filter storage.Filter
		want   []int
	}{
		{"all", storage.Filter{}, []int{2, 3, 1, 0}},
		{"commit", storage.Filter{Commit: commit}, []int{2, 3, 1}},
		{"path", storage.Filter{Path: "cmd"}, []int{2, 3, 0}},
		{"kind", storage.Filter{Kind: evidence.KindScreenshot}, []int{3, 1, 0}},
		{"combined", storage.Filter{Commit: commit, Path: "cmd", Kind: evidence.KindScreenshot}, []int{3}},
		{"no match", storage.Filter{Path: "CMD"}, nil},
		{"abbreviated SHA", storage.Filter{Commit: commit[:7]}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := make([]evidence.Entry, 0, len(tc.want))
			for _, i := range tc.want {
				want = append(want, added[i])
			}
			entries, err := store.List(ctx, tc.filter)
			if err != nil || !slices.EqualFunc(entries, want, sameEntry) {
				t.Fatalf("List = %+v, %v, want %+v", entries, err, want)
			}
		})
	}
}

func listDraft(sha, name, path string, kind evidence.Kind) storage.Draft {
	draft := exampleDraft()
	draft.Name = name
	draft.Subject = evidence.Subject{Commit: sha, Path: path}
	draft.Kind = kind
	return draft
}

func sameEntry(a, b evidence.Entry) bool {
	return a.ID() == b.ID() && a.Subject == b.Subject && a.Kind == b.Kind &&
		a.Title == b.Title && slices.Equal(a.Files, b.Files)
}

func TestListEmpty(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root, evidence.Limits{})
	ctx := context.Background()

	entries, err := store.List(ctx, storage.Filter{})
	if err != nil || len(entries) != 0 {
		t.Fatalf("List with missing folder = %+v, %v", entries, err)
	}
	if err := os.Mkdir(filepath.Join(root, evidence.Root), dirMode); err != nil {
		t.Fatal(err)
	}
	entries, err = store.List(ctx, storage.Filter{})
	if err != nil || len(entries) != 0 {
		t.Fatalf("List with empty folder = %+v, %v", entries, err)
	}
}

func TestListEntryLimitAcrossCommits(t *testing.T) {
	root, writer := added(t)
	ctx := context.Background()
	store := openStore(t, root, evidence.Limits{Entries: 2})

	for _, name := range []string{"first", "second"} {
		if _, err := writer.Add(ctx, listDraft(laterCommit, name, "cmd", evidence.KindLog)); err != nil {
			t.Fatal(err)
		}
		if name == "first" {
			entries, err := store.List(ctx, storage.Filter{})
			if err != nil || len(entries) != 2 {
				t.Fatalf("List at the limit = %+v, %v", entries, err)
			}
		}
	}

	// Even a filter selecting one entry cannot bypass the traversal limit.
	entries, err := store.List(ctx, storage.Filter{Commit: commit})
	wantListError(t, root, evidence.Root+"/"+laterCommit, entries, err, evidence.ErrTooLarge)
}

func TestListCommitLimit(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root, evidence.Limits{Entries: 1})
	for _, sha := range []string{commit, laterCommit} {
		if err := os.MkdirAll(filepath.Join(root, evidence.Root, sha), dirMode); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.List(context.Background(), storage.Filter{})
	wantListError(t, root, evidence.Root, entries, err, evidence.ErrTooLarge)
}

func TestListBrokenEntry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		limits  evidence.Limits
		want    error
	}{
		{"z-broken", `{"secret": true`, evidence.Limits{}, evidence.ErrInvalid},
		{"z..unsafe", `{}`, evidence.Limits{}, evidence.ErrInvalid},
		{"z-large", strings.Repeat(" ", 1025), evidence.Limits{MetadataBytes: 1024}, evidence.ErrTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := added(t)
			store := openStore(t, root, tc.limits)
			path := evidence.Root + "/" + commit + "/" + tc.name
			write(t, root, path+"/"+evidence.MetadataFile, []byte(tc.content))

			for _, filter := range []storage.Filter{{}, {Commit: laterCommit}} {
				entries, err := store.List(context.Background(), filter)
				wantListError(t, root, path, entries, err, tc.want)
			}
		})
	}
}

func TestListRejectsSymlinkedEntry(t *testing.T) {
	root, store := added(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Rename(filepath.Join(root, filepath.FromSlash(entryPath)), outside); err != nil {
		t.Fatal(err)
	}
	replace(t, root, entryPath, outside)

	entries, err := store.List(context.Background(), storage.Filter{})
	wantListError(t, root, entryPath, entries, err, evidence.ErrInvalid)
}

func TestListIgnoresStagingDirectory(t *testing.T) {
	root, store := added(t)
	staging := filepath.Join(root, evidence.Root, commit, stagingPrefix+rand.Text())
	if err := os.Mkdir(staging, dirMode); err != nil {
		t.Fatal(err)
	}

	entries, err := store.List(context.Background(), storage.Filter{})
	if err != nil || len(entries) != 1 || entries[0].ID() != exampleID() {
		t.Fatalf("List with a staging directory = %+v, %v", entries, err)
	}
}

func TestListRejectsCanceledContext(t *testing.T) {
	_, store := added(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.List(ctx, storage.Filter{})
	wantError(t, err, context.Canceled)
}

func wantListError(t *testing.T, root, path string, entries []evidence.Entry, err, category error) {
	t.Helper()

	wantError(t, err, category)
	wantClean(t, err, root)
	if len(entries) != 0 || !strings.Contains(err.Error(), path) {
		t.Fatalf("List = %+v, %v, want no entries and error naming %s", entries, err, path)
	}
}
