package fakeforge

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

func seeded(t *testing.T) (*Repo, Fixture) {
	t.Helper()
	r := NewRepo()
	return r, Seed(r)
}

func TestSeedIsDeterministic(t *testing.T) {
	_, a := seeded(t)
	_, b := seeded(t)
	if a.Head != b.Head || a.TagOuter != b.TagOuter {
		t.Fatalf("seed IDs differ: %s %s", a.Head, b.Head)
	}
}

func TestSeedContent(t *testing.T) {
	r, fx := seeded(t)
	want := map[string]gitobj.Mode{
		PathExec:      gitobj.ModeExec,
		PathSymlink:   gitobj.ModeSymlink,
		PathSubmodule: gitobj.ModeSubmodule,
		PathBinary:    gitobj.ModeFile,
		PathUnicode:   gitobj.ModeFile,
		"docs/guide":  gitobj.ModeTree,
	}
	for p, mode := range want {
		e, err := r.Lookup(fx.Head, p)
		if err != nil || e.Mode != mode {
			t.Errorf("Lookup(%q) = %v, %v; want mode %v", p, e, err, mode)
		}
	}
	if e, _ := r.Lookup(fx.Head, PathSubmodule); e.ID != SubmoduleCommit {
		t.Errorf("submodule = %s", e.ID)
	}
	data, err := r.ReadBlob(fx.Blobs[PathLarge])
	if err != nil || len(data) != LargeSize || data[len(data)-1] != '\n' {
		t.Errorf("large file: %d bytes, %v", len(data), err)
	}
	if data, _ := r.ReadBlob(fx.Blobs[PathSymlink]); string(data) != PathReadme {
		t.Errorf("symlink target = %q", data)
	}
	if data, _ := r.ReadBlob(fx.Blobs[PathLFS]); string(data) != LFSPointer {
		t.Errorf("LFS pointer = %q", data)
	}
	if _, err := r.Lookup(fx.Rename, PathOldName); !errors.Is(err, ErrNotFound) {
		t.Errorf("old name after rename: %v", err)
	}
	old, _ := r.Lookup(fx.Main[2], PathOldName)
	moved, _ := r.Lookup(fx.Rename, PathNewName)
	if old.ID != moved.ID {
		t.Error("rename changed the blob")
	}
	c, _ := r.ReadCommit(fx.Merge)
	if !slices.Equal(c.Parents, []gitobj.ID{fx.Main[4], fx.Feature[1]}) {
		t.Errorf("merge parents = %v", c.Parents)
	}
	for _, tc := range []struct {
		ref  string
		want gitobj.ID
	}{
		{"refs/heads/main", fx.Head},
		{"refs/heads/feature", fx.Feature[2]},
		{"refs/tags/chain", fx.Main[1]},
		{"refs/tags/v1.0", fx.Main[3]},
		{"refs/tags/light", fx.Main[0]},
		{"refs/heads/clash", fx.ClashBranch},
		{"refs/tags/clash", fx.ClashTag},
	} {
		id, ok := r.Ref(tc.ref)
		got, err := r.PeelCommit(id)
		if !ok || err != nil || got != tc.want {
			t.Errorf("%s peels to %s, %v", tc.ref, got, err)
		}
	}
	tag, _ := r.ReadTag(fx.TagOuter)
	if tag.Object != fx.TagInner || tag.Type != gitobj.TypeTag {
		t.Errorf("outer tag = %+v", tag)
	}
	if head, _ := r.Head(); head != fx.Head {
		t.Errorf("Head = %s", head)
	}
	if got := len(r.Refs("refs/tags/")); got != 4 {
		t.Errorf("%d tags", got)
	}
}

func TestReadErrors(t *testing.T) {
	r, fx := seeded(t)
	missing := gitobj.Hash(gitobj.TypeBlob, []byte("missing"))
	if _, err := r.ReadBlob(missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadBlob(missing) = %v", err)
	}
	if _, err := r.ReadTree(fx.Head); !errors.Is(err, ErrWrongType) {
		t.Errorf("ReadTree(commit) = %v", err)
	}
	if _, err := r.PeelCommit(fx.Blobs[PathReadme]); !errors.Is(err, ErrWrongType) {
		t.Errorf("PeelCommit(blob) = %v", err)
	}
	for _, p := range []string{"nope", "README.md/x", "docs//guide", "docs/", "/docs"} {
		if _, err := r.Lookup(fx.Head, p); !errors.Is(err, ErrNotFound) {
			t.Errorf("Lookup(%q) = %v", p, err)
		}
	}
	if _, err := r.Lookup(fx.Blobs[PathReadme], ""); !errors.Is(err, ErrWrongType) {
		t.Errorf("Lookup(blob) = %v", err)
	}
	if _, err := r.Put(gitobj.Object{Type: gitobj.TypeTree, Data: []byte("junk")}); err == nil {
		t.Error("Put accepted a bad tree")
	}
}

