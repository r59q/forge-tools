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
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/fakeforge"
	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// gitlabRepo is the seeded repository on the GitLab test forge.
var gitlabRepo = RepositoryRef{Source: "gl", Repository: "group/sub/proj"}

// gitlabTest is a GitLab fake forge with a seeded repository and a
// Client for it.
type gitlabTest struct {
	srv  *fakeforge.Server
	repo *fakeforge.Repo
	fx   fakeforge.Fixture
	c    *Client
}

func newGitLabTest(t *testing.T, l Limits) *gitlabTest {
	t.Helper()
	srv := fakeforge.NewServer(t, forgeauth.GitLab, fakeforge.GitLab)
	repo := srv.NewRepo(gitlabRepo.Repository)
	fx := fakeforge.Seed(repo)
	c, err := NewWithLimits(l, Source{ID: gitlabRepo.Source, Session: srv.Session})
	if err != nil {
		t.Fatal(err)
	}
	return &gitlabTest{srv: srv, repo: repo, fx: fx, c: c}
}

// gitlabOID converts a fixture ID.
func gitlabOID(id gitobj.ID) OID { return OID(id.String()) }

// gitlabNotFound checks that err matches kind and forgehttp.ErrNotFound.
func gitlabNotFound(t *testing.T, err, kind error) {
	t.Helper()
	if !errors.Is(err, kind) || !errors.Is(err, forgehttp.ErrNotFound) {
		t.Fatalf("err = %v, want %v and forgehttp.ErrNotFound", err, kind)
	}
}

func TestGitLabCapabilities(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	caps, err := g.c.Capabilities("gl")
	if err != nil || caps != (Capabilities{MergeBase: true}) {
		t.Fatalf("caps = %+v, %v", caps, err)
	}
}

func TestGitLabResolve(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	fx := g.fx
	o := gitlabOID
	tests := []struct {
		rev    string
		commit OID
		ref    string
	}{
		{"", o(fx.Head), "refs/heads/main"},
		{"HEAD", o(fx.Head), "refs/heads/main"},
		{string(o(fx.Head)), o(fx.Head), ""},
		{strings.ToUpper(string(o(fx.Merge))), o(fx.Merge), ""},
		{"refs/heads/feature", o(fx.Feature[2]), "refs/heads/feature"},
		{"feature", o(fx.Feature[2]), "refs/heads/feature"},
		{"refs/tags/v1.0", o(fx.Main[3]), "refs/tags/v1.0"},
		{"v1.0", o(fx.Main[3]), "refs/tags/v1.0"},
		{"chain", o(fx.Main[1]), "refs/tags/chain"},
		{"light", o(fx.Main[0]), "refs/tags/light"},
		{"refs/heads/clash", o(fx.ClashBranch), "refs/heads/clash"},
		{"refs/tags/clash", o(fx.ClashTag), "refs/tags/clash"},
		{"HEAD~1", o(fx.Merge), "refs/heads/main"},
		{"main~2", o(fx.Main[4]), "refs/heads/main"},
		{"HEAD~1^2", o(fx.Feature[1]), "refs/heads/main"},
		{"HEAD^^2~1", o(fx.Feature[0]), "refs/heads/main"},
		{"HEAD~6", o(fx.Main[0]), "refs/heads/main"},
		{"v1.0^0", o(fx.Main[3]), "refs/tags/v1.0"},
	}
	for _, tt := range tests {
		got, err := g.c.Resolve(context.Background(), gitlabRepo, tt.rev)
		if err != nil || got != (Resolved{Commit: tt.commit, Ref: tt.ref}) {
			t.Errorf("Resolve(%q) = %+v, %v; want %s %s", tt.rev, got, err, tt.commit, tt.ref)
		}
	}

	missing := []string{
		"nope", "refs/heads/nope", "refs/tags/nope", "HEAD~7", "HEAD^2",
		string(o(fx.Tag)),                         // a tag object, not a commit
		string(o(fx.Blobs[fakeforge.PathReadme])), // a blob
		strings.Repeat("ab", 20),                  // no such object
	}
	for _, rev := range missing {
		_, err := g.c.Resolve(context.Background(), gitlabRepo, rev)
		gitlabNotFound(t, err, ErrRevisionNotFound)
	}
	if _, err := g.c.Resolve(context.Background(), gitlabRepo, "clash"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("clash: err = %v, want ErrAmbiguous", err)
	}
	if _, err := g.c.Resolve(context.Background(), gitlabRepo, string(o(fx.Head))[:12]); !errors.Is(err, ErrInvalid) {
		t.Errorf("short SHA: err = %v, want ErrInvalid", err)
	}
}

