package folder

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-evidence/library/evidence"
	"github.com/r59q/forge-tools/forge-evidence/library/storage"
)

func TestValidateProblemLimit(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"c", "a", "b"} {
		write(t, root, commitPath+"/"+name, nil)
	}
	store := openStore(t, root, evidence.Limits{Problems: 2})
	report := validate(t, store)
	if !report.Truncated || len(report.Problems) != 2 ||
		report.Problems[0].Path != commitPath+"/a" || report.Problems[1].Path != commitPath+"/b" {
		t.Fatalf("Validate with problem limit = %+v, want a and b and truncation", report)
	}
	removePath(commitPath+"/c")(t, root)
	atLimit := validate(t, store)
	if atLimit.Truncated || !reflect.DeepEqual(report.Problems, atLimit.Problems) {
		t.Fatalf("Validate at problem limit = %+v, want the same problems without truncation", atLimit)
	}
}

func TestValidateEntryLimits(t *testing.T) {
	t.Run("root children", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"a", "b", "c"} {
			write(t, root, evidence.Root+"/"+name, nil)
		}
		store := openStore(t, root, evidence.Limits{Entries: 2})
		report, err := store.Validate(t.Context())
		wantValidationError(t, root, evidence.Root, report, err, evidence.ErrTooLarge)
	})
	t.Run("entry children across commits", func(t *testing.T) {
		root := t.TempDir()
		for _, path := range []string{commitPath + "/a", commitPath + "/b", evidence.Root + "/" + laterCommit + "/c"} {
			write(t, root, path, nil)
		}
		store := openStore(t, root, evidence.Limits{Entries: 2, Problems: 1})
		report, err := store.Validate(t.Context())
		wantValidationError(t, root, evidence.Root+"/"+laterCommit, report, err, evidence.ErrTooLarge)
	})
}

func TestValidateCannotComplete(t *testing.T) {
	root, store := added(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	report, err := store.Validate(ctx)
	wantValidationError(t, root, "", report, err, context.Canceled)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	report, err = store.Validate(t.Context())
	wantValidationError(t, root, evidence.Root, report, err, storage.ErrUnreadable)
}

func TestValidateMissingFolder(t *testing.T) {
	root := t.TempDir()
	store := openStore(t, root, evidence.Limits{})
	for _, makeFolder := range []bool{false, true} {
		if makeFolder {
			makeDirectory(evidence.Root)(t, root)
		}
		report := validate(t, store)
		if len(report.Problems) != 0 || report.Truncated {
			t.Fatalf("Validate empty evidence = %+v", report)
		}
	}
}

func TestValidateSymlinks(t *testing.T) {
	for _, path := range []string{evidence.Root, commitPath, entryPath, metadataPath, textPath} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			root, store := added(t)
			outside := t.TempDir()
			target := filepath.Join(outside, "outside")
			// A matching file would pass size and digest checks if followed.
			write(t, outside, "outside", []byte(textContent))
			replace(t, root, path, target)
			problem := wantProblem(t, store, path, storage.Symlink)
			if strings.Contains(problem.Message, target) {
				t.Fatalf("Validate disclosed symlink target in %+v", problem)
			}
		})
	}
}

func wantValidationError(t *testing.T, root, path string, report storage.Report, err, category error) {
	t.Helper()

	wantError(t, err, category)
	wantClean(t, err, root)
	if len(report.Problems) != 0 || report.Truncated || !strings.Contains(err.Error(), path) {
		t.Fatalf("Validate = %+v, %v, want an empty report and an error naming %q", report, err, path)
	}
}