func TestLookupRoots(t *testing.T) {
	r, fx := seeded(t)
	c, _ := r.ReadCommit(fx.Main[3])
	for _, root := range []gitobj.ID{fx.Main[3], fx.Tag, c.Tree} {
		e, err := r.Lookup(root, "")
		if err != nil || e.ID != c.Tree || e.Mode != gitobj.ModeTree {
			t.Errorf("Lookup(%s, \"\") = %v, %v", root, e, err)
		}
	}
	e, err := r.Lookup(fx.Head, PathNested)
	if err != nil || e.Name != "intro.md" {
		t.Errorf("nested = %v, %v", e, err)
	}
}

func TestListTree(t *testing.T) {
	r, fx := seeded(t)
	flat, err := r.ListTree(fx.Head, false)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range flat {
		names = append(names, e.Path)
	}
	want := []string{"README.md", "assets", "bin", "dir with space", "docs", "feature.txt", "large.txt", "link", "new-name.txt", "src", "vendor"}
	if !slices.Equal(names, want) {
		t.Errorf("flat = %q", names)
	}
	all, err := r.ListTree(fx.Head, true)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]PathEntry{}
	for _, e := range all {
		byPath[e.Path] = e
	}
	if e := byPath[PathLarge]; e.Size != LargeSize {
		t.Errorf("large size = %d", e.Size)
	}
	if e := byPath["docs/guide"]; e.Mode != gitobj.ModeTree || e.Size != 0 {
		t.Errorf("docs/guide = %+v", e)
	}
	if _, ok := byPath[PathNested]; !ok {
		t.Error("recursive listing misses nested file")
	}
	if i, j := slices.IndexFunc(all, func(e PathEntry) bool { return e.Path == "docs" }),
		slices.IndexFunc(all, func(e PathEntry) bool { return e.Path == PathNested }); i > j {
		t.Error("tree entry listed after its children")
	}
}

