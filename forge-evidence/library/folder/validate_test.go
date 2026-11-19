package folder

import (
	"crypto/rand"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
	"github.com/r59q/forge-tools/forge-evidence/library/storage"
)

const (
	metadataPath = entryPath + "/" + evidence.MetadataFile
	textPath     = entryPath + "/" + textFile
	commitPath   = evidence.Root + "/" + commit
)

func TestValidateMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*testing.T, string)
		limits evidence.Limits
		want   storage.Reason
	}{
		{"missing", removePath(metadataPath), evidence.Limits{}, storage.MissingMetadata},
		{"malformed", writePath(metadataPath, `{"private-metadata-text":`), evidence.Limits{}, storage.MalformedMetadata},
		{"oversized", nil, evidence.Limits{MetadataBytes: 1}, storage.OversizedMetadata},
		{"version", editMetadata(`"version": 1`, `"version": 99`), evidence.Limits{}, storage.UnknownVersion},
		{"missing version", editMetadata(`"version": 1,`, ""), evidence.Limits{}, storage.InvalidField},
		{"missing title", editMetadata(`"title": "Unit tests pass",`, ""), evidence.Limits{}, storage.InvalidField},
		{"bad SHA", editMetadata(commit, "private-metadata-text"), evidence.Limits{}, storage.InvalidField},
		{"unsafe subject", editMetadata(`"path": "forge-evidence/library"`, `"path": "/private-metadata-text"`),
			evidence.Limits{}, storage.InvalidField},
		{"wrong ID", editMetadata(`"name": "unit-tests"`, `"name": "private-metadata-text"`), evidence.Limits{}, storage.InvalidField},
		{"unsafe listed name", editMetadata(textFile, "../private-metadata-text"), evidence.Limits{}, storage.InvalidField},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := added(t)
			if tc.edit != nil {
				tc.edit(t, root)
			}
			problem := wantProblem(t, openStore(t, root, tc.limits), metadataPath, tc.want)
			for _, secret := range []string{"private-metadata-text", root, textContent} {
				if strings.Contains(problem.Message, secret) || strings.Contains(problem.Path, secret) {
					t.Fatalf("validation disclosed %q in %+v", secret, problem)
				}
			}
		})
	}
}

func TestValidateLayout(t *testing.T) {
	staging := commitPath + "/" + stagingPrefix + rand.Text()
	for _, tc := range []struct {
		name string
		path string
		edit func(*testing.T, string)
		want storage.Reason
	}{
		{"root file", evidence.Root, writePath(evidence.Root, ""), storage.StrayPath},
		{"stray root file", evidence.Root + "/readme", writePath(evidence.Root+"/readme", ""), storage.StrayPath},
		{"unsafe commit", evidence.Root + "/main", makeDirectory(evidence.Root + "/main"), storage.UnsafeName},
		{"stray commit file", commitPath + "/note", writePath(commitPath+"/note", ""), storage.StrayPath},
		{"unsafe entry", commitPath + "/bad..name", makeDirectory(commitPath + "/bad..name"), storage.UnsafeName},
		{"staging", staging, makeDirectory(staging), storage.UnsafeName},
		{"stray entry directory", entryPath + "/subdir", makeDirectory(entryPath + "/subdir"), storage.StrayPath},
		{"metadata directory", metadataPath, func(t *testing.T, root string) {
			removePath(metadataPath)(t, root)
			makeDirectory(metadataPath)(t, root)
		}, storage.StrayPath},
		{"unsafe file", entryPath + "/.hidden", writePath(entryPath+"/.hidden", ""), storage.UnsafeName},
		{"reserved file", entryPath + "/Entry.JSON", writePath(entryPath+"/Entry.JSON", ""), storage.UnsafeName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.path != evidence.Root {
				root, _ = added(t)
			}
			tc.edit(t, root)
			wantProblem(t, openStore(t, root, evidence.Limits{}), tc.path, tc.want)
		})
	}
}

