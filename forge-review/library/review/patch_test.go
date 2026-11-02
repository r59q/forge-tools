package review

import (
	"context"
	"errors"
	"testing"
)

func TestGitHeaderPathBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, header, metadata, oldPath, newPath string
		ambiguous                                bool
	}{
		{name: "ambiguous binary", header: "a/dir b/f b/dir b/f", metadata: "Binary files a/dir b/f and b/dir b/f differ\n", ambiguous: true},
		{name: "ambiguous rename", header: "a/dir b/f b/other", metadata: "similarity index 100%\nrename from dir b/f\nrename to other\n", ambiguous: true},
		{name: "quoted binary", header: `"a/dir b/f" "b/dir b/f"`, metadata: "Binary files a/dir b/f and b/dir b/f differ\n", oldPath: "dir b/f", newPath: "dir b/f"},
		{name: "quoted rename", header: `"a/dir b/f" "b/other"`, metadata: "similarity index 100%\nrename from dir b/f\nrename to other\n", oldPath: "dir b/f", newPath: "other"},
		{name: "ordinary spaces", header: "a/dir space/f b/dir space/f", metadata: "Binary files a/dir space/f and b/dir space/f differ\n", oldPath: "dir space/f", newPath: "dir space/f"},
		{name: "quoted destination", header: `a/dir b/f "b/other b/f"`, metadata: "similarity index 100%\nrename from dir b/f\nrename to other b/f\n", oldPath: "dir b/f", newPath: "other b/f"},
		{name: "quoted source", header: `"a/dir b/f" b/other b/f`, metadata: "similarity index 100%\nrename from dir b/f\nrename to other b/f\n", oldPath: "dir b/f", newPath: "other b/f"},
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