func TestGitLabResolveCalls(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	tests := []struct {
		rev   string
		calls int
	}{
		{string(gitlabOID(g.fx.Head)), 1}, // commit check
		{"refs/heads/main", 1},
		{"main", 2}, // tag and branch lookups
		{"HEAD", 2}, // project, branch
		{"HEAD~2", 4},
	}
	for _, tt := range tests {
		g.srv.ResetCalls()
		if _, err := g.c.Resolve(context.Background(), gitlabRepo, tt.rev); err != nil {
			t.Fatal(err)
		}
		if n := g.srv.Calls("", "*"); n != tt.calls {
			t.Errorf("Resolve(%q) made %d calls, want %d: %v", tt.rev, n, tt.calls, g.srv.Requests())
		}
	}
}

func TestGitLabEscaping(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	g.repo.SetRef("refs/heads/feat/x", g.fx.Main[1])
	g.repo.SetRef("refs/tags/rel/1.0", g.fx.Tag)
	for rev, want := range map[string]gitobj.ID{"feat/x": g.fx.Main[1], "refs/tags/rel/1.0": g.fx.Main[3]} {
		got, err := g.c.Resolve(context.Background(), gitlabRepo, rev)
		if err != nil || got.Commit != gitlabOID(want) {
			t.Errorf("Resolve(%q) = %+v, %v", rev, got, err)
		}
	}
	reqs := strings.Join(g.srv.Requests(), "\n")
	for _, want := range []string{
		"GET /projects/group%2Fsub%2Fproj/repository/branches/feat%2Fx?",
		"GET /projects/group%2Fsub%2Fproj/repository/tags/rel%2F1.0?",
	} {
		if !strings.Contains(reqs, want) {
			t.Errorf("no request %q in\n%s", want, reqs)
		}
	}
}

