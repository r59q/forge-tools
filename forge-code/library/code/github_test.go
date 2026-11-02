package code

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-code/library/internal/fakeforge"
	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// githubTest is a GitHub front with the seeded fixture at octo/fixture.
type githubTest struct {
	s    *fakeforge.Server
	repo *fakeforge.Repo
	fx   fakeforge.Fixture
	c    *Client
	ref  RepositoryRef
}

func newGitHubTest(t *testing.T, opts fakeforge.GitHubOptions, l Limits) *githubTest {
	t.Helper()
	s := fakeforge.NewServer(t, forgeauth.GitHub, fakeforge.GitHubFront(opts))
	r := s.NewRepo("octo/fixture")
	fx := fakeforge.Seed(r)
	c, err := NewWithLimits(l, Source{ID: "gh", Session: s.Session})
	if err != nil {
		t.Fatal(err)
	}
	return &githubTest{s: s, repo: r, fx: fx, c: c, ref: RepositoryRef{Source: "gh", Repository: "octo/fixture"}}
}

// oid converts a fakeforge ID.
func githubOID(id gitobj.ID) OID { return OID(id.String()) }

// calls counts API requests matching a pattern since the last reset.
func (g *githubTest) calls(pattern string) int { return g.s.Calls("", pattern) }

func TestGitHubResolve(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	fx := g.fx
	tests := []struct {
		rev   string
		want  gitobj.ID
		ref   string
		calls int
	}{
		{fx.Head.String(), fx.Head, "", 1},
		{strings.ToUpper(fx.Head.String()), fx.Head, "", 1},
		{"", fx.Head, "refs/heads/main", 2},
		{"HEAD", fx.Head, "refs/heads/main", 2},
		{"refs/heads/feature", fx.Feature[2], "refs/heads/feature", 1},
		{"refs/heads/clash", fx.ClashBranch, "refs/heads/clash", 1},
		{"refs/tags/clash", fx.ClashTag, "refs/tags/clash", 1},
		{"refs/tags/v1.0", fx.Main[3], "refs/tags/v1.0", 2},
		{"refs/tags/chain", fx.Main[1], "refs/tags/chain", 3},
		{"v1.0", fx.Main[3], "refs/tags/v1.0", 3},
		{"light", fx.Light, "refs/tags/light", 2},
		{"feature", fx.Feature[2], "refs/heads/feature", 2},
		{"HEAD^", fx.Merge, "refs/heads/main", 3},
		{"main~2", fx.Main[4], "refs/heads/main", 4},
		{"main~1^2", fx.Feature[1], "refs/heads/main", 4},
		{"v1.0~1", fx.Main[2], "refs/tags/v1.0", 4},
		{fx.Head.String() + "^0", fx.Head, "", 1},
	}
	for _, tt := range tests {
		g.s.ResetCalls()
		got, err := g.c.Resolve(context.Background(), g.ref, tt.rev)
		if err != nil {
			t.Errorf("Resolve(%q): %v", tt.rev, err)
			continue
		}
		if want := (Resolved{Commit: githubOID(tt.want), Ref: tt.ref}); got != want {
			t.Errorf("Resolve(%q) = %+v, want %+v", tt.rev, got, want)
		}
		if n := g.calls("*"); n != tt.calls {
			t.Errorf("Resolve(%q) made %d calls, want %d: %v", tt.rev, n, tt.calls, g.s.Requests())
		}
	}
}

func TestGitHubResolveErrors(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	ctx := context.Background()
	for _, rev := range []string{
		"nope", "refs/heads/nope", "refs/tags/nope", "refs/heads/feat",
		strings.Repeat("ab", 20), g.fx.Blobs[fakeforge.PathReadme].String(),
		"HEAD~100", "HEAD^3",
	} {
		_, err := g.c.Resolve(ctx, g.ref, rev)
		if !errors.Is(err, ErrRevisionNotFound) || !errors.Is(err, forgehttp.ErrNotFound) {
			t.Errorf("Resolve(%q) = %v, want ErrRevisionNotFound and forgehttp.ErrNotFound", rev, err)
		}
	}
	if _, err := g.c.Resolve(ctx, g.ref, "clash"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("Resolve(clash) = %v, want ErrAmbiguous", err)
	}
	for _, rev := range []string{g.fx.Head.String()[:7], "main..feature", "main@{1}", "refs/notes/x"} {
		if _, err := g.c.Resolve(ctx, g.ref, rev); !errors.Is(err, ErrInvalid) {
			t.Errorf("Resolve(%q) = %v, want ErrInvalid", rev, err)
		}
	}
	nested := RepositoryRef{Source: "gh", Repository: "group/sub/proj"}
	if _, err := g.c.Resolve(ctx, nested, "HEAD"); !errors.Is(err, ErrInvalid) {
		t.Errorf("nested repository: %v, want ErrInvalid", err)
	}
}

