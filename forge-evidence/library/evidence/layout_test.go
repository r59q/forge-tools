package evidence

import (
	"errors"
	"strings"
	"testing"
)

func TestLayoutPaths(t *testing.T) {
	id := exampleEntry().ID()
	dir := ".evidence/" + exampleCommit + "/unit-tests"

	commitDir, err := CommitDir(id.Commit)
	if err != nil || commitDir != ".evidence/"+exampleCommit {
		t.Errorf("CommitDir = %q, %v", commitDir, err)
	}

	got, err := id.Dir()
	if err != nil || got != dir {
		t.Errorf("Dir = %q, %v, want %q", got, err, dir)
	}

	got, err = id.MetadataPath()
	if err != nil || got != dir+"/entry.json" {
		t.Errorf("MetadataPath = %q, %v", got, err)
	}

	got, err = id.FilePath("junit.xml")
	if err != nil || got != dir+"/junit.xml" {
		t.Errorf("FilePath = %q, %v", got, err)
	}
}

func TestLayoutRoundTrip(t *testing.T) {
	ids := []ID{exampleEntry().ID(), sha256Entry().ID()}

	for _, id := range ids {
		dir, err := id.Dir()
		if err != nil {
			t.Fatalf("Dir: %v", err)
		}
		assertParsed(t, dir, id, "")

		for _, file := range []string{MetadataFile, "junit.xml"} {
			path, err := id.FilePath(file)
			if err != nil {
				t.Fatalf("FilePath: %v", err)
			}
			assertParsed(t, path, id, file)
		}
	}
}

func assertParsed(t *testing.T, path string, id ID, file string) {
	t.Helper()

	gotID, gotFile, err := ParsePath(path)
	if err != nil || gotID != id || gotFile != file {
		t.Errorf("ParsePath(%q) = %+v, %q, %v, want %+v, %q", path, gotID, gotFile, err, id, file)
	}
}

func TestLayoutRejectsInvalidID(t *testing.T) {
	ids := []ID{
		{Commit: "HEAD", Name: "a"},
		{Commit: exampleCommit, Name: ".."},
		{Commit: exampleCommit, Name: ""},
	}
	for _, id := range ids {
		if _, err := id.Dir(); !errors.Is(err, ErrInvalid) {
			t.Errorf("Dir(%+v) = %v, want ErrInvalid", id, err)
		}
	}

	id := exampleEntry().ID()
	if _, err := id.FilePath("../x"); !errors.Is(err, ErrInvalid) {
		t.Errorf("FilePath(../x) = %v, want ErrInvalid", err)
	}
	if _, err := CommitDir("main"); !errors.Is(err, ErrInvalid) {
		t.Errorf("CommitDir(main) = %v, want ErrInvalid", err)
	}
}

func TestParsePathRejects(t *testing.T) {
	dir := ".evidence/" + exampleCommit + "/unit-tests"

	paths := []string{
		"",
		".evidence",
		".evidence/" + exampleCommit,
		dir + "/",
		dir + "/a/b",
		"/" + dir,
		"./" + dir,
		"evidence/" + exampleCommit + "/unit-tests",
		".Evidence/" + exampleCommit + "/unit-tests",
		".evidence/" + strings.ToUpper(exampleCommit) + "/unit-tests",
		".evidence/" + exampleCommit + "/..",
		".evidence/" + exampleCommit + "/../x",
		dir + "/..",
		dir + `/a\b`,
		dir + "/a\x00",
		`.evidence\` + exampleCommit + `\unit-tests`,
	}
	for _, path := range paths {
		if _, _, err := ParsePath(path); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParsePath(%q) = %v, want ErrInvalid", path, err)
		}
	}
}