func TestLog(t *testing.T) {
	r, fx := seeded(t)
	m, f := fx.Main, fx.Feature
	for _, tc := range []struct {
		name string
		tip  gitobj.ID
		o    LogOptions
		want []gitobj.ID
	}{
		{"all", fx.Head, LogOptions{}, []gitobj.ID{fx.Head, fx.Merge, m[4], f[1], m[3], f[0], m[2], m[1], m[0]}},
		{"tag tip", fx.TagOuter, LogOptions{}, []gitobj.ID{m[1], m[0]}},
		{"side path", fx.Head, LogOptions{Path: PathFeature}, []gitobj.ID{f[1], f[0]}},
		{"main path", fx.Head, LogOptions{Path: PathReadme}, []gitobj.ID{m[4], m[0]}},
		{"renamed", fx.Head, LogOptions{Path: PathOldName}, []gitobj.ID{m[3], m[0]}},
		{"dir", fx.Head, LogOptions{Path: "docs"}, []gitobj.ID{fx.Head, m[0]}},
		{"missing path", fx.Head, LogOptions{Path: "nope"}, nil},
		{"not main", f[2], LogOptions{Not: []gitobj.ID{fx.Head}}, []gitobj.ID{f[2]}},
		{"not feature", fx.Head, LogOptions{Not: []gitobj.ID{f[2]}}, []gitobj.ID{fx.Head, fx.Merge, m[4], m[3]}},
		{"not self", fx.Head, LogOptions{Not: []gitobj.ID{fx.Head}}, nil},
		{"not with path", fx.Head, LogOptions{Path: PathFeature, Not: []gitobj.ID{m[4]}}, []gitobj.ID{f[1], f[0]}},
	} {
		got, err := r.Log(tc.tip, tc.o)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%s: Log = %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
	if _, err := r.Log(fx.Blobs[PathReadme], LogOptions{}); !errors.Is(err, ErrWrongType) {
		t.Errorf("Log(blob) = %v", err)
	}
}

func TestLogTiesKeepInsertionOrder(t *testing.T) {
	r := NewRepo()
	tree := r.BuildTree(nil)
	when := Epoch
	root := r.CommitTree(tree, when, "root")
	a := r.CommitTree(tree, when, "a", root)
	b := r.CommitTree(tree, when, "b", root)
	m := r.CommitTree(tree, when, "m", a, b)
	got, _ := r.Log(m, LogOptions{})
	if want := []gitobj.ID{m, a, b, root}; !slices.Equal(got, want) {
		t.Errorf("Log = %v, want %v", got, want)
	}
}

func TestMergeBase(t *testing.T) {
	r, fx := seeded(t)
	for _, tc := range []struct {
		a, b, want gitobj.ID
	}{
		{fx.Head, fx.Feature[2], fx.MergeBase},
		{fx.Main[4], fx.Feature[0], fx.Main[2]},
		{fx.Head, fx.Main[1], fx.Main[1]},
		{fx.Main[1], fx.Head, fx.Main[1]},
		{fx.Tag, fx.TagOuter, fx.Main[1]},
	} {
		got, ok, err := r.MergeBase(tc.a, tc.b)
		if err != nil || !ok || got != tc.want {
			t.Errorf("MergeBase(%s, %s) = %s, %v, %v; want %s", tc.a, tc.b, got, ok, err, tc.want)
		}
	}
	for _, tc := range []struct {
		a, b gitobj.ID
		want bool
	}{
		{fx.Main[0], fx.Head, true},
		{fx.Feature[1], fx.Head, true},
		{fx.Feature[2], fx.Head, false},
		{fx.Head, fx.Head, true},
		{fx.Head, fx.Main[0], false},
	} {
		if got, err := r.IsAncestor(tc.a, tc.b); err != nil || got != tc.want {
			t.Errorf("IsAncestor(%s, %s) = %v, %v", tc.a, tc.b, got, err)
		}
	}
}

// crissCross builds two roots-joined histories whose tips have two merge
// bases, plus an unrelated root.
func crissCross(r *Repo) (x, y, b1, b2, lone gitobj.ID) {
	at := func(h int) time.Time { return Epoch.Add(time.Duration(h) * time.Hour) }
	tree := func(s string) gitobj.ID { return r.BuildTree(map[string]File{"f": {Data: []byte(s)}}) }
	root := r.CommitTree(tree("0"), at(0), "root")
	b1 = r.CommitTree(tree("1"), at(1), "b1", root)
	b2 = r.CommitTree(tree("2"), at(2), "b2", root)
	x = r.CommitTree(tree("x"), at(3), "x", b1, b2)
	y = r.CommitTree(tree("y"), at(4), "y", b2, b1)
	lone = r.CommitTree(tree("lone"), at(5), "lone")
	return
}

func TestMergeBasesCrissCross(t *testing.T) {
	r := NewRepo()
	x, y, b1, b2, lone := crissCross(r)
	got, err := r.MergeBases(x, y)
	if want := []gitobj.ID{b2, b1}; err != nil || !slices.Equal(got, want) {
		t.Errorf("MergeBases = %v, %v; want %v", got, err, want)
	}
	if _, ok, err := r.MergeBase(x, lone); ok || err != nil {
		t.Errorf("unrelated MergeBase = %v, %v", ok, err)
	}
}

func TestUpdateRefs(t *testing.T) {
	r, fx := seeded(t)
	main := "refs/heads/main"
	missing := gitobj.Hash(gitobj.TypeCommit, []byte("missing"))
	for _, tc := range []struct {
		name string
		u    []RefUpdate
		err  error
	}{
		{"stale", []RefUpdate{{main, fx.Main[0], fx.Main[1]}}, ErrStale},
		{"exists", []RefUpdate{{main, gitobj.ZeroID, fx.Main[1]}}, ErrStale},
		{"missing object", []RefUpdate{{main, fx.Head, missing}}, ErrNotFound},
		{"repeated", []RefUpdate{{main, fx.Head, fx.Main[1]}, {main, fx.Head, fx.Main[1]}}, gitobj.ErrInvalid},
		{"bad name", []RefUpdate{{"main", fx.Head, fx.Main[1]}}, gitobj.ErrInvalid},
		{"atomic", []RefUpdate{{main, fx.Head, fx.Main[1]}, {"refs/heads/feature", fx.Head, fx.Main[1]}}, ErrStale},
	} {
		if err := r.UpdateRefs(tc.u...); !errors.Is(err, tc.err) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.err)
		}
	}
	if id, _ := r.Ref(main); id != fx.Head {
		t.Fatalf("failed update moved main to %s", id)
	}
	err := r.UpdateRefs(
		RefUpdate{main, fx.Head, fx.Main[4]},
		RefUpdate{"refs/heads/new", gitobj.ZeroID, fx.Head},
		RefUpdate{"refs/heads/clash", fx.ClashBranch, gitobj.ZeroID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := r.Ref(main); id != fx.Main[4] {
		t.Errorf("main = %s", id)
	}
	if id, _ := r.Ref("refs/heads/new"); id != fx.Head {
		t.Errorf("new = %s", id)
	}
	if _, ok := r.Ref("refs/heads/clash"); ok {
		t.Error("clash branch not deleted")
	}
}

func TestUpdateRefsRace(t *testing.T) {
	r, fx := seeded(t)
	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			errs[i] = r.UpdateRefs(RefUpdate{"refs/heads/main", fx.Head, fx.Main[i%5]})
			_, _ = r.Log(fx.Head, LogOptions{Path: PathReadme})
		})
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrStale):
			t.Error(err)
		}
	}
	if ok != 1 {
		t.Errorf("%d updates won, want 1", ok)
	}
}

