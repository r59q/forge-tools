package code

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/fakeforge"
	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Submodule commits used by the Compare fixture.
var (
	cmpSub1 = gitobj.Hash(gitobj.TypeCommit, []byte("compare submodule one"))
	cmpSub2 = gitobj.Hash(gitobj.TypeCommit, []byte("compare submodule two"))
)

// cmpFile returns a regular file for BuildTree.
func cmpFile(data string) *fakeforge.File { return &fakeforge.File{Data: []byte(data)} }

// cmpBase is the tree of refs/heads/base.
func cmpBase() map[string]fakeforge.File {
	files := map[string]fakeforge.File{
		"README.md":    {Data: []byte("hello\n")},
		"a/b/c.txt":    {Data: []byte("c1\n")},
		"a/y/keep.txt": {Data: []byte("keep\n")},
		"x/x.txt":      {Data: []byte("x\n")},
		"run.sh":       {Data: []byte("#!/bin/sh\n")},
		"link":         {Mode: gitobj.ModeSymlink, Data: []byte("README.md")},
		"tosym":        {Data: []byte("target\n")},
		"tosub":        {Data: []byte("sub\n")},
		"doc":          {Data: []byte("doc\n")},
		"dir2/f.txt":   {Data: []byte("f\n")},
		"sub":          {Mode: gitobj.ModeSubmodule, Submodule: cmpSub1},
		"p1.txt":       {Data: []byte("dup\n")},
		"p2.txt":       {Data: []byte("dup\n")},
		"img.bin":      {Data: fakeforge.Binary},
		"crlf.txt":     {Data: []byte("a\r\nb\r\n")},
		"nonl.txt":     {Data: []byte("a\nb")},
	}
	for i := 1; i <= 3; i++ {
		files[fmt.Sprintf("t%d.txt", i)] = fakeforge.File{Data: []byte(fmt.Sprintf("line one of t%d\nline two\n", i))}
	}
	return files
}

// cmpVariants are children of base, each stored as refs/heads/<name>. A
// nil File deletes the path.
var cmpVariants = map[string]map[string]*fakeforge.File{
	"add":    {"new.txt": cmpFile("new\n")},
	"delete": {"README.md": nil},
	"modify": {"README.md": cmpFile("hello\nworld\n")},
	"rename": {"p1.txt": nil, "p2.txt": nil, "a/p2.txt": cmpFile("dup\n"), "b.txt": cmpFile("dup\n")},
	"mode":   {"run.sh": {Mode: gitobj.ModeExec, Data: []byte("#!/bin/sh\n")}},
	"types": {
		"link":  cmpFile("plain\n"),
		"tosym": {Mode: gitobj.ModeSymlink, Data: []byte("README.md")},
		"tosub": {Mode: gitobj.ModeSubmodule, Submodule: cmpSub1},
	},
	"filedir": {"doc": nil, "doc/inner.txt": cmpFile("inner\n"), "dir2/f.txt": nil, "dir2": cmpFile("now a file\n")},
	"submod":  {"sub": {Mode: gitobj.ModeSubmodule, Submodule: cmpSub2}},
	"nested":  {"a/b/c.txt": cmpFile("c2\n")},
	"multi": {
		"README.md": cmpFile("hi\n"), "a/b/c.txt": cmpFile("c2\n"),
		"x/x.txt": cmpFile("x2\n"), "a/y/new.txt": cmpFile("new\n"),
	},
	"many": {
		"many/f0": cmpFile("0\n"), "many/f1": cmpFile("1\n"), "many/f2": cmpFile("2\n"), "many/f3": cmpFile("3\n"),
		"many/f4": cmpFile("4\n"), "many/f5": cmpFile("5\n"), "many/f6": cmpFile("6\n"), "many/f7": cmpFile("7\n"),
		"many/f8": cmpFile("8\n"), "many/f9": cmpFile("9\n"),
	},
	"patchy": {
		"t1.txt": cmpFile("line one of t1\nline 2\n"),
		"t2.txt": cmpFile("line one of t2\nline 2\n"),
		"t3.txt": cmpFile("line one of t3\nline 2\n"),
	},
	"binary": {"img.bin": {Data: append(append([]byte{}, fakeforge.Binary...), "more"...)}},
	"crlf":   {"crlf.txt": cmpFile("a\r\nc\r\n")},
	"nonl":   {"nonl.txt": cmpFile("a\nc")},
	"empty":  {},
}