func TestGitLabListRefs(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	fx := g.fx
	o := gitlabOID
	list, err := g.c.ListRefs(context.Background(), gitlabRepo, RefsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Ref{
		{"refs/heads/clash", o(fx.ClashBranch), o(fx.ClashBranch)},
		{"refs/heads/feature", o(fx.Feature[2]), o(fx.Feature[2])},
		{"refs/heads/main", o(fx.Head), o(fx.Head)},
		{"refs/tags/chain", o(fx.TagOuter), o(fx.Main[1])},
		{"refs/tags/clash", o(fx.ClashTag), o(fx.ClashTag)},
		{"refs/tags/light", o(fx.Light), o(fx.Light)},
		{"refs/tags/v1.0", o(fx.Tag), o(fx.Main[3])},
	}
	if list.Default != "refs/heads/main" || list.Truncated || !slices.Equal(list.Refs, want) {
		t.Fatalf("list = %+v", list)
	}

	tags, err := g.c.ListRefs(context.Background(), gitlabRepo, RefsOptions{Prefix: tagsPrefix})
	if err != nil || len(tags.Refs) != 4 || tags.Truncated {
		t.Fatalf("tags = %+v, %v", tags, err)
	}
	for limit, n := range map[int]int{2: 2, 3: 3, 6: 6} {
		cut, err := g.c.ListRefs(context.Background(), gitlabRepo, RefsOptions{Limit: limit})
		if err != nil || len(cut.Refs) != n || !cut.Truncated || !slices.Equal(cut.Refs, want[:n]) {
			t.Errorf("limit %d: %+v, %v", limit, cut, err)
		}
	}
}

func TestGitLabListRefsPaging(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	for i := range 250 {
		g.repo.SetRef(fmt.Sprintf("refs/heads/b%03d", i), g.fx.Main[0])
	}
	list, err := g.c.ListRefs(context.Background(), gitlabRepo, RefsOptions{Prefix: headsPrefix})
	if err != nil || len(list.Refs) != 253 || list.Truncated {
		t.Fatalf("got %d refs, truncated %v, %v", len(list.Refs), list.Truncated, err)
	}
	if n := g.srv.Calls("GET", "*/repository/branches"); n != 3 {
		t.Errorf("%d branch pages, want 3", n)
	}

	g.srv.ResetCalls()
	list, err = g.c.ListRefs(context.Background(), gitlabRepo, RefsOptions{Prefix: headsPrefix, Limit: 120})
	if err != nil || len(list.Refs) != 120 || !list.Truncated {
		t.Fatalf("got %d refs, truncated %v, %v", len(list.Refs), list.Truncated, err)
	}
	if n := g.srv.Calls("GET", "*/repository/branches"); n != 2 {
		t.Errorf("%d branch pages, want 2", n)
	}

	// Running out of calls mid-listing truncates.
	small := newGitLabTest(t, Limits{MaxCalls: 2})
	for i := range 250 {
		small.repo.SetRef(fmt.Sprintf("refs/heads/b%03d", i), small.fx.Main[0])
	}
	list, err = small.c.ListRefs(context.Background(), gitlabRepo, RefsOptions{Prefix: headsPrefix})
	if err != nil || len(list.Refs) != 100 || !list.Truncated {
		t.Fatalf("budget: got %d refs, truncated %v, %v", len(list.Refs), list.Truncated, err)
	}
}

func TestGitLabEmptyRepository(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	g.srv.NewRepo("group/empty")
	r := RepositoryRef{Source: "gl", Repository: "group/empty"}
	list, err := g.c.ListRefs(context.Background(), r, RefsOptions{})
	if err != nil || list.Default != "" || len(list.Refs) != 0 || list.Truncated {
		t.Fatalf("list = %+v, %v", list, err)
	}
	_, err = g.c.Resolve(context.Background(), r, "HEAD")
	gitlabNotFound(t, err, ErrRevisionNotFound)
}

func TestGitLabCommit(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	fx := g.fx
	got, err := g.c.Commit(context.Background(), gitlabRepo, "v1.0")
	if err != nil {
		t.Fatal(err)
	}
	when := fakeforge.Epoch.Add(4 * time.Hour)
	sig := Signature{Name: "Fake Author", Email: "author@example.com"}
	if got.ID != gitlabOID(fx.Main[3]) || got.Tree != "" || !slices.Equal(got.Parents, []OID{gitlabOID(fx.Main[2])}) ||
		got.Message != "Rename and add unicode name" || !got.Author.When.Equal(when) || !got.Committer.When.Equal(when) ||
		got.Author.Name != sig.Name || got.Author.Email != sig.Email || got.Committer.Name != sig.Name {
		t.Fatalf("commit = %+v", got)
	}

	g.srv.ResetCalls()
	merge, err := g.c.Commit(context.Background(), gitlabRepo, "HEAD~1")
	if err != nil || !slices.Equal(merge.Parents, []OID{gitlabOID(fx.Main[4]), gitlabOID(fx.Feature[1])}) {
		t.Fatalf("merge = %+v, %v", merge, err)
	}
	// project, branch, Head; the merge is read once.
	if n := g.srv.Calls("", "*"); n != 4 {
		t.Errorf("%d calls, want 4: %v", n, g.srv.Requests())
	}

	root, err := g.c.Commit(context.Background(), gitlabRepo, "light")
	if err != nil || len(root.Parents) != 0 {
		t.Fatalf("root = %+v, %v", root, err)
	}
}

// gitlabHistory pages through History and returns every commit ID.
func gitlabHistory(t *testing.T, g *gitlabTest, opts HistoryOptions) ([]OID, OID) {
	t.Helper()
	var ids []OID
	var tip OID
	for range 100 {
		page, err := g.c.History(context.Background(), gitlabRepo, opts)
		if err != nil {
			t.Fatal(err)
		}
		if tip != "" && page.Tip != tip {
			t.Fatalf("tip moved from %s to %s", tip, page.Tip)
		}
		tip = page.Tip
		if len(page.Commits) > cmp(opts.PageSize, defaultPageSize) {
			t.Fatalf("page of %d commits", len(page.Commits))
		}
		for _, c := range page.Commits {
			ids = append(ids, c.ID)
		}
		if page.Next == "" {
			return ids, tip
		}
		opts = HistoryOptions{Cursor: page.Next}
	}
	t.Fatal("history does not end")
	return nil, ""
}

// gitlabLog is the fixture's git log, as OIDs.
func gitlabLog(t *testing.T, g *gitlabTest, tip gitobj.ID, path string) []OID {
	t.Helper()
	ids, err := g.repo.Log(tip, fakeforge.LogOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]OID, len(ids))
	for i, id := range ids {
		out[i] = gitlabOID(id)
	}
	return out
}