func TestGitHubListRefs(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	fx := g.fx
	ctx := context.Background()
	got, err := g.c.ListRefs(ctx, g.ref, RefsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Ref{
		{"refs/heads/clash", githubOID(fx.ClashBranch), githubOID(fx.ClashBranch)},
		{"refs/heads/feature", githubOID(fx.Feature[2]), githubOID(fx.Feature[2])},
		{"refs/heads/main", githubOID(fx.Head), githubOID(fx.Head)},
		{"refs/tags/chain", githubOID(fx.TagOuter), githubOID(fx.Main[1])},
		{"refs/tags/clash", githubOID(fx.ClashTag), githubOID(fx.ClashTag)},
		{"refs/tags/light", githubOID(fx.Light), githubOID(fx.Light)},
		{"refs/tags/v1.0", githubOID(fx.Tag), githubOID(fx.Main[3])},
	}
	if got.Default != "refs/heads/main" || got.Truncated || !slices.Equal(got.Refs, want) {
		t.Fatalf("ListRefs = %+v\nwant %+v", got, want)
	}
	if n := g.calls("*/git/tags/*"); n != 0 {
		t.Errorf("peeled with %d git/tags calls, want 0 (join on /tags)", n)
	}

	g.s.ResetCalls()
	got, err = g.c.ListRefs(ctx, g.ref, RefsOptions{Limit: 2})
	if err != nil || !got.Truncated || len(got.Refs) != 2 || got.Refs[1].Name != "refs/heads/feature" {
		t.Errorf("Limit 2 = %+v, %v", got, err)
	}
	if n := g.calls("*/tags"); n != 0 {
		t.Errorf("Limit 2 listed tags with %d calls", n)
	}

	got, err = g.c.ListRefs(ctx, g.ref, RefsOptions{Prefix: "refs/tags/"})
	if err != nil || got.Truncated || !slices.Equal(got.Refs, want[3:]) {
		t.Errorf("tags = %+v, %v", got, err)
	}
}

func TestGitHubListRefsPaging(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	ctx := context.Background()
	for i := range 150 {
		g.repo.SetRef(fmt.Sprintf("refs/heads/b/%03d", i), g.fx.Main[0])
	}
	// A tag on a tree has no commit; a dangling one cannot be peeled.
	tree, _ := g.repo.Lookup(g.fx.Head, "")
	g.repo.SetRef("refs/tags/tree", tree.ID)
	tagged := g.repo.WriteTag(gitobj.Tag{Object: tree.ID, Name: "treetag", Message: "x\n"})
	g.repo.SetRef("refs/tags/treetag", tagged)

	got, err := g.c.ListRefs(ctx, g.ref, RefsOptions{Prefix: "refs/heads/"})
	if err != nil || got.Truncated || len(got.Refs) != 153 {
		t.Fatalf("heads = %d refs, truncated %v, %v", len(got.Refs), got.Truncated, err)
	}
	got, err = g.c.ListRefs(ctx, g.ref, RefsOptions{Prefix: "refs/tags/"})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Ref{}
	for _, r := range got.Refs {
		byName[r.Name] = r
	}
	if r := byName["refs/tags/tree"]; r.Commit != "" || r.Target != githubOID(tree.ID) {
		t.Errorf("tree tag = %+v", r)
	}
	if r := byName["refs/tags/treetag"]; r.Commit != "" || r.Target != githubOID(tagged) {
		t.Errorf("annotated tree tag = %+v", r)
	}

	// matching-refs is not paged: one call lists every head.
	g.s.ResetCalls()
	got, err = g.c.ListRefs(ctx, g.ref, RefsOptions{Prefix: "refs/heads/", Limit: 1})
	if err != nil || !got.Truncated || len(got.Refs) != 1 {
		t.Errorf("Limit 1 = %d refs, truncated %v, %v", len(got.Refs), got.Truncated, err)
	}
	if n := g.calls("*/git/matching-refs/*"); n != 1 {
		t.Errorf("listed heads with %d calls, want 1: %v", n, g.s.Requests())
	}
	for _, r := range g.s.Requests() {
		if strings.Contains(r, "matching-refs") && strings.Contains(r, "per_page") {
			t.Errorf("matching-refs sent per_page: %s", r)
		}
	}

	// Out of calls before listing: nothing, Truncated.
	c1, err := NewWithLimits(Limits{MaxCalls: 1}, Source{ID: "gh", Session: g.s.Session})
	if err != nil {
		t.Fatal(err)
	}
	got, err = c1.ListRefs(ctx, g.ref, RefsOptions{Prefix: "refs/heads/"})
	if err != nil || !got.Truncated || len(got.Refs) != 0 {
		t.Errorf("budget = %d refs, truncated %v, %v", len(got.Refs), got.Truncated, err)
	}
	c, err := NewWithLimits(Limits{MaxCalls: 2}, Source{ID: "gh", Session: g.s.Session})
	if err != nil {
		t.Fatal(err)
	}
	// Out of calls before peeling: no incomplete tag is returned.
	got, err = c.ListRefs(ctx, g.ref, RefsOptions{Prefix: "refs/tags/"})
	if err != nil || !got.Truncated {
		t.Fatalf("tags on budget = %+v, %v", got, err)
	}
	for _, r := range got.Refs {
		if r.Target != r.Commit {
			t.Errorf("incomplete tag %+v", r)
		}
	}
}

func TestGitHubListRefsTooLarge(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	// GitHub sends every match at once; a body over the cap is ErrTooLarge,
	// not a decode error.
	g.s.Inject(fakeforge.Fault{Path: "*/git/matching-refs/*", Status: http.StatusOK,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   "[" + strings.Repeat(" ", githubMaxRefsBody) + "]"})
	_, err := g.c.ListRefs(context.Background(), g.ref, RefsOptions{Limit: 1})
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized listing = %v, want ErrTooLarge", err)
	}
}

