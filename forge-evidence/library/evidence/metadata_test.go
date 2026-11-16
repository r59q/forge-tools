package evidence

import (
	"bytes"
	_ "embed"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// golden is the encoding of exampleEntry, as the README shows it. It is
// embedded so the tests, like the package, read no files.
//
//go:embed testdata/entry.json
var golden string

const (
	exampleCommit = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	exampleDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	otherDigest   = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
)

func exampleEntry() Entry {
	return Entry{
		Name:    "unit-tests",
		Title:   "Unit tests pass",
		Kind:    KindTestResult,
		Subject: Subject{Commit: exampleCommit, Path: "forge-evidence/library"},
		Files: []File{
			{Name: "junit.xml", Size: 2048, SHA256: otherDigest},
			{Name: "go-test.log", Size: 0, SHA256: exampleDigest},
		},
	}
}

func TestEncodeMatchesGolden(t *testing.T) {
	data, err := Encode(exampleEntry(), Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if string(data) != goldenMetadata(t) {
		t.Fatalf("Encode =\n%s\nwant testdata/entry.json:\n%s", data, golden)
	}
}

func TestEncodeIsDeterministic(t *testing.T) {
	entry := exampleEntry()

	first, err := Encode(entry, Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	second, err := Encode(entry, Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("Encode gave different bytes:\n%s\n%s", first, second)
	}

	entry.Files[0], entry.Files[1] = entry.Files[1], entry.Files[0]
	reordered, err := Encode(entry, Limits{})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Equal(first, reordered) {
		t.Fatalf("Encode depends on file order:\n%s\n%s", first, reordered)
	}
}

func TestEncodeDoesNotReorderCallerFiles(t *testing.T) {
	entry := exampleEntry()
	if _, err := Encode(entry, Limits{}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if entry.Files[0].Name != "junit.xml" {
		t.Fatalf("Encode reordered the caller's files")
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	entries := []Entry{exampleEntry(), wholeCommitEntry(), sha256Entry()}

	for _, entry := range entries {
		data, err := Encode(entry, Limits{})
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}

		decoded, err := Decode(data, Limits{})
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}

		entry.Files = sortedFiles(entry.Files)
		if !reflect.DeepEqual(decoded, entry) {
			t.Fatalf("Decode = %+v, want %+v", decoded, entry)
		}

		again, err := Encode(decoded, Limits{})
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("re-encoding changed the bytes: %v", err)
		}
	}
}

func TestDecodeReturnsFilesInNameOrder(t *testing.T) {
	data := []byte(`{"version": 1, "name": "a", "title": "t", "kind": "log",
		"subject": {"commit": "` + exampleCommit + `"},
		"files": [
			{"name": "b.log", "size": 1, "sha256": "` + exampleDigest + `"},
			{"name": "a.log", "size": 2, "sha256": "` + exampleDigest + `"}
		]}`)

	entry, err := Decode(data, Limits{})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if entry.Files[0].Name != "a.log" || entry.Files[1].Name != "b.log" {
		t.Fatalf("Decode files = %+v, want name order", entry.Files)
	}
}

func TestEncodeRejectsInvalidEntry(t *testing.T) {
	entry := exampleEntry()
	entry.Subject.Commit = "HEAD"

	if _, err := Encode(entry, Limits{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Encode = %v, want ErrInvalid", err)
	}
}

func TestEncodeRejectsMetadataOverLimit(t *testing.T) {
	if _, err := Encode(exampleEntry(), Limits{MetadataBytes: 64}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Encode = %v, want ErrTooLarge", err)
	}
}

func wholeCommitEntry() Entry {
	return Entry{
		Name:    "screenshot",
		Title:   `Login form renders on mobile <320px> & "dark" mode, ünïcode`,
		Kind:    KindScreenshot,
		Subject: Subject{Commit: exampleCommit},
		Files:   []File{{Name: "login.png", Size: 4 << 20, SHA256: exampleDigest}},
	}
}

func sha256Entry() Entry {
	return Entry{
		Name:    "analysis",
		Title:   "Custom kind",
		Kind:    "benchmark",
		Subject: Subject{Commit: strings.Repeat("ab", 32), Path: "a/b c/d.go"},
		Files:   []File{{Name: "result.md", Size: 1, SHA256: exampleDigest}},
	}
}