func TestBuilderPanics(t *testing.T) {
	r := NewRepo()
	missing := gitobj.Hash(gitobj.TypeBlob, []byte("missing"))
	blob := r.WriteBlob([]byte("x"))
	for name, fn := range map[string]func(){
		"tree missing blob": func() { r.WriteTree(gitobj.TreeEntry{Name: "a", Mode: gitobj.ModeFile, ID: missing}) },
		"tree wrong type":   func() { r.WriteTree(gitobj.TreeEntry{Name: "a", Mode: gitobj.ModeTree, ID: blob}) },
		"tree bad name":     func() { r.WriteTree(gitobj.TreeEntry{Name: ".git", Mode: gitobj.ModeFile, ID: blob}) },
		"file and dir":      func() { r.BuildTree(map[string]File{"a": {}, "a/b": {}}) },
		"commit no tree":    func() { r.CommitTree(missing, Epoch, "m") },
		"tag no target":     func() { r.WriteTag(gitobj.Tag{Object: missing, Name: "t"}) },
		"tag wrong type":    func() { r.WriteTag(gitobj.Tag{Object: blob, Type: gitobj.TypeCommit, Name: "t"}) },
		"bad ref":           func() { r.SetRef("main", blob) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			fn()
		}()
	}
}

func TestRepoSettings(t *testing.T) {
	r := NewRepo()
	r.SetDefaultBranch("trunk")
	r.SetPrivate(true)
	if r.DefaultBranch() != "trunk" || !r.Private() || r.ID() != 0 {
		t.Error("settings not kept")
	}
	if _, ok := r.Head(); ok {
		t.Error("empty repo has a head")
	}
	blob := r.WriteBlob([]byte("x"))
	r.SetRef("refs/heads/trunk", blob)
	r.SetRef("refs/heads/trunk", gitobj.ZeroID)
	if _, ok := r.Ref("refs/heads/trunk"); ok {
		t.Error("zero SetRef did not delete")
	}
	o, ok := r.Object(blob)
	o.Data[0] = 'y'
	if again, _ := r.Object(blob); !ok || string(again.Data) != "x" {
		t.Error("Object shares its data")
	}
	if ids := r.IDs(); len(ids) != 1 || ids[0] != blob {
		t.Errorf("IDs = %v", ids)
	}
}

func TestPeel(t *testing.T) {
	r, fx := seeded(t)
	c, _ := r.ReadCommit(fx.Main[0])
	tagTree := r.WriteTag(gitobj.Tag{Object: c.Tree, Name: "tree-tag", Message: "t\n"})
	for _, tc := range []struct {
		in, want gitobj.ID
		typ      gitobj.Type
	}{
		{fx.TagOuter, fx.Main[1], gitobj.TypeCommit},
		{fx.Head, fx.Head, gitobj.TypeCommit},
		{tagTree, c.Tree, gitobj.TypeTree},
	} {
		got, typ, err := r.Peel(tc.in)
		if err != nil || got != tc.want || typ != tc.typ {
			t.Errorf("Peel(%s) = %s, %s, %v", tc.in, got, typ, err)
		}
	}
	if _, err := r.PeelCommit(tagTree); !errors.Is(err, ErrWrongType) {
		t.Errorf("PeelCommit(tree tag) = %v", err)
	}
	if _, _, err := r.Peel(gitobj.Hash(gitobj.TypeTag, nil)); !errors.Is(err, ErrNotFound) {
		t.Errorf("Peel(missing) = %v", err)
	}
}