func TestGitHubListRefsTagMoved(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	fx := g.fx
	// v1.0 moves to a new annotated tag while /tags is read.
	moved := g.repo.WriteTag(gitobj.Tag{Object: fx.Head, Name: "v1.0", Message: "moved\n"})
	var once sync.Once
	g.s.SetHook(func(r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tags") {
			once.Do(func() { g.repo.SetRef("refs/tags/v1.0", moved) })
		}
	})
	defer g.s.SetHook(nil)
	got, err := g.c.ListRefs(context.Background(), g.ref, RefsOptions{Prefix: "refs/tags/"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got.Refs {
		if r.Name != "refs/tags/v1.0" {
			continue
		}
		if want := (Ref{Name: r.Name, Target: githubOID(moved), Commit: githubOID(fx.Head)}); r != want {
			t.Errorf("moved tag = %+v, want %+v", r, want)
		}
		return
	}
	t.Errorf("v1.0 missing from %+v", got.Refs)
}

func TestGitHubCommit(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	got, err := g.c.Commit(context.Background(), g.ref, "main^")
	if err != nil {
		t.Fatal(err)
	}
	src, _ := g.repo.ReadCommit(g.fx.Merge)
	want := Commit{
		ID: githubOID(g.fx.Merge), Tree: githubOID(src.Tree),
		Parents: []OID{githubOID(g.fx.Main[4]), githubOID(g.fx.Feature[1])},
		Author:  Signature{Name: src.Author.Name, Email: src.Author.Email, When: src.Author.When},
		Message: "Merge branch 'feature'",
	}
	want.Committer = want.Author
	if got.ID != want.ID || got.Tree != want.Tree || !slices.Equal(got.Parents, want.Parents) ||
		got.Message != want.Message || got.Author.Name != want.Author.Name ||
		got.Author.Email != want.Author.Email || !got.Author.When.Equal(want.Author.When) ||
		!got.Committer.When.Equal(want.Committer.When) {
		t.Errorf("Commit = %+v\nwant %+v", got, want)
	}
	root, err := g.c.Commit(context.Background(), g.ref, g.fx.Main[0].String())
	if err != nil || root.Parents == nil || len(root.Parents) != 0 {
		t.Errorf("root commit = %+v, %v", root, err)
	}
}

// githubAll pages through History and returns the commit IDs.
func githubAll(t *testing.T, g *githubTest, opts HistoryOptions, between func()) ([]OID, OID) {
	t.Helper()
	var ids []OID
	var tip OID
	for pages := 0; ; pages++ {
		if pages > 50 {
			t.Fatal("history does not end")
		}
		p, err := g.c.History(context.Background(), g.ref, opts)
		if err != nil {
			t.Fatal(err)
		}
		if tip == "" {
			tip = p.Tip
		} else if p.Tip != tip {
			t.Fatalf("tip moved from %s to %s", tip, p.Tip)
		}
		for _, c := range p.Commits {
			ids = append(ids, c.ID)
		}
		if p.Next == "" {
			return ids, tip
		}
		opts = HistoryOptions{Cursor: p.Next}
		if between != nil {
			between()
		}
	}
}

func githubLog(t *testing.T, r *fakeforge.Repo, tip gitobj.ID, path string) []OID {
	t.Helper()
	ids, err := r.Log(tip, fakeforge.LogOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	out := []OID{}
	for _, id := range ids {
		out = append(out, githubOID(id))
	}
	return out
}

func TestGitHubHistory(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	fx := g.fx

	// The hook moves main after the first page; paging stays on the tip.
	moved := false
	ids, tip := githubAll(t, g, HistoryOptions{Revision: "main", PageSize: 3}, func() {
		if !moved {
			g.repo.SetRef("refs/heads/main", fx.Feature[2])
			moved = true
		}
	})
	if tip != githubOID(fx.Head) || !slices.Equal(ids, githubLog(t, g.repo, fx.Head, "")) {
		t.Errorf("history = %v (tip %s)\nwant %v", ids, tip, githubLog(t, g.repo, fx.Head, ""))
	}
	g.repo.SetRef("refs/heads/main", fx.Head)

	for _, path := range []string{fakeforge.PathFeature, "docs", fakeforge.PathReadme, "no/such/path"} {
		ids, _ := githubAll(t, g, HistoryOptions{Revision: "HEAD", Path: path, PageSize: 2}, nil)
		if want := githubLog(t, g.repo, fx.Head, path); !slices.Equal(ids, want) {
			t.Errorf("history of %q = %v, want %v", path, ids, want)
		}
	}

	// One page that fits exactly has no Next.
	n := len(githubLog(t, g.repo, fx.Head, ""))
	p, err := g.c.History(context.Background(), g.ref, HistoryOptions{PageSize: n})
	if err != nil || p.Next != "" || len(p.Commits) != n {
		t.Errorf("full page: %d commits, next %q, %v", len(p.Commits), p.Next, err)
	}
	if p.Commits[0].Tree == "" || p.Commits[0].Author.When.IsZero() {
		t.Errorf("history commit lacks fields: %+v", p.Commits[0])
	}
}

func TestGitHubTree(t *testing.T) {
	for _, limit := range []int{0, 5} {
		t.Run(fmt.Sprintf("TreeLimit=%d", limit), func(t *testing.T) {
			g := newGitHubTest(t, fakeforge.GitHubOptions{TreeLimit: limit}, Limits{})
			githubCheckTrees(t, g)
		})
	}
}

// githubEntries lists root in the store as Entries under dir, sorted.
func githubEntries(t *testing.T, r *fakeforge.Repo, root gitobj.ID, dir string, recursive bool) []Entry {
	t.Helper()
	list, err := r.ListTree(root, recursive)
	if err != nil {
		t.Fatal(err)
	}
	out := []Entry{}
	for _, e := range list {
		mode := FileMode(e.Mode)
		path := e.Path
		if dir != "" {
			path = dir + "/" + path
		}
		size := int64(-1)
		if typeOf(mode) == ObjectBlob {
			size = e.Size
		}
		out = append(out, Entry{Name: e.Name, Path: path, Mode: mode, Type: typeOf(mode), ID: githubOID(e.ID), Size: size})
	}
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	return out
}

func githubCheckTrees(t *testing.T, g *githubTest) {
	ctx := context.Background()
	fx := g.fx
	root, _ := g.repo.Lookup(fx.Head, "")
	docs, _ := g.repo.Lookup(fx.Head, "docs")

	g.s.ResetCalls()
	tr, err := g.c.Tree(ctx, g.ref, "refs/heads/main", TreeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := githubEntries(t, g.repo, root.ID, "", false)
	if tr.Commit != githubOID(fx.Head) || tr.ID != githubOID(root.ID) || tr.Path != "" || tr.Truncated ||
		!slices.Equal(tr.Entries, want) {
		t.Errorf("flat root = %+v\nwant %+v", tr, want)
	}
	if n := g.calls("*"); n != 3 { // ref, commit, tree
		t.Errorf("flat root made %d calls: %v", n, g.s.Requests())
	}

	tr, err = g.c.Tree(ctx, g.ref, "main", TreeOptions{Recursive: true})
	want = githubEntries(t, g.repo, root.ID, "", true)
	if err != nil || tr.Truncated || !slices.Equal(tr.Entries, want) {
		t.Errorf("recursive root = %+v, %v\nwant %+v", tr, err, want)
	}

	tr, err = g.c.Tree(ctx, g.ref, "main", TreeOptions{Path: "docs", Recursive: true})
	want = githubEntries(t, g.repo, docs.ID, "docs", true)
	if err != nil || tr.ID != githubOID(docs.ID) || tr.Path != "docs" || !slices.Equal(tr.Entries, want) {
		t.Errorf("recursive docs = %+v, %v\nwant %+v", tr, err, want)
	}

	tr, err = g.c.Tree(ctx, g.ref, "main", TreeOptions{Recursive: true, MaxEntries: 3})
	if err != nil || !tr.Truncated || len(tr.Entries) != 3 {
		t.Errorf("MaxEntries 3 = %+v, %v", tr, err)
	}

	for _, p := range []string{"nope", "docs/nope", fakeforge.PathReadme, fakeforge.PathSubmodule} {
		_, err := g.c.Tree(ctx, g.ref, "main", TreeOptions{Path: p})
		if !errors.Is(err, ErrPathNotFound) || !errors.Is(err, forgehttp.ErrNotFound) {
			t.Errorf("Tree(%q) = %v, want ErrPathNotFound", p, err)
		}
	}
}

func TestGitHubTreeTruncatedWalk(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{TreeLimit: 5}, Limits{})
	ctx := context.Background()
	tr, err := g.c.Tree(ctx, g.ref, g.fx.Head.String(), TreeOptions{Recursive: true})
	if err != nil || tr.Truncated {
		t.Fatalf("walk = %+v, %v", tr, err)
	}
	// Out of calls mid-walk: Truncated, not an error.
	c, err := NewWithLimits(Limits{MaxCalls: 4}, Source{ID: "gh", Session: g.s.Session})
	if err != nil {
		t.Fatal(err)
	}
	tr, err = c.Tree(ctx, g.ref, g.fx.Head.String(), TreeOptions{Recursive: true})
	if err != nil || !tr.Truncated || len(tr.Entries) == 0 {
		t.Errorf("budget walk = %d entries, truncated %v, %v", len(tr.Entries), tr.Truncated, err)
	}
}

func TestGitHubStat(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	ctx := context.Background()
	b := g.fx.Blobs
	tests := []struct {
		path string
		mode FileMode
		typ  ObjectType
		id   OID
		size int64
	}{
		{fakeforge.PathSymlink, ModeSymlink, ObjectBlob, githubOID(b[fakeforge.PathSymlink]), int64(len(fakeforge.PathReadme))},
		{fakeforge.PathExec, ModeExec, ObjectBlob, githubOID(b[fakeforge.PathExec]), int64(len("#!/bin/sh\necho run\n"))},
		{fakeforge.PathSubmodule, ModeSubmodule, ObjectCommit, githubOID(fakeforge.SubmoduleCommit), -1},
		{fakeforge.PathUnicode, ModeFile, ObjectBlob, githubOID(b[fakeforge.PathUnicode]), int64(len("Grüße, 世界\n"))},
		{fakeforge.PathLarge, ModeFile, ObjectBlob, githubOID(b[fakeforge.PathLarge]), fakeforge.LargeSize},
	}
	for _, tt := range tests {
		got, err := g.c.Stat(ctx, g.ref, "HEAD", tt.path)
		if err != nil {
			t.Errorf("Stat(%q): %v", tt.path, err)
			continue
		}
		name := tt.path[strings.LastIndexByte(tt.path, '/')+1:]
		want := PathInfo{Commit: githubOID(g.fx.Head), Entry: Entry{Name: name, Path: tt.path, Mode: tt.mode, Type: tt.typ, ID: tt.id, Size: tt.size}}
		if got != want {
			t.Errorf("Stat(%q) = %+v\nwant %+v", tt.path, got, want)
		}
	}
	docs, err := g.c.Stat(ctx, g.ref, "HEAD", "docs/guide")
	if err != nil || docs.Mode != ModeTree || docs.Type != ObjectTree || docs.Size != -1 {
		t.Errorf("Stat(docs/guide) = %+v, %v", docs, err)
	}
	for _, p := range []string{"nope", "docs/nope", "README.md/x", "nope/x", fakeforge.PathSubmodule + "/x"} {
		_, err := g.c.Stat(ctx, g.ref, "HEAD", p)
		if !errors.Is(err, ErrPathNotFound) || !errors.Is(err, forgehttp.ErrNotFound) {
			t.Errorf("Stat(%q) = %v, want ErrPathNotFound", p, err)
		}
	}
	// The old name exists before the rename only.
	if _, err := g.c.Stat(ctx, g.ref, g.fx.Main[2].String(), fakeforge.PathOldName); err != nil {
		t.Errorf("Stat old name: %v", err)
	}
}

// githubRead reads and closes r.
func githubRead(t *testing.T, r *Reader, err error) []byte {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGitHubReadFile(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	ctx := context.Background()
	for _, path := range []string{
		fakeforge.PathReadme, fakeforge.PathBinary, fakeforge.PathLarge, fakeforge.PathUnicode,
		fakeforge.PathSymlink, fakeforge.PathNested, fakeforge.PathLFS,
	} {
		want, err := g.repo.ReadBlob(g.fx.Blobs[path])
		if err != nil {
			t.Fatal(err)
		}
		g.s.ResetCalls()
		r, err := g.c.ReadFile(ctx, g.ref, g.fx.Head.String(), path)
		blob := r
		got := githubRead(t, r, err)
		if !bytes.Equal(got, want) {
			t.Errorf("ReadFile(%q) = %d bytes, want %d", path, len(got), len(want))
		}
		wantBlob := Blob{ID: githubOID(g.fx.Blobs[path]), Commit: githubOID(g.fx.Head), Path: path,
			Size: int64(len(want)), LFSPointer: path == fakeforge.PathLFS}
		if blob.Blob != wantBlob {
			t.Errorf("ReadFile(%q) blob = %+v, want %+v", path, blob.Blob, wantBlob)
		}
		// commit, one tree per directory level, blob
		if n, want := g.calls("*"), 3+strings.Count(path, "/"); n != want {
			t.Errorf("ReadFile(%q) made %d calls, want %d: %v", path, n, want, g.s.Requests())
		}
	}
	for _, p := range []string{"docs", fakeforge.PathSubmodule} {
		if _, err := g.c.ReadFile(ctx, g.ref, "HEAD", p); !errors.Is(err, ErrNotFile) {
			t.Errorf("ReadFile(%q) = %v, want ErrNotFile", p, err)
		}
	}
	if _, err := g.c.ReadFile(ctx, g.ref, "HEAD", "nope.txt"); !errors.Is(err, ErrPathNotFound) {
		t.Errorf("ReadFile(nope.txt) = %v, want ErrPathNotFound", err)
	}

	// The blob is pinned: moving main after resolving changes nothing.
	g.s.SetHook(func(r *http.Request) {
		if strings.Contains(r.URL.Path, "/git/blobs/") {
			g.repo.SetRef("refs/heads/main", g.fx.Main[0])
		}
	})
	r, err := g.c.ReadFile(ctx, g.ref, "main", fakeforge.PathReadme)
	if got := githubRead(t, r, err); r.Commit != githubOID(g.fx.Head) ||
		!bytes.Contains(got, []byte("with history")) {
		t.Errorf("pinned read = %q at %s", got, r.Commit)
	}
}

func TestGitHubReadBlob(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	ctx := context.Background()
	id := g.fx.Blobs[fakeforge.PathBinary]
	r, err := g.c.ReadBlob(ctx, g.ref, githubOID(id))
	got := githubRead(t, r, err)
	if !bytes.Equal(got, fakeforge.Binary) || r.ID != githubOID(id) || r.Size != int64(len(got)) || r.Commit != "" {
		t.Errorf("ReadBlob = %q, %+v", got, r.Blob)
	}
	r, err = g.c.ReadBlob(ctx, g.ref, githubOID(g.fx.Blobs[fakeforge.PathLarge]))
	if got := githubRead(t, r, err); len(got) != fakeforge.LargeSize {
		t.Errorf("large blob = %d bytes", len(got))
	}
	r, err = g.c.ReadBlob(ctx, g.ref, githubOID(g.fx.Blobs[fakeforge.PathLFS]))
	if githubRead(t, r, err); !r.LFSPointer {
		t.Error("LFS pointer not flagged")
	}
	for _, missing := range []OID{OID(strings.Repeat("ab", 20)), githubOID(g.fx.Head)} {
		_, err := g.c.ReadBlob(ctx, g.ref, missing)
		if !errors.Is(err, ErrRevisionNotFound) || !errors.Is(err, forgehttp.ErrNotFound) {
			t.Errorf("ReadBlob(%s) = %v, want ErrRevisionNotFound", missing, err)
		}
	}
}

func TestGitHubBlobLimits(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{MaxBlobBytes: 1000}, Limits{MaxBlobBytes: 100})
	ctx := context.Background()
	// The tree entry's size is over the limit: no blob call.
	_, err := g.c.ReadFile(ctx, g.ref, "HEAD", fakeforge.PathLarge)
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("ReadFile(large) = %v, want ErrTooLarge", err)
	}
	if n := g.calls("*/git/blobs/*"); n != 0 {
		t.Errorf("fetched the blob %d times", n)
	}
	// ReadBlob learns the size from the response.
	_, err = g.c.ReadBlob(ctx, g.ref, githubOID(g.fx.Blobs[fakeforge.PathReadme]))
	if err != nil {
		t.Errorf("ReadBlob(small) = %v", err)
	}
	r, err := g.c.ReadBlob(ctx, g.ref, githubOID(g.fx.Blobs[fakeforge.PathMain]))
	if err == nil {
		_, err = io.ReadAll(r)
		r.Close()
	}
	if err != nil {
		t.Errorf("ReadBlob(main.go) = %v", err)
	}

	// GitHub refuses blobs over its own limit with a 403.
	c, err := NewWithLimits(Limits{}, Source{ID: "gh", Session: g.s.Session})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ReadBlob(ctx, g.ref, githubOID(g.fx.Blobs[fakeforge.PathLarge]))
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("ReadBlob over GitHub's limit = %v, want ErrTooLarge", err)
	}
}