// cmpEnv is one fake forge with the Compare fixture.
type cmpEnv struct {
	kind forgeauth.Kind
	s    *fakeforge.Server
	r    *fakeforge.Repo
	ref  RepositoryRef
}

func newCmpEnvs(t *testing.T) []*cmpEnv {
	t.Helper()
	var envs []*cmpEnv
	for _, f := range confForges {
		s := fakeforge.NewServer(t, f.kind, f.front)
		r := s.NewRepo(f.repo)
		base := cmpBase()
		b0 := r.CommitTree(r.BuildTree(base), fakeforge.Epoch, "base\n")
		r.SetRef("refs/heads/base", b0)
		r.SetRef("refs/heads/main", b0)
		for name, edits := range cmpVariants {
			files := maps.Clone(base)
			for p, file := range edits {
				if file == nil {
					delete(files, p)
				} else {
					files[p] = *file
				}
			}
			id := r.CommitTree(r.BuildTree(files), fakeforge.Epoch.Add(time.Hour), name+"\n", b0)
			r.SetRef("refs/heads/"+name, id)
		}
		envs = append(envs, &cmpEnv{kind: f.kind, s: s, r: r, ref: RepositoryRef{Source: "src", Repository: f.repo}})
	}
	return envs
}

// client returns a Client for e with limits l.
func (e *cmpEnv) client(t *testing.T, l Limits) *Client {
	t.Helper()
	c, err := NewWithLimits(l, Source{ID: "src", Session: e.s.Session})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// id returns the commit of branch name.
func (e *cmpEnv) id(name string) OID {
	id, ok := e.r.Ref("refs/heads/" + name)
	if !ok {
		panic("no branch " + name)
	}
	return confOID(id)
}

// cmpBlob returns the blob ID of data.
func cmpBlob(data string) OID { return confOID(gitobj.Hash(gitobj.TypeBlob, []byte(data))) }

func cmpAdd(p string, m FileMode, data string) Change {
	return Change{Status: Added, Path: p, Mode: m, ID: cmpBlob(data)}
}

func cmpDel(p string, m FileMode, data string) Change {
	return Change{Status: Deleted, OldPath: p, OldMode: m, OldID: cmpBlob(data)}
}

func cmpMod(p string, om FileMode, od string, m FileMode, d string) Change {
	return Change{Status: Modified, OldPath: p, Path: p, OldMode: om, Mode: m, OldID: cmpBlob(od), ID: cmpBlob(d)}
}

// withPatch returns c with a patch and line counts.
func withPatch(c Change, patch string, adds, dels int) Change {
	c.Patch, c.Additions, c.Deletions = patch, adds, dels
	return c
}

// cmpCase is one Compare call; want returns the expected Diff.
type cmpCase struct {
	name       string
	base, head string
	opts       CompareOptions
	want       func(e *cmpEnv) Diff
	err        string // expected confErr class
}

// cmpChild expects head, a child of base, compared in MergeBase mode.
func cmpChild(head string, changes ...Change) func(e *cmpEnv) Diff {
	return func(e *cmpEnv) Diff {
		return Diff{Base: e.id("base"), Head: e.id(head), MergeBase: e.id("base"), Changes: append([]Change{}, changes...)}
	}
}

func cmpCases() []cmpCase {
	var many []Change
	for i := range 10 {
		many = append(many, cmpAdd(fmt.Sprintf("many/f%d", i), ModeFile, fmt.Sprintf("%d\n", i)))
	}
	t1 := cmpMod("t1.txt", ModeFile, "line one of t1\nline two\n", ModeFile, "line one of t1\nline 2\n")
	t2 := cmpMod("t2.txt", ModeFile, "line one of t2\nline two\n", ModeFile, "line one of t2\nline 2\n")
	t3 := cmpMod("t3.txt", ModeFile, "line one of t3\nline two\n", ModeFile, "line one of t3\nline 2\n")
	tPatch := func(c Change, n int) Change {
		return withPatch(c, fmt.Sprintf("@@ -1,2 +1,2 @@\n line one of t%d\n-line two\n+line 2\n", n), 1, 1)
	}
	omitted := func(c Change, counts bool) Change {
		c.PatchOmitted = true
		if counts {
			c.Additions, c.Deletions = 1, 1
		}
		return c
	}
	return []cmpCase{
		{name: "add", base: "base", head: "add",
			want: cmpChild("add", cmpAdd("new.txt", ModeFile, "new\n"))},
		{name: "add patch", base: "base", head: "add", opts: CompareOptions{Patches: true},
			want: cmpChild("add", withPatch(cmpAdd("new.txt", ModeFile, "new\n"), "@@ -0,0 +1 @@\n+new\n", 1, 0))},
		{name: "delete", base: "base", head: "delete", opts: CompareOptions{Patches: true},
			want: cmpChild("delete", withPatch(cmpDel("README.md", ModeFile, "hello\n"), "@@ -1 +0,0 @@\n-hello\n", 0, 1))},
		{name: "modify", base: "base", head: "modify", opts: CompareOptions{Patches: true},
			want: cmpChild("modify", withPatch(cmpMod("README.md", ModeFile, "hello\n", ModeFile, "hello\nworld\n"),
				"@@ -1 +1,2 @@\n hello\n+world\n", 1, 0))},
		{name: "modify no context", base: "base", head: "modify", opts: CompareOptions{Patches: true, Context: -1},
			want: cmpChild("modify", withPatch(cmpMod("README.md", ModeFile, "hello\n", ModeFile, "hello\nworld\n"),
				"@@ -1,0 +2 @@\n+world\n", 1, 0))},
		{name: "rename", base: "base", head: "rename", opts: CompareOptions{Patches: true},
			want: cmpChild("rename",
				Change{Status: Renamed, OldPath: "p2.txt", Path: "a/p2.txt", OldMode: ModeFile, Mode: ModeFile, OldID: cmpBlob("dup\n"), ID: cmpBlob("dup\n")},
				Change{Status: Renamed, OldPath: "p1.txt", Path: "b.txt", OldMode: ModeFile, Mode: ModeFile, OldID: cmpBlob("dup\n"), ID: cmpBlob("dup\n")})},
		{name: "exec bit", base: "base", head: "mode", opts: CompareOptions{Patches: true},
			want: cmpChild("mode", cmpMod("run.sh", ModeFile, "#!/bin/sh\n", ModeExec, "#!/bin/sh\n"))},
		{name: "type changes", base: "base", head: "types", opts: CompareOptions{Patches: true},
			want: cmpChild("types",
				withPatch(Change{Status: TypeChanged, OldPath: "link", Path: "link", OldMode: ModeSymlink, Mode: ModeFile,
					OldID: cmpBlob("README.md"), ID: cmpBlob("plain\n")},
					"@@ -1 +1 @@\n-README.md\n\\ No newline at end of file\n+plain\n", 1, 1),
				withPatch(Change{Status: TypeChanged, OldPath: "tosub", Path: "tosub", OldMode: ModeFile, Mode: ModeSubmodule,
					OldID: cmpBlob("sub\n"), ID: confOID(cmpSub1)},
					"@@ -1 +1 @@\n-sub\n+Subproject commit "+cmpSub1.String()+"\n", 1, 1),
				withPatch(Change{Status: TypeChanged, OldPath: "tosym", Path: "tosym", OldMode: ModeFile, Mode: ModeSymlink,
					OldID: cmpBlob("target\n"), ID: cmpBlob("README.md")},
					"@@ -1 +1 @@\n-target\n+README.md\n\\ No newline at end of file\n", 1, 1))},
		{name: "file and dir", base: "base", head: "filedir",
			want: cmpChild("filedir",
				cmpAdd("dir2", ModeFile, "now a file\n"),
				cmpDel("dir2/f.txt", ModeFile, "f\n"),
				cmpDel("doc", ModeFile, "doc\n"),
				cmpAdd("doc/inner.txt", ModeFile, "inner\n"))},
		{name: "file and dir reversed", base: "filedir", head: "base", opts: CompareOptions{Mode: Direct},
			want: func(e *cmpEnv) Diff {
				return Diff{Base: e.id("filedir"), Head: e.id("base"), Changes: []Change{
					cmpDel("dir2", ModeFile, "now a file\n"),
					cmpAdd("dir2/f.txt", ModeFile, "f\n"),
					cmpAdd("doc", ModeFile, "doc\n"),
					cmpDel("doc/inner.txt", ModeFile, "inner\n"),
				}}
			}},
		{name: "submodule", base: "base", head: "submod", opts: CompareOptions{Patches: true},
			want: cmpChild("submod", withPatch(Change{Status: Modified, OldPath: "sub", Path: "sub", OldMode: ModeSubmodule,
				Mode: ModeSubmodule, OldID: confOID(cmpSub1), ID: confOID(cmpSub2)},
				"@@ -1 +1 @@\n-Subproject commit "+cmpSub1.String()+"\n+Subproject commit "+cmpSub2.String()+"\n", 1, 1))},
		{name: "nested", base: "base", head: "nested",
			want: cmpChild("nested", cmpMod("a/b/c.txt", ModeFile, "c1\n", ModeFile, "c2\n"))},
		{name: "paths", base: "base", head: "multi", opts: CompareOptions{Paths: []string{"a/b", "x/x.txt"}},
			want: cmpChild("multi",
				cmpMod("a/b/c.txt", ModeFile, "c1\n", ModeFile, "c2\n"),
				cmpMod("x/x.txt", ModeFile, "x\n", ModeFile, "x2\n"))},
		{name: "paths prefix is by component", base: "base", head: "multi", opts: CompareOptions{Paths: []string{"a/y/new", "READ"}},
			want: cmpChild("multi")},
		{name: "paths below new dir", base: "base", head: "filedir", opts: CompareOptions{Paths: []string{"doc/inner.txt"}},
			want: cmpChild("filedir", cmpAdd("doc/inner.txt", ModeFile, "inner\n"))},
		{name: "paths below deleted dir", base: "base", head: "filedir", opts: CompareOptions{Paths: []string{"dir2/f.txt"}},
			want: cmpChild("filedir", cmpDel("dir2/f.txt", ModeFile, "f\n"))},
		{name: "all multi", base: "base", head: "multi",
			want: cmpChild("multi",
				cmpMod("README.md", ModeFile, "hello\n", ModeFile, "hi\n"),
				cmpMod("a/b/c.txt", ModeFile, "c1\n", ModeFile, "c2\n"),
				cmpAdd("a/y/new.txt", ModeFile, "new\n"),
				cmpMod("x/x.txt", ModeFile, "x\n", ModeFile, "x2\n"))},
		{name: "max changes", base: "base", head: "many", opts: CompareOptions{MaxChanges: 3},
			want: func(e *cmpEnv) Diff {
				d := cmpChild("many", many[:3]...)(e)
				d.Truncated = true
				return d
			}},
		{name: "max changes exact", base: "base", head: "many", opts: CompareOptions{MaxChanges: 10},
			want: cmpChild("many", many...)},
		{name: "max patch files", base: "base", head: "patchy", opts: CompareOptions{Patches: true, MaxPatchFiles: 2},
			want: cmpChild("patchy", tPatch(t1, 1), tPatch(t2, 2), omitted(t3, false))},
		{name: "max patch bytes", base: "base", head: "patchy", opts: CompareOptions{Patches: true, MaxPatchBytes: 10},
			want: cmpChild("patchy", omitted(t1, true), omitted(t2, true), omitted(t3, true))},
		{name: "blob over read cap", base: "base", head: "patchy", opts: CompareOptions{Patches: true, MaxPatchBytes: 1},
			want: cmpChild("patchy", omitted(t1, false), omitted(t2, false), omitted(t3, false))},
		{name: "binary", base: "base", head: "binary", opts: CompareOptions{Patches: true},
			want: func(e *cmpEnv) Diff {
				c := Change{Status: Modified, OldPath: "img.bin", Path: "img.bin", OldMode: ModeFile, Mode: ModeFile,
					OldID: cmpBlob(string(fakeforge.Binary)), ID: cmpBlob(string(fakeforge.Binary) + "more"), Binary: true}
				return cmpChild("binary", c)(e)
			}},
		{name: "crlf", base: "base", head: "crlf", opts: CompareOptions{Patches: true},
			want: cmpChild("crlf", withPatch(cmpMod("crlf.txt", ModeFile, "a\r\nb\r\n", ModeFile, "a\r\nc\r\n"),
				"@@ -1,2 +1,2 @@\n a\r\n-b\r\n+c\r\n", 1, 1))},
		{name: "no newline at end", base: "base", head: "nonl", opts: CompareOptions{Patches: true},
			want: cmpChild("nonl", withPatch(cmpMod("nonl.txt", ModeFile, "a\nb", ModeFile, "a\nc"),
				"@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+c\n\\ No newline at end of file\n", 1, 1))},
		{name: "merge base on diverged branches", base: "modify", head: "add",
			want: func(e *cmpEnv) Diff {
				return Diff{Base: e.id("modify"), Head: e.id("add"), MergeBase: e.id("base"),
					Changes: []Change{cmpAdd("new.txt", ModeFile, "new\n")}}
			}},
		{name: "direct on diverged branches", base: "modify", head: "add", opts: CompareOptions{Mode: Direct},
			want: func(e *cmpEnv) Diff {
				return Diff{Base: e.id("modify"), Head: e.id("add"), Changes: []Change{
					cmpMod("README.md", ModeFile, "hello\nworld\n", ModeFile, "hello\n"),
					cmpAdd("new.txt", ModeFile, "new\n"),
				}}
			}},
		{name: "head behind base", base: "add", head: "base",
			want: func(e *cmpEnv) Diff {
				return Diff{Base: e.id("add"), Head: e.id("base"), MergeBase: e.id("base"), Changes: []Change{}}
			}},
		{name: "identical", base: "base", head: "refs/heads/main",
			want: func(e *cmpEnv) Diff {
				return Diff{Base: e.id("base"), Head: e.id("base"), MergeBase: e.id("base"), Changes: []Change{}}
			}},
		{name: "identical trees", base: "base", head: "empty", opts: CompareOptions{Mode: Direct},
			want: func(e *cmpEnv) Diff {
				return Diff{Base: e.id("base"), Head: e.id("empty"), Changes: []Change{}}
			}},
		{name: "missing base", base: "nope", head: "add", err: "revision-not-found"},
		{name: "missing head", base: "base", head: strings.Repeat("a", 40), err: "revision-not-found"},
		{name: "short sha", base: "base", head: "abc1234", err: "invalid"},
		{name: "bad path", base: "base", head: "add", opts: CompareOptions{Paths: []string{"/a"}}, err: "invalid"},
		{name: "bad mode", base: "base", head: "add", opts: CompareOptions{Mode: "x"}, err: "invalid"},
	}
}

func TestCompare(t *testing.T) {
	envs := newCmpEnvs(t)
	ctx := context.Background()
	clients := make([]*Client, len(envs))
	for i, e := range envs {
		clients[i] = e.client(t, Limits{})
	}
	for _, tc := range cmpCases() {
		t.Run(tc.name, func(t *testing.T) {
			var ref *Diff
			for i, e := range envs {
				got, err := clients[i].Compare(ctx, e.ref, tc.base, tc.head, tc.opts)
				if class := confErr(err); class != tc.err {
					t.Fatalf("%s: error %q (%v), want %q", e.kind, class, err, tc.err)
				}
				if err != nil {
					continue
				}
				if want := tc.want(e); !reflect.DeepEqual(got, want) {
					t.Errorf("%s: got\n%s\nwant\n%s", e.kind, cmpFormat(got), cmpFormat(want))
				}
				if ref == nil {
					ref = &got
				} else if !reflect.DeepEqual(got, *ref) {
					t.Errorf("%s differs from %s", e.kind, envs[0].kind)
				}
			}
		})
	}
}

// cmpFormat prints a Diff one change per line.
func cmpFormat(d Diff) string {
	var b strings.Builder
	fmt.Fprintf(&b, "base %s head %s merge base %s truncated %t\n", d.Base, d.Head, d.MergeBase, d.Truncated)
	for _, c := range d.Changes {
		fmt.Fprintf(&b, "  %+v\n", c)
	}
	return b.String()
}

// TestComparePruning checks that the walk lists only trees on the path to
// a change: a/b/c.txt changes, while x and a/y do not.
func TestComparePruning(t *testing.T) {
	ctx := context.Background()
	for _, e := range newCmpEnvs(t) {
		t.Run(string(e.kind), func(t *testing.T) {
			base, head := e.id("base"), e.id("nested")
			skipped := map[string]bool{}
			for _, p := range []string{"x", "a/y"} {
				root, _ := e.r.Ref("refs/heads/base")
				ent, err := e.r.Lookup(root, p)
				if err != nil {
					t.Fatal(err)
				}
				skipped[ent.ID.String()] = true
			}
			e.s.ResetCalls()
			d, err := e.client(t, Limits{}).Compare(ctx, e.ref, string(base), string(head), CompareOptions{Mode: Direct})
			if err != nil || len(d.Changes) != 1 || d.Truncated {
				t.Fatalf("Compare = %+v, %v", d, err)
			}
			listings := 0
			for _, req := range e.s.Requests() {
				switch e.kind {
				case forgeauth.GitLab:
					if strings.Contains(req, "/repository/tree?") {
						listings++
						if strings.Contains(req, "path=x&") || strings.Contains(req, "path=a%2Fy&") {
							t.Errorf("listed an unchanged tree: %s", req)
						}
					}
				default:
					if _, rest, ok := strings.Cut(req, "/git/trees/"); ok {
						listings++
						if skipped[rest[:40]] {
							t.Errorf("listed an unchanged tree: %s", req)
						}
					}
				}
			}
			// Two commit reads, then the root, a and a/b on both sides.
			if listings != 6 {
				t.Errorf("%d tree listings, want 6: %q", listings, e.s.Requests())
			}
			if total := len(e.s.Requests()); total != listings+2 {
				t.Errorf("%d calls, want %d: %q", total, listings+2, e.s.Requests())
			}
		})
	}
}

// TestCompareCallLimit checks how running out of calls shows in each
// phase of Compare.
func TestCompareCallLimit(t *testing.T) {
	ctx := context.Background()
	for _, e := range newCmpEnvs(t) {
		t.Run(string(e.kind), func(t *testing.T) {
			base, head := string(e.id("base")), string(e.id("nested"))
			direct := CompareOptions{Mode: Direct}

			// Resolving needs one call per revision.
			_, err := e.client(t, Limits{MaxCalls: 1}).Compare(ctx, e.ref, base, head, direct)
			if confErr(err) != "too-large" {
				t.Errorf("resolve: %v, want ErrTooLarge", err)
			}

			// The walk stops: no changes, Truncated.
			d, err := e.client(t, Limits{MaxCalls: 3}).Compare(ctx, e.ref, base, head, direct)
			if err != nil || !d.Truncated || len(d.Changes) != 0 {
				t.Errorf("walk: %s, %v", cmpFormat(d), err)
			}

			// Count the calls of the whole walk, then leave room for one
			// blob read only: the patch is omitted.
			e.s.ResetCalls()
			if _, err := e.client(t, Limits{}).Compare(ctx, e.ref, base, head, direct); err != nil {
				t.Fatal(err)
			}
			walk := len(e.s.Requests())
			d, err = e.client(t, Limits{MaxCalls: walk + 1}).Compare(ctx, e.ref, base, head, CompareOptions{Mode: Direct, Patches: true})
			want := cmpMod("a/b/c.txt", ModeFile, "c1\n", ModeFile, "c2\n")
			want.PatchOmitted = true
			if err != nil || d.Truncated || len(d.Changes) != 1 || d.Changes[0] != want {
				t.Errorf("patch: %s, %v", cmpFormat(d), err)
			}

			// With room for both blobs the patch is made.
			d, err = e.client(t, Limits{MaxCalls: walk + 2}).Compare(ctx, e.ref, base, head, CompareOptions{Mode: Direct, Patches: true})
			want = withPatch(cmpMod("a/b/c.txt", ModeFile, "c1\n", ModeFile, "c2\n"), "@@ -1 +1 @@\n-c1\n+c2\n", 1, 1)
			if err != nil || len(d.Changes) != 1 || d.Changes[0] != want {
				t.Errorf("patch: %s, %v", cmpFormat(d), err)
			}
		})
	}
}

// TestCompareRawCap checks that the walk stops early once enough changes
// are found to exceed MaxChanges.
func TestCompareRawCap(t *testing.T) {
	ctx := context.Background()
	e := newCmpEnvs(t)[0]
	d, err := e.client(t, Limits{}).Compare(ctx, e.ref, "base", "multi", CompareOptions{MaxChanges: 1})
	if err != nil || !d.Truncated || len(d.Changes) != 1 {
		t.Fatalf("Compare = %s, %v", cmpFormat(d), err)
	}
}

func TestPairRenames(t *testing.T) {
	a, b := cmpBlob("a\n"), cmpBlob("b\n")
	raw := []Change{
		{Status: Deleted, OldPath: "x/one", OldMode: ModeFile, OldID: a},
		{Status: Deleted, OldPath: "y/two", OldMode: ModeFile, OldID: a},
		{Status: Added, Path: "z/two", Mode: ModeExec, ID: a},
		{Status: Added, Path: "w/other", Mode: ModeFile, ID: a},
		{Status: Added, Path: "extra", Mode: ModeFile, ID: a},
		// Different kinds and submodules never pair.
		{Status: Deleted, OldPath: "l", OldMode: ModeSymlink, OldID: b},
		{Status: Added, Path: "f", Mode: ModeFile, ID: b},
		{Status: Deleted, OldPath: "s1", OldMode: ModeSubmodule, OldID: b},
		{Status: Added, Path: "s2", Mode: ModeSubmodule, ID: b},
	}
	got := map[string]string{}
	statuses := map[ChangeStatus]int{}
	for _, c := range pairRenames(raw) {
		statuses[c.Status]++
		if c.Status == Renamed {
			got[c.OldPath] = c.Path
		}
	}
	want := map[string]string{"y/two": "z/two", "x/one": "extra"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("renames = %v, want %v", got, want)
	}
	if statuses[Added] != 3 || statuses[Deleted] != 2 || statuses[Renamed] != 2 {
		t.Errorf("statuses = %v", statuses)
	}
}

// TestCompareSameTree checks that commits with equal root trees are
// compared without listing them where tree IDs are known.
func TestCompareSameTree(t *testing.T) {
	ctx := context.Background()
	for _, e := range newCmpEnvs(t) {
		e.s.ResetCalls()
		d, err := e.client(t, Limits{}).Compare(ctx, e.ref, string(e.id("base")), string(e.id("empty")), CompareOptions{Mode: Direct})
		if err != nil || len(d.Changes) != 0 || d.Truncated {
			t.Fatalf("%s: Compare = %s, %v", e.kind, cmpFormat(d), err)
		}
		want := 2 // the two commits
		if e.kind == forgeauth.GitLab {
			want = 4 // and both root listings
		}
		if got := len(e.s.Requests()); got != want {
			t.Errorf("%s: %d calls, want %d: %q", e.kind, got, want, e.s.Requests())
		}
	}
}

// blobStub is a backend whose blobs are size zero bytes each, of unknown
// size if unsized. Only readBlob works.
type blobStub struct {
	backend
	size    int
	unsized bool
	reads   int
}

func (s *blobStub) readBlob(o *op, id OID) (*Reader, error) {
	s.reads++
	size := int64(s.size)
	if s.unsized {
		size = -1
	}
	data := make([]byte, s.size)
	return &Reader{Blob: Blob{ID: id, Size: size}, ReadCloser: io.NopCloser(bytes.NewReader(data))}, nil
}

func TestComparePatchBudget(t *testing.T) {
	opts, err := CompareOptions{Patches: true, MaxPatchFiles: 1 << 20, MaxPatchBytes: 1 << 30}.withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	// Request limits are lowered to the caps.
	if opts.MaxPatchFiles != maxPatchFiles || opts.MaxPatchBytes != maxPatchBytes {
		t.Fatalf("limits = %d files, %d bytes", opts.MaxPatchFiles, opts.MaxPatchBytes)
	}
	// Each change reads two 4 MiB blobs; the read budget stops patches
	// after 64 MiB.
	stub := &blobStub{size: 4 << 20}
	td := &treeDiff{b: stub, opts: opts}
	changes := make([]Change, 20)
	for i := range changes {
		changes[i] = Change{Status: Modified, Path: fmt.Sprint(i), OldPath: fmt.Sprint(i),
			OldMode: ModeFile, Mode: ModeFile, OldID: id(1000 + i), ID: id(2000 + i)}
	}
	if err := td.patches(changes, 100<<20); err != nil {
		t.Fatal(err)
	}
	read := patchInputBudget / (2 * stub.size)
	for i, c := range changes {
		if c.Binary != (i < read) || c.PatchOmitted != (i >= read) {
			t.Errorf("change %d: binary %v, omitted %v", i, c.Binary, c.PatchOmitted)
		}
	}
	if stub.reads != 2*read {
		t.Errorf("%d blob reads, want %d", stub.reads, 2*read)
	}
}

func TestCompareSniffCopy(t *testing.T) {
	// A blob of unknown size over the cap keeps only its start, not the
	// whole read buffer.
	td := &treeDiff{b: &blobStub{size: 1 << 20, unsized: true}}
	s, err := td.readBlob(id(1), 1<<19)
	if err != nil {
		t.Fatal(err)
	}
	if s.full || len(s.data) != binarySniff || cap(s.data) > 2*binarySniff {
		t.Errorf("side: full %v, len %d, cap %d", s.full, len(s.data), cap(s.data))
	}
}
