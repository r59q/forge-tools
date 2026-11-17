package storage

import (
	"reflect"
	"testing"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
)

func TestDraftEntry(t *testing.T) {
	subject := evidence.Subject{Commit: "4b825dc642cb6eb9a060e54bf8d69288fbee4904"}
	draft := Draft{
		Name:    "unit-tests",
		Title:   "Unit tests pass",
		Kind:    evidence.KindTestResult,
		Subject: subject,
		Files: []Content{
			{Name: "test", Data: []byte("test")},
			{Name: "empty.log"},
		},
	}

	want := evidence.Entry{
		Name:    "unit-tests",
		Title:   "Unit tests pass",
		Kind:    evidence.KindTestResult,
		Subject: subject,
		Files: []evidence.File{
			{Name: "test", Size: 4, SHA256: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"},
			{Name: "empty.log", Size: 0, SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		},
	}

	entry := draft.Entry()
	if !reflect.DeepEqual(entry, want) {
		t.Errorf("Entry = %+v, want %+v", entry, want)
	}
	if err := entry.Validate(evidence.Limits{}); err != nil {
		t.Errorf("Validate: %v", err)
	}
}