func TestGitHubMergeBase(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	ctx := context.Background()
	fx := g.fx
	tests := []struct {
		a, b string
		want gitobj.ID
	}{
		{"main", "feature", fx.MergeBase},
		{"feature", "main", fx.MergeBase},
		{fx.Main[1].String(), "HEAD", fx.Main[1]},
		{"HEAD", fx.Main[1].String(), fx.Main[1]},
		{"v1.0", "refs/heads/feature", fx.Main[2]},
	}
	for _, tt := range tests {
		got, err := g.c.MergeBase(ctx, g.ref, tt.a, tt.b)
		if err != nil || got != githubOID(tt.want) {
			t.Errorf("MergeBase(%s, %s) = %s, %v, want %s", tt.a, tt.b, got, err, tt.want)
		}
	}
	orphan := g.repo.CommitTree(g.repo.BuildTree(map[string]fakeforge.File{"x": {Data: []byte("x")}}), fakeforge.Epoch, "Orphan\n")
	g.repo.SetRef("refs/heads/orphan", orphan)
	if _, err := g.c.MergeBase(ctx, g.ref, "main", "orphan"); !errors.Is(err, ErrInvalid) {
		t.Errorf("MergeBase(unrelated) = %v, want ErrInvalid", err)
	}
	if _, err := g.c.MergeBase(ctx, g.ref, "main", "nope"); !errors.Is(err, ErrRevisionNotFound) {
		t.Errorf("MergeBase(missing) = %v, want ErrRevisionNotFound", err)
	}
	caps, err := g.c.Capabilities("gh")
	if err != nil || caps != (Capabilities{TreeIDs: true, MergeBase: true}) {
		t.Errorf("Capabilities = %+v, %v", caps, err)
	}
}