func TestGitLabHistory(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	fx := g.fx
	want := gitlabLog(t, g, fx.Head, "")
	// The hook moves main on every listing; pages stay on the first tip.
	flip := false
	g.srv.SetHook(func(r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/repository/commits") {
			flip = !flip
			to := fx.Head
			if flip {
				to = fx.Feature[2]
			}
			g.repo.SetRef("refs/heads/main", to)
		}
	})
	for _, size := range []int{1, 2, 3, 4, 100} {
		g.repo.SetRef("refs/heads/main", fx.Head)
		got, tip := gitlabHistory(t, g, HistoryOptions{Revision: "main", PageSize: size})
		if tip != gitlabOID(fx.Head) || !slices.Equal(got, want) {
			t.Errorf("size %d: tip %s, got %v, want %v", size, tip, got, want)
		}
	}
	g.srv.SetHook(nil)
	g.repo.SetRef("refs/heads/main", fx.Head)

	for _, path := range []string{fakeforge.PathFeature, fakeforge.PathReadme, "docs", fakeforge.PathUnicode} {
		want := gitlabLog(t, g, fx.Head, path)
		got, _ := gitlabHistory(t, g, HistoryOptions{Revision: "HEAD", Path: path, PageSize: 2})
		if len(want) == 0 || !slices.Equal(got, want) {
			t.Errorf("path %q: got %v, want %v", path, got, want)
		}
	}

	page, err := g.c.History(context.Background(), gitlabRepo, HistoryOptions{Path: "nope.txt"})
	if err != nil || len(page.Commits) != 0 || page.Next != "" {
		t.Fatalf("no history: %+v, %v", page, err)
	}
	if q := strings.Join(g.srv.Requests(), "\n"); !strings.Contains(q, "follow=false") {
		t.Errorf("path filter without follow=false:\n%s", q)
	}
}

