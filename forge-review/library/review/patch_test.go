package review

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGitHeaderPathBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		header    string
		metadata  string
		oldPath   string
		newPath   string
		ambiguous bool
	}{
		{
			name:      "ambiguous binary",
			header:    "a/dir b/f b/dir b/f",
			metadata:  "Binary files a/dir b/f and b/dir b/f differ\n",
			ambiguous: true,
		},
		{
			name:      "ambiguous rename",
			header:    "a/dir b/f b/other",
			metadata:  "similarity index 100%\nrename from dir b/f\nrename to other\n",
			ambiguous: true,
		},
		{
			name:     "quoted binary",
			header:   `"a/dir b/f" "b/dir b/f"`,
			metadata: "Binary files a/dir b/f and b/dir b/f differ\n",
			oldPath:  "dir b/f",
			newPath:  "dir b/f",
		},
		{
			name:     "quoted rename",
			header:   `"a/dir b/f" "b/other"`,
			metadata: "similarity index 100%\nrename from dir b/f\nrename to other\n",
			oldPath:  "dir b/f",
			newPath:  "other",
		},
		{
			name:     "ordinary spaces",
			header:   "a/dir space/f b/dir space/f",
			metadata: "Binary files a/dir space/f and b/dir space/f differ\n",
			oldPath:  "dir space/f",
			newPath:  "dir space/f",
		},
		{
			name:     "quoted destination",
			header:   `a/dir b/f "b/other b/f"`,
			metadata: "similarity index 100%\nrename from dir b/f\nrename to other b/f\n",
			oldPath:  "dir b/f",
			newPath:  "other b/f",
		},
		{
			name:     "quoted source",
			header:   `"a/dir b/f" b/other b/f`,
			metadata: "similarity index 100%\nrename from dir b/f\nrename to other b/f\n",
			oldPath:  "dir b/f",
			newPath:  "other b/f",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &MemoryStore{}
			c, err := New(store)
			if err != nil {
				t.Fatal(err)
			}

			ctx := context.Background()
			s, err := c.CreateSubject(ctx, Input{Kind: "patch", Patch: "diff --git " + tc.header + "\n" + tc.metadata})
			if tc.ambiguous {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("ambiguous snapshot accepted: %+v, %v", s, err)
				}

				state, err := store.Read(ctx)
				if err != nil || len(state.Subjects) != 0 {
					t.Fatal("invalid snapshot persisted", state, err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}
			if len(s.Files) != 1 || s.Files[0].OldPath != tc.oldPath || s.Files[0].Path != tc.newPath {
				t.Fatalf("wrong paths: %+v", s.Files)
			}

			for _, a := range []Anchor{{Path: tc.oldPath, Side: "old"}, {Path: tc.newPath, Side: "new"}} {
				if err = ValidateAnchor(s, &a); err != nil {
					t.Fatalf("file anchor %+v: %v", a, err)
				}
			}
		})
	}
}

func TestPatchFileTransitions(t *testing.T) {
	first := strings.Join([]string{
		"--- a/first.go",
		"+++ b/first.go",
		"@@ -1,2 +1,2 @@",
		" shared",
		"-before",
		"+after",
		"",
	}, "\n")
	second := strings.Join([]string{
		"--- /dev/null",
		"+++ b/second.go",
		"@@ -0,0 +1 @@",
		"+created",
		"",
	}, "\n")

	data, err := parsePatch(first + second)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.files) != 2 || data.files[1].OldPath != "" {
		t.Fatalf("file boundaries lost: %+v", data.files)
	}
	if data.lines[fileSide{"first.go", "old"}][2] != "before" ||
		data.lines[fileSide{"second.go", "new"}][1] != "created" {
		t.Fatal("lines crossed file boundaries")
	}
	if data.pairs[fileSide{"first.go", "old"}][1] != 1 ||
		data.pairs[fileSide{"first.go", "new"}][1] != 1 {
		t.Fatal("unchanged line pairing was lost")
	}

	if _, err := parsePatch(first + first); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate file accepted: %v", err)
	}

	unfinished := strings.Replace(first, "@@ -1,2 +1,2 @@", "@@ -1,3 +1,3 @@", 1)
	if _, err := parsePatch(unfinished + second); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unfinished hunk accepted at a file boundary: %v", err)
	}
}