func TestGitHubAuthErrors(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	ctx := context.Background()

	bad, err := g.s.SessionWithToken("wrong")
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Source{ID: "gh", Session: bad})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve(ctx, g.ref, "HEAD"); !errors.Is(err, forgehttp.ErrUnauthorized) {
		t.Errorf("wrong token = %v, want ErrUnauthorized", err)
	}

	g.repo.SetPrivate(true)
	g.s.SetAccess("octo/fixture", fakeforge.AccessNone)
	for _, rev := range []string{"HEAD", "main", g.fx.Head.String()} {
		_, err := g.c.Resolve(ctx, g.ref, rev)
		if !errors.Is(err, forgehttp.ErrNotFound) || errors.Is(err, forgehttp.ErrUnauthorized) {
			t.Errorf("private Resolve(%q) = %v, want not found", rev, err)
		}
	}
	if _, err := g.c.ListRefs(ctx, g.ref, RefsOptions{}); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("private ListRefs = %v, want not found", err)
	}
	g.s.SetAccess("octo/fixture", fakeforge.AccessRead)

	g.s.Inject(fakeforge.Fault{Path: "*/git/trees/*", Status: http.StatusForbidden, Times: 1})
	if _, err := g.c.Tree(ctx, g.ref, "main", TreeOptions{}); !errors.Is(err, forgehttp.ErrForbidden) {
		t.Errorf("403 = %v, want ErrForbidden", err)
	}
	g.s.Inject(fakeforge.Fault{Path: "*", Status: http.StatusForbidden, Times: 1,
		Header: http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"2000000000"}}})
	if _, err := g.c.Resolve(ctx, g.ref, "main"); !errors.Is(err, forgehttp.ErrRateLimited) {
		t.Errorf("403 rate limit = %v, want ErrRateLimited", err)
	}
	g.s.Inject(fakeforge.Fault{Path: "*", Status: http.StatusTooManyRequests, Times: 1,
		Header: http.Header{"Retry-After": {"30"}}})
	if _, err := g.c.History(ctx, g.ref, HistoryOptions{Revision: g.fx.Head.String()}); !errors.Is(err, forgehttp.ErrRateLimited) {
		t.Errorf("429 = %v, want ErrRateLimited", err)
	}
	missing := RepositoryRef{Source: "gh", Repository: "octo/missing"}
	if _, err := g.c.Resolve(ctx, missing, "HEAD"); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("missing repository = %v, want not found", err)
	}
}

