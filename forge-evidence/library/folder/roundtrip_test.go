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
	text := exampleDraft()
	text.Files = text.Files[:1]
	binary := listDraft(laterCommit, "screenshot", "frontend", evidence.KindScreenshot)
	binary.Files = binary.Files[1:]

	binaryEntry, err := store.Add(ctx, binary)
	if err != nil {
		t.Fatal(err)
	}
	textEntry, err := store.Add(ctx, text)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		filter storage.Filter
		want   []evidence.Entry
	}{
		{storage.Filter{}, []evidence.Entry{textEntry, binaryEntry}},
		{storage.Filter{Commit: text.Subject.Commit, Path: text.Subject.Path}, []evidence.Entry{textEntry}},
		{storage.Filter{Kind: binary.Kind}, []evidence.Entry{binaryEntry}},
	} {
		got, err := store.List(ctx, tc.filter)
		if err != nil || !slices.EqualFunc(got, tc.want, sameEntry) {
			t.Fatalf("List(%+v) = %+v, %v, want %+v", tc.filter, got, err, tc.want)
		}
	}

	for _, draft := range []storage.Draft{text, binary} {
		wantEntry := draft.Entry()
		id := wantEntry.ID()
		entry, err := store.Entry(ctx, id)
		if err != nil || !sameEntry(entry, wantEntry) {
			t.Fatalf("Entry(%+v) = %+v, %v, want %+v", id, entry, err, wantEntry)
		}
		file := draft.Files[0]
		content, err := store.ReadFile(ctx, id, file.Name)
		if err != nil || !bytes.Equal(content, file.Data) {
			t.Fatalf("ReadFile(%+v, %s) = %x, %v, want %x", id, file.Name, content, err, file.Data)
		}
	}

	report := validate(t, store)
	if len(report.Problems) != 0 || report.Truncated {
		t.Fatalf("Validate = %+v, want an empty report", report)
	}
}