func TestGitLabTree(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	fx := g.fx
	ctx := context.Background()

	check := func(tr Tree, root gitobj.ID, dir string, recursive bool) {
		t.Helper()
		list, err := g.repo.ListTree(root, recursive)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, p := range list {
			path := p.Path
			if dir != "" {
				path = dir + "/" + path
			}
			want = append(want, path)
		}
		slices.Sort(want)
		var got []string
		for _, e := range tr.Entries {
			got = append(got, e.Path)
			if e.Size != -1 || e.Type != typeOf(e.Mode) || !e.ID.Valid() {
				t.Errorf("entry %+v", e)
			}
		}
		if tr.ID != "" || tr.Commit != gitlabOID(fx.Head) || tr.Path != dir || tr.Truncated || !slices.Equal(got, want) {
			t.Errorf("tree %q: %+v\nwant %v", dir, tr, want)
		}
	}
	rootTree, err := g.repo.Lookup(fx.Head, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range []bool{false, true} {
		tr, err := g.c.Tree(ctx, gitlabRepo, "main", TreeOptions{Recursive: rec})
		if err != nil {
			t.Fatal(err)
		}
		check(tr, rootTree.ID, "", rec)
	}
	docs, _ := g.repo.Lookup(fx.Head, "docs")
	tr, err := g.c.Tree(ctx, gitlabRepo, "main", TreeOptions{Path: "docs", Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	check(tr, docs.ID, "docs", true)

	byPath := map[string]Entry{}
	tr, _ = g.c.Tree(ctx, gitlabRepo, "main", TreeOptions{Recursive: true})
	for _, e := range tr.Entries {
		byPath[e.Path] = e
	}
	for path, mode := range map[string]FileMode{
		fakeforge.PathExec: ModeExec, fakeforge.PathSymlink: ModeSymlink, fakeforge.PathSubmodule: ModeSubmodule,
		"docs/guide": ModeTree, fakeforge.PathReadme: ModeFile,
	} {
		if byPath[path].Mode != mode {
			t.Errorf("%s: mode %v, want %v", path, byPath[path].Mode, mode)
		}
	}

	cut, err := g.c.Tree(ctx, gitlabRepo, "main", TreeOptions{Recursive: true, MaxEntries: 3})
	if err != nil || len(cut.Entries) != 3 || !cut.Truncated {
		t.Fatalf("cut = %+v, %v", cut, err)
	}
	for _, path := range []string{"nope", fakeforge.PathReadme, fakeforge.PathSubmodule, "docs/nope"} {
		_, err := g.c.Tree(ctx, gitlabRepo, "main", TreeOptions{Path: path})
		gitlabNotFound(t, err, ErrPathNotFound)
	}
}

func TestGitLabTreePaging(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	files := map[string]fakeforge.File{}
	for i := range 250 {
		files[fmt.Sprintf("d/f%03d", i)] = fakeforge.File{Data: []byte("same\n")} // equal blob IDs
	}
	files["top"] = fakeforge.File{Data: []byte("x")}
	big := g.repo.CommitTree(g.repo.BuildTree(files), fakeforge.Epoch, "big\n")
	g.repo.SetRef("refs/heads/big", big)

	g.srv.ResetCalls()
	tr, err := g.c.Tree(context.Background(), gitlabRepo, "refs/heads/big", TreeOptions{Path: "d"})
	if err != nil || len(tr.Entries) != 250 || tr.Truncated {
		t.Fatalf("got %d entries, truncated %v, %v", len(tr.Entries), tr.Truncated, err)
	}
	if n := g.srv.Calls("GET", "*/repository/tree"); n != 3 {
		t.Errorf("%d tree pages, want 3", n)
	}
	if !slices.IsSortedFunc(tr.Entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) }) {
		t.Error("entries not sorted")
	}

	// Stat finds an entry on a later page.
	pi, err := g.c.Stat(context.Background(), gitlabRepo, "refs/heads/big", "d/f249")
	if err != nil || pi.Path != "d/f249" || pi.Name != "f249" || pi.Commit != gitlabOID(big) {
		t.Fatalf("stat = %+v, %v", pi, err)
	}

	// The call budget cuts a listing short.
	small := newGitLabTest(t, Limits{MaxCalls: 3})
	big = small.repo.CommitTree(small.repo.BuildTree(files), fakeforge.Epoch, "big\n")
	tr, err = small.c.Tree(context.Background(), gitlabRepo, string(gitlabOID(big)), TreeOptions{Recursive: true})
	if err != nil || len(tr.Entries) != 200 || !tr.Truncated {
		t.Fatalf("budget: got %d entries, truncated %v, %v", len(tr.Entries), tr.Truncated, err)
	}
}

func TestGitLabStat(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	fx := g.fx
	tests := []struct {
		path string
		mode FileMode
		typ  ObjectType
		id   OID
	}{
		{fakeforge.PathSymlink, ModeSymlink, ObjectBlob, gitlabOID(fx.Blobs[fakeforge.PathSymlink])},
		{fakeforge.PathExec, ModeExec, ObjectBlob, gitlabOID(fx.Blobs[fakeforge.PathExec])},
		{fakeforge.PathSubmodule, ModeSubmodule, ObjectCommit, gitlabOID(fakeforge.SubmoduleCommit)},
		{fakeforge.PathNested, ModeFile, ObjectBlob, gitlabOID(fx.Blobs[fakeforge.PathNested])},
		{fakeforge.PathUnicode, ModeFile, ObjectBlob, gitlabOID(fx.Blobs[fakeforge.PathUnicode])},
		{"docs/guide", ModeTree, ObjectTree, ""},
	}
	for _, tt := range tests {
		pi, err := g.c.Stat(context.Background(), gitlabRepo, "main", tt.path)
		if err != nil || pi.Mode != tt.mode || pi.Type != tt.typ || (tt.id != "" && pi.ID != tt.id) ||
			pi.Path != tt.path || pi.Commit != gitlabOID(fx.Head) || pi.Size != -1 {
			t.Errorf("Stat(%q) = %+v, %v", tt.path, pi, err)
		}
	}
	for _, path := range []string{"nope", "docs/nope", "nope/deeper", fakeforge.PathReadme + "/x"} {
		_, err := g.c.Stat(context.Background(), gitlabRepo, "main", path)
		gitlabNotFound(t, err, ErrPathNotFound)
	}
}

// gitlabRead returns a func that reads a whole Reader and closes it.
func gitlabRead(t *testing.T) func(*Reader, error) []byte {
	return func(r *Reader, err error) []byte {
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
}

func TestGitLabReadFile(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	fx := g.fx
	ctx := context.Background()
	for _, path := range []string{
		fakeforge.PathReadme, fakeforge.PathBinary, fakeforge.PathLarge, fakeforge.PathSymlink,
		fakeforge.PathExec, fakeforge.PathUnicode, fakeforge.PathLFS,
	} {
		want, err := g.repo.ReadBlob(fx.Blobs[path])
		if err != nil {
			t.Fatal(err)
		}
		r, err := g.c.ReadFile(ctx, gitlabRepo, "main", path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		blob := r.Blob
		got := gitlabRead(t)(r, nil)
		if !bytes.Equal(got, want) {
			t.Errorf("%s: content differs (%d bytes, want %d)", path, len(got), len(want))
		}
		wantBlob := Blob{ID: gitlabOID(fx.Blobs[path]), Commit: gitlabOID(fx.Head), Path: path,
			Size: int64(len(want)), LFSPointer: path == fakeforge.PathLFS}
		if blob != wantBlob {
			t.Errorf("%s: blob %+v, want %+v", path, blob, wantBlob)
		}
	}
	if got := gitlabRead(t)(g.c.ReadFile(ctx, gitlabRepo, "main", fakeforge.PathSymlink)); string(got) != fakeforge.PathReadme {
		t.Errorf("symlink content %q", got)
	}

	// One commit check, one parent listing, one raw read. repository/files
	// is never used: GitLab loads and hashes the whole blob for it.
	g.srv.ResetCalls()
	gitlabRead(t)(g.c.ReadFile(ctx, gitlabRepo, string(gitlabOID(fx.Head)), fakeforge.PathReadme))
	if n := g.srv.Calls("", "*"); n != 3 {
		t.Errorf("%d calls, want 3: %v", n, g.srv.Requests())
	}
	if n := g.srv.Calls("", "*/repository/files/*"); n != 0 {
		t.Errorf("%d repository/files calls, want 0: %v", n, g.srv.Requests())
	}

	for _, path := range []string{"docs", "docs/guide", fakeforge.PathSubmodule} {
		if _, err := g.c.ReadFile(ctx, gitlabRepo, "main", path); !errors.Is(err, ErrNotFile) {
			t.Errorf("%s: err = %v, want ErrNotFile", path, err)
		}
	}
	for _, path := range []string{"nope", "docs/nope", fakeforge.PathReadme + "/x"} {
		_, err := g.c.ReadFile(ctx, gitlabRepo, "main", path)
		gitlabNotFound(t, err, ErrPathNotFound)
	}
	// An older revision: the file was renamed later.
	old := gitlabRead(t)(g.c.ReadFile(ctx, gitlabRepo, "refs/tags/chain", fakeforge.PathOldName))
	if string(old) != "this file moves\n" {
		t.Errorf("old file %q", old)
	}
	_, err := g.c.ReadFile(ctx, gitlabRepo, "refs/tags/chain", fakeforge.PathNewName)
	gitlabNotFound(t, err, ErrPathNotFound)
}

func TestGitLabReadLimits(t *testing.T) {
	g := newGitLabTest(t, Limits{MaxBlobBytes: 1000})
	ctx := context.Background()
	g.srv.ResetCalls()
	if _, err := g.c.ReadFile(ctx, gitlabRepo, "main", fakeforge.PathLarge); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if n := g.srv.Calls("", "*/repository/files/*"); n != 0 {
		t.Errorf("asked repository/files, which reads the whole blob: %v", g.srv.Requests())
	}
	if _, err := g.c.ReadBlob(ctx, gitlabRepo, gitlabOID(g.fx.Blobs[fakeforge.PathLarge])); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("blob: err = %v, want ErrTooLarge", err)
	}
	small := gitlabRead(t)(g.c.ReadFile(ctx, gitlabRepo, "main", fakeforge.PathReadme))
	if len(small) == 0 {
		t.Error("small file empty")
	}
}

func TestGitLabReadBlob(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	ctx := context.Background()
	for _, path := range []string{fakeforge.PathBinary, fakeforge.PathLarge, fakeforge.PathLFS} {
		id := g.fx.Blobs[path]
		want, _ := g.repo.ReadBlob(id)
		r, err := g.c.ReadBlob(ctx, gitlabRepo, gitlabOID(id))
		if err != nil {
			t.Fatal(err)
		}
		blob := r.Blob
		if got := gitlabRead(t)(r, nil); !bytes.Equal(got, want) {
			t.Errorf("%s: content differs", path)
		}
		wantBlob := Blob{ID: gitlabOID(id), Size: int64(len(want)), LFSPointer: path == fakeforge.PathLFS}
		if blob != wantBlob {
			t.Errorf("%s: blob %+v, want %+v", path, blob, wantBlob)
		}
	}
	for _, id := range []gitobj.ID{g.fx.Head, gitobj.Hash(gitobj.TypeBlob, []byte("absent"))} {
		_, err := g.c.ReadBlob(ctx, gitlabRepo, gitlabOID(id))
		gitlabNotFound(t, err, ErrRevisionNotFound)
	}
}

func TestGitLabMergeBase(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	fx := g.fx
	ctx := context.Background()
	tests := []struct {
		a, b string
		want gitobj.ID
	}{
		{"main", "feature", fx.MergeBase},
		{"feature", "main", fx.MergeBase},
		{string(gitlabOID(fx.Main[1])), "main", fx.Main[1]},
		{"main", "main", fx.Head},
		{"v1.0", "refs/heads/clash", fx.Main[2]},
	}
	for _, tt := range tests {
		got, err := g.c.MergeBase(ctx, gitlabRepo, tt.a, tt.b)
		if err != nil || got != gitlabOID(tt.want) {
			t.Errorf("MergeBase(%s, %s) = %s, %v; want %s", tt.a, tt.b, got, err, tt.want)
		}
	}

	orphan := g.repo.CommitTree(g.repo.BuildTree(map[string]fakeforge.File{"x": {Data: []byte("x")}}), fakeforge.Epoch, "orphan\n")
	g.repo.SetRef("refs/heads/orphan", orphan)
	if _, err := g.c.MergeBase(ctx, gitlabRepo, "main", "orphan"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unrelated: err = %v, want ErrInvalid", err)
	}
}

func TestGitLabErrors(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	ctx := context.Background()

	bad, err := g.srv.SessionWithToken("wrong")
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Source{ID: "gl", Session: bad})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve(ctx, gitlabRepo, "refs/heads/main"); !errors.Is(err, forgehttp.ErrUnauthorized) {
		t.Errorf("bad token: err = %v", err)
	}

	g.srv.Inject(fakeforge.Fault{Path: "*/repository/branches/*", Status: http.StatusForbidden,
		Body: `{"message":"403 Forbidden"}`, Times: 1})
	if _, err := g.c.Resolve(ctx, gitlabRepo, "refs/heads/main"); !errors.Is(err, forgehttp.ErrForbidden) {
		t.Errorf("403: err = %v", err)
	}
	g.srv.Inject(fakeforge.Fault{Path: "*", Status: http.StatusTooManyRequests,
		Header: http.Header{"Retry-After": {"30"}, "Ratelimit-Remaining": {"0"}}, Times: 1})
	if _, err := g.c.Tree(ctx, gitlabRepo, "main", TreeOptions{}); !errors.Is(err, forgehttp.ErrRateLimited) {
		t.Errorf("429: err = %v", err)
	}
	g.srv.Inject(fakeforge.Fault{Path: "*/repository/tree", Status: http.StatusInternalServerError, Times: 1})
	if _, err := g.c.Tree(ctx, gitlabRepo, "main", TreeOptions{}); err == nil || errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("500: err = %v", err)
	}

	// A private project without access, or a missing one, is the forge's
	// 404, not a missing revision or path.
	g.repo.SetPrivate(true)
	g.srv.SetAccess(gitlabRepo.Repository, fakeforge.AccessNone)
	missing := RepositoryRef{Source: "gl", Repository: "group/nope"}
	for _, r := range []RepositoryRef{gitlabRepo, missing} {
		for _, rev := range []string{"main", "refs/heads/main", "HEAD", string(gitlabOID(g.fx.Head))} {
			_, err := g.c.Resolve(ctx, r, rev)
			if !errors.Is(err, forgehttp.ErrNotFound) || errors.Is(err, ErrRevisionNotFound) || errors.Is(err, forgehttp.ErrUnauthorized) {
				t.Errorf("%s %s: err = %v", r.Repository, rev, err)
			}
		}
		_, err := g.c.ReadBlob(ctx, r, gitlabOID(g.fx.Blobs[fakeforge.PathReadme]))
		if !errors.Is(err, forgehttp.ErrNotFound) || errors.Is(err, ErrRevisionNotFound) {
			t.Errorf("%s blob: err = %v", r.Repository, err)
		}
	}
	g.srv.SetAccess(gitlabRepo.Repository, fakeforge.AccessRead)
	if _, err := g.c.Resolve(ctx, gitlabRepo, "main"); err != nil {
		t.Errorf("read access: %v", err)
	}
}

func TestGitLabMissingFileOnHiddenProject(t *testing.T) {
	g := newGitLabTest(t, Limits{})
	ctx := context.Background()
	// The project disappears after the revision resolves: the parent
	// listing reports the project, not a missing path.
	g.srv.SetHook(func(r *http.Request) {
		if strings.Contains(r.URL.Path, "/repository/tree") {
			g.repo.SetPrivate(true)
			g.srv.SetAccess(gitlabRepo.Repository, fakeforge.AccessNone)
		}
	})
	_, err := g.c.ReadFile(ctx, gitlabRepo, string(gitlabOID(g.fx.Head)), fakeforge.PathReadme)
	if !errors.Is(err, forgehttp.ErrNotFound) || errors.Is(err, ErrPathNotFound) {
		t.Fatalf("err = %v", err)
	}
}
