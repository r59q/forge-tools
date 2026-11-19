package folder

import (
	"bytes"
	"slices"
	"testing"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
	"github.com/r59q/forge-tools/forge-evidence/library/storage"
)

func TestEvidenceRoundTrip(t *testing.T) {
	var store storage.Store = openStore(t, t.TempDir(), evidence.Limits{})
	ctx := t.Context()
	testRun := exampleDraft()
	screenshot := listDraft(laterCommit, "screenshot", "frontend", evidence.KindScreenshot)
	screenshot.Files = screenshot.Files[1:]

	screenshotEntry, err := store.Add(ctx, screenshot)
	if err != nil {
		t.Fatal(err)
	}
	testEntry, err := store.Add(ctx, testRun)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		filter storage.Filter
		want   []evidence.Entry
	}{
		{storage.Filter{}, []evidence.Entry{testEntry, screenshotEntry}},
		{storage.Filter{Commit: testRun.Subject.Commit, Path: testRun.Subject.Path}, []evidence.Entry{testEntry}},
		{storage.Filter{Kind: screenshot.Kind}, []evidence.Entry{screenshotEntry}},
	} {
		got, err := store.List(ctx, tc.filter)
		if err != nil || !slices.EqualFunc(got, tc.want, sameEntry) {
			t.Fatalf("List(%+v) = %+v, %v, want %+v", tc.filter, got, err, tc.want)
		}
	}

	for _, draft := range []storage.Draft{testRun, screenshot} {
		wantEntry := draft.Entry()
		id := wantEntry.ID()
		entry, err := store.Entry(ctx, id)
		if err != nil || !sameEntry(entry, wantEntry) {
			t.Fatalf("Entry(%+v) = %+v, %v, want %+v", id, entry, err, wantEntry)
		}
		for _, file := range draft.Files {
			content, err := store.ReadFile(ctx, id, file.Name)
			if err != nil || !bytes.Equal(content, file.Data) {
				t.Fatalf("ReadFile(%+v, %s) = %x, %v, want %x", id, file.Name, content, err, file.Data)
			}
		}
	}

	report := validate(t, store)
	if len(report.Problems) != 0 || report.Truncated {
		t.Fatalf("Validate = %+v, want an empty report", report)
	}
}