func TestValidateFiles(t *testing.T) {
	unlisted := entryPath + "/unlisted"
	for _, tc := range []struct {
		name   string
		path   string
		edit   func(*testing.T, string)
		limits evidence.Limits
		want   storage.Reason
	}{
		{"missing", textPath, removePath(textPath), evidence.Limits{}, storage.MissingFile},
		{"unlisted", unlisted, writePath(unlisted, ""), evidence.Limits{}, storage.UnlistedFile},
		{"size mismatch", textPath, writePath(textPath, ""), evidence.Limits{}, storage.FileMismatch},
		{"digest mismatch", textPath, writePath(textPath, strings.Repeat("x", len(textContent))),
			evidence.Limits{}, storage.FileMismatch},
		{"oversized file", textPath, writePath(textPath, strings.Repeat("a", 65)),
			evidence.Limits{FileBytes: 64}, storage.FileTooLarge},
		{"oversized recorded file", entryPath, editMetadata(fmt.Sprintf(`"size": %d`, len(textContent)), `"size": 65`),
			evidence.Limits{FileBytes: 64}, storage.FileTooLarge},
		{"too many on disk", entryPath, writePath(unlisted, ""), evidence.Limits{Files: 2}, storage.TooManyFiles},
		{"too many declared and on disk", entryPath, nil, evidence.Limits{Files: 1}, storage.TooManyFiles},
		{"total size", entryPath, nil, evidence.Limits{EntryBytes: 20}, storage.EntryTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := added(t)
			if tc.edit != nil {
				tc.edit(t, root)
			}
			wantProblem(t, openStore(t, root, tc.limits), tc.path, tc.want)
		})
	}
}

func TestValidateReportsAcrossEntries(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root, evidence.Limits{})
	for _, name := range []string{"debris", "corrupt", "absent"} {
		draft := exampleDraft()
		draft.Name = name
		if _, err := store.Add(t.Context(), draft); err != nil {
			t.Fatal(err)
		}
	}
	absent := commitPath + "/absent/" + evidence.MetadataFile
	corrupt := commitPath + "/corrupt/" + evidence.MetadataFile
	debris := commitPath + "/debris/extra"
	removePath(absent)(t, root)
	write(t, root, corrupt, []byte("{"))
	write(t, root, debris, nil)

	before := tree(t, root)
	report := validate(t, store)
	want := []storage.Problem{
		{Path: absent, Reason: storage.MissingMetadata},
		{Path: corrupt, Reason: storage.MalformedMetadata},
		{Path: debris, Reason: storage.UnlistedFile},
	}
	sameProblem := func(a, b storage.Problem) bool { return a.Path == b.Path && a.Reason == b.Reason && a.Message != "" }
	if report.Truncated || !slices.EqualFunc(report.Problems, want, sameProblem) {
		t.Fatalf("Validate = %+v, want paths and reasons %+v", report, want)
	}
	if !maps.Equal(before, tree(t, root)) {
		t.Fatal("Validate changed the folder")
	}
}

func wantProblem(t *testing.T, store storage.Store, path string, reason storage.Reason) storage.Problem {
	t.Helper()

	report := validate(t, store)
	if report.Truncated || len(report.Problems) != 1 {
		t.Fatalf("Validate = %+v, want one problem (%s, %s)", report, path, reason)
	}
	problem := report.Problems[0]
	if problem.Path != path || problem.Reason != reason || problem.Message == "" {
		t.Fatalf("Validate problem = %+v, want %s with %s and a message", problem, path, reason)
	}
	return problem
}

func validate(t *testing.T, store storage.Store) storage.Report {
	t.Helper()

	report, err := store.Validate(t.Context())
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return report
}

func editMetadata(old, replacement string) func(*testing.T, string) {
	return func(t *testing.T, root string) {
		t.Helper()

		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(metadataPath)))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, old) {
			t.Fatalf("metadata fixture is missing %q", old)
		}
		write(t, root, metadataPath, []byte(strings.Replace(text, old, replacement, 1)))
	}
}

func removePath(path string) func(*testing.T, string) {
	return func(t *testing.T, root string) {
		t.Helper()

		if err := os.Remove(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			t.Fatal(err)
		}
	}
}

func writePath(path, content string) func(*testing.T, string) {
	return func(t *testing.T, root string) {
		t.Helper()

		write(t, root, path, []byte(content))
	}
}

func makeDirectory(path string) func(*testing.T, string) {
	return func(t *testing.T, root string) {
		t.Helper()

		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(path)), dirMode); err != nil {
			t.Fatal(err)
		}
	}
}