func TestGitHubEmptyRepository(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	g.s.NewRepo("octo/empty")
	ctx := context.Background()
	empty := RepositoryRef{Source: "gh", Repository: "octo/empty"}
	for _, rev := range []string{"HEAD", "main", strings.Repeat("ab", 20)} {
		_, err := g.c.Resolve(ctx, empty, rev)
		if !errors.Is(err, ErrRevisionNotFound) {
			t.Errorf("empty Resolve(%q) = %v, want ErrRevisionNotFound", rev, err)
		}
	}
	list, err := g.c.ListRefs(ctx, empty, RefsOptions{})
	if err != nil || len(list.Refs) != 0 || list.Truncated {
		t.Errorf("empty ListRefs = %+v, %v", list, err)
	}
}

func TestGitHubConcurrent(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := g.c.Tree(ctx, g.ref, "feature", TreeOptions{Recursive: true}); err != nil {
				t.Error(err)
			}
			if _, err := g.c.Resolve(ctx, g.ref, "v1.0"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestGitHubQuirks(t *testing.T) {
	g := newGitHubTest(t, fakeforge.GitHubOptions{}, Limits{})
	ctx := context.Background()

	// A chain of five tags is deeper than the adapter peels.
	id := g.fx.Main[0]
	for i := range 5 {
		id = g.repo.WriteTag(gitobj.Tag{Object: id, Name: fmt.Sprintf("deep%d", i), Message: "x\n"})
	}
	g.repo.SetRef("refs/tags/deep", id)
	if _, err := g.c.Resolve(ctx, g.ref, "refs/tags/deep"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("deep tag = %v, want ErrUnsupported", err)
	}
	// ListRefs peels it through /tags.
	list, err := g.c.ListRefs(ctx, g.ref, RefsOptions{Prefix: "refs/tags/"})
	if err != nil || list.Refs[2].Name != "refs/tags/deep" || list.Refs[2].Commit != githubOID(g.fx.Main[0]) {
		t.Errorf("ListRefs deep = %+v, %v", list, err)
	}

	// A JSON body in place of raw content is refused, never returned.
	g.s.Inject(fakeforge.Fault{Path: "*/git/blobs/*", Status: http.StatusOK, Times: 1,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: `{"content":"eA==","encoding":"base64"}`})
	if r, err := g.c.ReadBlob(ctx, g.ref, githubOID(g.fx.Blobs[fakeforge.PathReadme])); err == nil {
		r.Close()
		t.Error("ReadBlob accepted a JSON body")
	}

	// A cursor for a commit GitHub does not know gets its 404, or 422 on
	// older versions.
	cur := historyCursor{V: cursorVersion, Tip: OID(strings.Repeat("ab", 20)), N: 10}
	_, err = g.c.History(ctx, g.ref, HistoryOptions{Cursor: cur.encode()})
	if !errors.Is(err, ErrRevisionNotFound) || !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("unknown tip = %v, want ErrRevisionNotFound", err)
	}
	g.s.Inject(fakeforge.Fault{Path: "*/commits", Status: http.StatusUnprocessableEntity, Times: 1})
	_, err = g.c.History(ctx, g.ref, HistoryOptions{Cursor: cur.encode()})
	if !errors.Is(err, ErrRevisionNotFound) {
		t.Errorf("unknown tip, 422 = %v, want ErrRevisionNotFound", err)
	}
	// A cursor on a missing repository is not blamed on the tip.
	gone := RepositoryRef{Source: "gh", Repository: "octo/gone"}
	_, err = g.c.History(ctx, gone, HistoryOptions{Cursor: cur.encode()})
	if errors.Is(err, ErrRevisionNotFound) || !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("missing repository = %v, want a plain not found", err)
	}
}
