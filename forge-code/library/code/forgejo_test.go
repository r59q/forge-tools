package code

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/fakeforge"
	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// forgejoTestRepo names the seeded repository on the Forgejo test server.
var forgejoTestRepo = RepositoryRef{Source: "fj", Repository: "o/r"}

// forgejoEnv is a Forgejo test server with a seeded repository.
type forgejoEnv struct {
	s    *fakeforge.Server
	repo *fakeforge.Repo
	fx   fakeforge.Fixture
	c    *Client
}

func newForgejoEnv(t *testing.T, opts fakeforge.ForgejoOptions, l Limits) *forgejoEnv {
	t.Helper()
	s := fakeforge.NewServer(t, forgeauth.Forgejo, fakeforge.ForgejoAPIWith(opts))
	repo := s.NewRepo("o/r")
	fx := fakeforge.Seed(repo)
	c, err := NewWithLimits(l, Source{ID: "fj", Session: s.Session})
	if err != nil {
		t.Fatal(err)
	}
	return &forgejoEnv{s: s, repo: repo, fx: fx, c: c}
}

func forgejoOID(id gitobj.ID) OID { return OID(id.String()) }

// forgejoMissing checks that err matches kind and forgehttp.ErrNotFound
// without carrying the forge's 400 or 422.
func forgejoMissing(t *testing.T, err, kind error) {
	t.Helper()
	if !errors.Is(err, kind) || !errors.Is(err, forgehttp.ErrNotFound) {
		t.Fatalf("err = %v, want %v and forgehttp.ErrNotFound", err, kind)
	}
	var fe *forgehttp.Error
	if errors.As(err, &fe) && fe.StatusCode != http.StatusNotFound {
		t.Fatalf("err wraps forge status %d", fe.StatusCode)
	}
}

func TestForgejoCapabilities(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{}, Limits{})
	caps, err := e.c.Capabilities("fj")
	if err != nil || caps != (Capabilities{TreeIDs: true, MergeBase: true}) {
		t.Fatalf("caps = %+v, %v", caps, err)
	}
}

func TestForgejoResolve(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{}, Limits{})
	fx := e.fx
	head := forgejoOID(fx.Head)
	cases := []struct {
		rev    string
		commit gitobj.ID
		ref    string
	}{
		{string(head), fx.Head, ""},
		{strings.ToUpper(string(head)), fx.Head, ""},
		{"", fx.Head, "refs/heads/main"},
		{"HEAD", fx.Head, "refs/heads/main"},
		{"refs/heads/feature", fx.Feature[2], "refs/heads/feature"},
		{"refs/tags/v1.0", fx.Main[3], "refs/tags/v1.0"},
		{"refs/tags/chain", fx.Main[1], "refs/tags/chain"},
		{"v1.0", fx.Main[3], "refs/tags/v1.0"},
		{"feature", fx.Feature[2], "refs/heads/feature"},
		{"light", fx.Light, "refs/tags/light"},
		{"refs/heads/clash", fx.ClashBranch, "refs/heads/clash"},
		{"refs/tags/clash", fx.ClashTag, "refs/tags/clash"},
		{"main~2", fx.Main[4], "refs/heads/main"},
		{"main^", fx.Merge, "refs/heads/main"},
		{"main~1^2", fx.Feature[1], "refs/heads/main"},
		{"HEAD~1^2~1", fx.Feature[0], "refs/heads/main"},
		{string(head) + "^", fx.Merge, ""},
	}
	for _, tc := range cases {
		t.Run(tc.rev, func(t *testing.T) {
			got, err := e.c.Resolve(context.Background(), forgejoTestRepo, tc.rev)
			if err != nil {
				t.Fatal(err)
			}
			if want := (Resolved{Commit: forgejoOID(tc.commit), Ref: tc.ref}); got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}

	if _, err := e.c.Resolve(context.Background(), forgejoTestRepo, "clash"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("clash: err = %v, want ErrAmbiguous", err)
	}
	for _, rev := range []string{
		"nope", "refs/heads/nope",
		"refs/heads/mai", "refs/tags/v1", // the forge matches these by prefix
		strings.Repeat("ab", 20),   // missing commit
		string(forgejoOID(fx.Tag)), // tag object: the forge peels it
		string(forgejoOID(fx.Blobs[fakeforge.PathReadme])),
		"main~100", "main^3",
	} {
		_, err := e.c.Resolve(context.Background(), forgejoTestRepo, rev)
		forgejoMissing(t, err, ErrRevisionNotFound)
	}
	if _, err := e.c.Resolve(context.Background(), forgejoTestRepo, string(head[:12])); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short SHA: err = %v, want ErrInvalid", err)
	}
}

func TestForgejoResolveCalls(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{}, Limits{})
	ctx := context.Background()
	cases := []struct {
		rev  string
		want int
	}{
		{string(forgejoOID(e.fx.Head)), 1}, // git/commits
		{"refs/heads/main", 1},             // git/refs
		{"refs/tags/chain", 2},             // git/refs, git/commits peels the chain
		{"main", 2},                        // tag and branch in parallel
		{"HEAD", 2},                        // repo, git/refs
		{"main~2", 4},                      // two git/refs, two git/commits
	}
	for _, tc := range cases {
		e.s.ResetCalls()
		if _, err := e.c.Resolve(ctx, forgejoTestRepo, tc.rev); err != nil {
			t.Fatal(err)
		}
		if got := e.s.Calls("", "*"); got != tc.want {
			t.Errorf("%s: %d calls, want %d: %v", tc.rev, got, tc.want, e.s.Requests())
		}
	}
}

func TestForgejoSingleRefObject(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{SingleRefObject: true}, Limits{})
	var raw json.RawMessage
	if err := forgerest.Send(context.Background(), e.s.Session, "GET", "/repos/o/r/git/refs/heads/main", nil, &raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		t.Fatalf("front sent %s, want an object", raw)
	}
	got, err := e.c.Resolve(context.Background(), forgejoTestRepo, "refs/heads/main")
	if err != nil || got.Commit != forgejoOID(e.fx.Head) {
		t.Fatalf("got %+v, %v", got, err)
	}
	got, err = e.c.Resolve(context.Background(), forgejoTestRepo, "refs/tags/v1.0")
	if err != nil || got.Commit != forgejoOID(e.fx.Main[3]) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestForgejoListRefs(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{}, Limits{})
	fx := e.fx
	ctx := context.Background()
	ref := func(name string, target, commit gitobj.ID) Ref {
		return Ref{Name: name, Target: forgejoOID(target), Commit: forgejoOID(commit)}
	}
	want := []Ref{
		ref("refs/heads/clash", fx.ClashBranch, fx.ClashBranch),
		ref("refs/heads/feature", fx.Feature[2], fx.Feature[2]),
		ref("refs/heads/main", fx.Head, fx.Head),
		// Documented gap: /tags peels one level and cannot tell the
		// unlisted inner tag object from a commit.
		ref("refs/tags/chain", fx.TagOuter, fx.TagInner),
		ref("refs/tags/clash", fx.ClashTag, fx.ClashTag),
		ref("refs/tags/light", fx.Light, fx.Light),
		ref("refs/tags/v1.0", fx.Tag, fx.Main[3]),
	}

	e.s.ResetCalls()
	list, err := e.c.ListRefs(ctx, forgejoTestRepo, RefsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Default != "refs/heads/main" || list.Truncated || !slices.Equal(list.Refs, want) {
		t.Fatalf("list = %+v", list)
	}
	// repo, git/refs/heads/, git/refs/tags/, one /tags page; no N+1.
	if n := e.s.Calls("", "*"); n != 4 || e.s.Calls("", "*/git/tags/*") != 0 {
		t.Fatalf("%d calls: %v", n, e.s.Requests())
	}

	for _, tc := range []struct {
		opts      RefsOptions
		n         int
		truncated bool
	}{
		{RefsOptions{Limit: 2}, 2, true},
		{RefsOptions{Limit: 3}, 3, true},
		{RefsOptions{Limit: 7}, 7, false},
		{RefsOptions{Prefix: "refs/tags/"}, 4, false},
		{RefsOptions{Prefix: "refs/tags/", Limit: 1}, 1, true},
	} {
		list, err := e.c.ListRefs(ctx, forgejoTestRepo, tc.opts)
		if err != nil || len(list.Refs) != tc.n || list.Truncated != tc.truncated {
			t.Fatalf("%+v: %+v, %v", tc.opts, list, err)
		}
	}

	t.Run("listed inner tag", func(t *testing.T) {
		e.repo.SetRef("refs/tags/chain-inner", fx.TagInner)
		defer e.repo.SetRef("refs/tags/chain-inner", gitobj.ZeroID)
		list, err := e.c.ListRefs(ctx, forgejoTestRepo, RefsOptions{Prefix: "refs/tags/"})
		if err != nil {
			t.Fatal(err)
		}
		if got := list.Refs[0]; got != ref("refs/tags/chain", fx.TagOuter, fx.Main[1]) {
			t.Fatalf("chain = %+v", got)
		}
		// The same with Limit 1, where the /tags walk stops before the
		// inner tag's entry.
		list, err = e.c.ListRefs(ctx, forgejoTestRepo, RefsOptions{Prefix: "refs/tags/", Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if got := list.Refs[0]; got != ref("refs/tags/chain", fx.TagOuter, fx.Main[1]) {
			t.Fatalf("Limit 1: chain = %+v", got)
		}
	})

	t.Run("deep chain", func(t *testing.T) {
		// Five tags deep, each with its own ref: ListRefs and Resolve both
		// peel to the commit.
		id := fx.Main[0]
		for i := range 5 {
			id = e.repo.WriteTag(gitobj.Tag{Object: id, Name: fmt.Sprintf("deep%d", i), Message: "x\n"})
			e.repo.SetRef(fmt.Sprintf("refs/tags/deep%d", i), id)
		}
		defer func() {
			for i := range 5 {
				e.repo.SetRef(fmt.Sprintf("refs/tags/deep%d", i), gitobj.ZeroID)
			}
		}()
		list, err := e.c.ListRefs(ctx, forgejoTestRepo, RefsOptions{Prefix: "refs/tags/"})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range list.Refs {
			if strings.HasPrefix(r.Name, "refs/tags/deep") && r.Commit != forgejoOID(fx.Main[0]) {
				t.Errorf("%s = %+v, want commit %s", r.Name, r, fx.Main[0])
			}
		}
		got, err := e.c.Resolve(ctx, forgejoTestRepo, "refs/tags/deep4")
		if err != nil || got.Commit != forgejoOID(fx.Main[0]) {
			t.Fatalf("Resolve deep4 = %+v, %v", got, err)
		}
	})

	t.Run("tag gone from /tags", func(t *testing.T) {
		// The tag disappears between git/refs and /tags: git/commits peels
		// it.
		var once atomic.Bool
		e.s.SetHook(func(r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/repos/o/r/tags") && once.CompareAndSwap(false, true) {
				e.repo.SetRef("refs/tags/v1.0", gitobj.ZeroID)
			}
		})
		defer func() { e.s.SetHook(nil); e.repo.SetRef("refs/tags/v1.0", fx.Tag) }()
		e.s.ResetCalls()
		list, err := e.c.ListRefs(ctx, forgejoTestRepo, RefsOptions{Prefix: "refs/tags/"})
		if err != nil {
			t.Fatal(err)
		}
		if got := list.Refs[3]; got != ref("refs/tags/v1.0", fx.Tag, fx.Main[3]) {
			t.Fatalf("v1.0 = %+v", got)
		}
		if n := e.s.Calls("GET", "/repos/o/r/git/commits/"+fx.Tag.String()); n != 1 {
			t.Fatalf("%d git/commits calls: %v", n, e.s.Requests())
		}
	})

	t.Run("budget", func(t *testing.T) {
		c, err := NewWithLimits(Limits{MaxCalls: 2}, Source{ID: "fj", Session: e.s.Session})
		if err != nil {
			t.Fatal(err)
		}
		// repo and git/refs fit; /tags does not, so only complete refs
		// before the first annotated tag are returned.
		list, err := c.ListRefs(ctx, forgejoTestRepo, RefsOptions{Prefix: "refs/tags/"})
		if err != nil || !list.Truncated || len(list.Refs) != 0 {
			t.Fatalf("list = %+v, %v", list, err)
		}
	})

	t.Run("empty", func(t *testing.T) {
		e.s.NewRepo("o/empty")
		list, err := e.c.ListRefs(ctx, RepositoryRef{Source: "fj", Repository: "o/empty"}, RefsOptions{})
		if err != nil || len(list.Refs) != 0 || list.Default != "" {
			t.Fatalf("list = %+v, %v", list, err)
		}
		_, err = e.c.Resolve(ctx, RepositoryRef{Source: "fj", Repository: "o/empty"}, "HEAD")
		forgejoMissing(t, err, ErrRevisionNotFound)
	})
}

// forgejoCommitOf returns the expected Commit for id.
func forgejoCommitOf(t *testing.T, repo *fakeforge.Repo, id gitobj.ID) Commit {
	t.Helper()
	c, err := repo.ReadCommit(id)
	if err != nil {
		t.Fatal(err)
	}
	out := Commit{
		ID:        forgejoOID(id),
		Tree:      forgejoOID(c.Tree),
		Author:    Signature{Name: c.Author.Name, Email: c.Author.Email, When: c.Author.When},
		Committer: Signature{Name: c.Committer.Name, Email: c.Committer.Email, When: c.Committer.When},
		Message:   strings.TrimSuffix(c.Message, "\n"),
	}
	for _, p := range c.Parents {
		out.Parents = append(out.Parents, forgejoOID(p))
	}
	return out
}

// forgejoSameCommit compares commits, with times by instant.
func forgejoSameCommit(a, b Commit) bool {
	return a.ID == b.ID && a.Tree == b.Tree && slices.Equal(a.Parents, b.Parents) && a.Message == b.Message &&
		a.Author.Name == b.Author.Name && a.Author.Email == b.Author.Email && a.Author.When.Equal(b.Author.When) &&
		a.Committer.Name == b.Committer.Name && a.Committer.Email == b.Committer.Email && a.Committer.When.Equal(b.Committer.When)
}

func TestForgejoCommit(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{}, Limits{})
	ctx := context.Background()
	for _, id := range []gitobj.ID{e.fx.Head, e.fx.Merge, e.fx.Main[0]} {
		got, err := e.c.Commit(ctx, forgejoTestRepo, string(forgejoOID(id)))
		if err != nil {
			t.Fatal(err)
		}
		if want := forgejoCommitOf(t, e.repo, id); !forgejoSameCommit(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
	e.s.ResetCalls()
	got, err := e.c.Commit(ctx, forgejoTestRepo, "main~1")
	if err != nil || got.ID != forgejoOID(e.fx.Merge) || len(got.Parents) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	// Two git/refs (tag and branch), git/commits for main and its parent.
	if n := e.s.Calls("", "*"); n != 4 {
		t.Fatalf("%d calls: %v", n, e.s.Requests())
	}
}

// forgejoAllHistory pages through History and returns every commit ID.
func forgejoAllHistory(t *testing.T, c *Client, r RepositoryRef, opts HistoryOptions, between func()) ([]OID, OID) {
	t.Helper()
	var ids []OID
	var tip OID
	for i := 0; ; i++ {
		page, err := c.History(context.Background(), r, opts)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			tip = page.Tip
		} else if page.Tip != tip {
			t.Fatalf("tip moved from %s to %s", tip, page.Tip)
		}
		if opts.PageSize > 0 && len(page.Commits) > opts.PageSize {
			t.Fatalf("page of %d, size %d", len(page.Commits), opts.PageSize)
		}
		for _, cm := range page.Commits {
			ids = append(ids, cm.ID)
		}
		if page.Next == "" {
			return ids, tip
		}
		if i > 1000 {
			t.Fatal("history does not end")
		}
		opts = HistoryOptions{Cursor: page.Next}
		if between != nil {
			between()
		}
	}
}

// forgejoLog returns the expected history as OIDs.
func forgejoWantLog(t *testing.T, repo *fakeforge.Repo, tip gitobj.ID, path string) []OID {
	t.Helper()
	ids, err := repo.Log(tip, fakeforge.LogOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	var out []OID
	for _, id := range ids {
		out = append(out, forgejoOID(id))
	}
	return out
}

func TestForgejoHistory(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{}, Limits{})
	fx := e.fx

	t.Run("snapshot", func(t *testing.T) {
		defer e.repo.SetRef("refs/heads/main", fx.Head)
		moved := false
		got, tip := forgejoAllHistory(t, e.c, forgejoTestRepo, HistoryOptions{Revision: "main", PageSize: 3}, func() {
			if !moved {
				e.repo.SetRef("refs/heads/main", fx.Feature[2])
				moved = true
			}
		})
		if tip != forgejoOID(fx.Head) || !slices.Equal(got, forgejoWantLog(t, e.repo, fx.Head, "")) {
			t.Fatalf("tip %s, got %v", tip, got)
		}
	})

	t.Run("path", func(t *testing.T) {
		for _, p := range []string{fakeforge.PathFeature, fakeforge.PathReadme, "docs", fakeforge.PathUnicode} {
			got, _ := forgejoAllHistory(t, e.c, forgejoTestRepo, HistoryOptions{Revision: "HEAD", Path: p, PageSize: 1}, nil)
			if want := forgejoWantLog(t, e.repo, fx.Head, p); !slices.Equal(got, want) || len(want) == 0 {
				t.Fatalf("%s: got %v, want %v", p, got, want)
			}
		}
	})

	t.Run("no history", func(t *testing.T) {
		page, err := e.c.History(context.Background(), forgejoTestRepo, HistoryOptions{Path: "nope.txt"})
		if err != nil || len(page.Commits) != 0 || page.Next != "" {
			t.Fatalf("page = %+v, %v", page, err)
		}
	})

	t.Run("calls", func(t *testing.T) {
		e.s.ResetCalls()
		page, err := e.c.History(context.Background(), forgejoTestRepo, HistoryOptions{Revision: string(forgejoOID(fx.Head))})
		if err != nil || len(page.Commits) != len(forgejoWantLog(t, e.repo, fx.Head, "")) {
			t.Fatalf("page = %d commits, %v", len(page.Commits), err)
		}
		if !forgejoSameCommit(page.Commits[0], forgejoCommitOf(t, e.repo, fx.Head)) {
			t.Fatalf("first = %+v", page.Commits[0])
		}
		// git/commits to check the ID, then one /commits page.
		if n := e.s.Calls("", "*"); n != 2 {
			t.Fatalf("%d calls: %v", n, e.s.Requests())
		}
	})
}

// forgejoLongRepo adds o/long: n commits on main, every third changing
// x.txt, and returns its tip.
func forgejoLongRepo(s *fakeforge.Server, n int) (*fakeforge.Repo, gitobj.ID) {
	repo := s.NewRepo("o/long")
	files := map[string]fakeforge.File{"x.txt": {Data: []byte("0\n")}, "y.txt": {Data: []byte("0\n")}}
	var tip gitobj.ID
	for i := range n {
		f := "y.txt"
		if i%3 == 0 {
			f = "x.txt"
		}
		files[f] = fakeforge.File{Data: fmt.Appendf(nil, "%d\n", i)}
		var parents []gitobj.ID
		if i > 0 {
			parents = []gitobj.ID{tip}
		}
		tip = repo.CommitTree(repo.BuildTree(files), fakeforge.Epoch.Add(time.Duration(i)*time.Minute), fmt.Sprintf("c%d\n", i), parents...)
	}
	repo.SetRef("refs/heads/main", tip)
	return repo, tip
}

func TestForgejoHistoryPaging(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{}, Limits{})
	repo, tip := forgejoLongRepo(e.s, 230)
	r := RepositoryRef{Source: "fj", Repository: "o/long"}
	for _, tc := range []struct {
		path string
		size int
	}{
		{"", 100}, {"", 7}, {"x.txt", 7}, {"x.txt", 50}, {"x.txt", 100}, {"y.txt", 33},
	} {
		got, _ := forgejoAllHistory(t, e.c, r, HistoryOptions{Path: tc.path, PageSize: tc.size}, nil)
		if want := forgejoWantLog(t, repo, tip, tc.path); !slices.Equal(got, want) {
			t.Fatalf("%q by %d: got %d commits, want %d", tc.path, tc.size, len(got), len(want))
		}
	}
	// The forge page is always 50, because of its path-skip bug.
	for _, req := range e.s.Requests() {
		if strings.Contains(req, "/commits?") && !strings.Contains(req, "limit=50") {
			t.Fatalf("request %s", req)
		}
	}
}

func TestForgejoHistoryServerSettings(t *testing.T) {
	// With a path, Forgejo skips (page-1)*COMMITS_RANGE_SIZE commits but
	// returns at most MAX_RESPONSE_ITEMS. Other settings than 50 and 50
	// leave gaps, which must fail, not come back as a short log.
	for _, tc := range []struct{ rangeSize, maxItems int }{{100, 50}, {50, 30}} {
		e := newForgejoEnv(t, fakeforge.ForgejoOptions{CommitsRangeSize: tc.rangeSize, MaxResponseItems: tc.maxItems}, Limits{})
		repo, tip := forgejoLongRepo(e.s, 230)
		r := RepositoryRef{Source: "fj", Repository: "o/long"}
		opts := HistoryOptions{Path: "x.txt", PageSize: 50}
		var err error
		for range 10 {
			var page HistoryPage
			if page, err = e.c.History(context.Background(), r, opts); err != nil || page.Next == "" {
				break
			}
			opts = HistoryOptions{Cursor: page.Next}
		}
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("range %d, items %d: path history err = %v, want ErrUnsupported", tc.rangeSize, tc.maxItems, err)
		}
		// Without a path the forge pages consistently.
		got, _ := forgejoAllHistory(t, e.c, r, HistoryOptions{PageSize: 100}, nil)
		if want := forgejoWantLog(t, repo, tip, ""); !slices.Equal(got, want) {
			t.Errorf("range %d, items %d: got %d commits, want %d", tc.rangeSize, tc.maxItems, len(got), len(want))
		}
	}
}

func TestForgejoHistoryLiteralPath(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{}, Limits{})
	repo := e.s.NewRepo("o/glob")
	files := map[string]fakeforge.File{"a[1].txt": {Data: []byte("0\n")}, "a1.txt": {Data: []byte("0\n")}}
	c0 := repo.CommitTree(repo.BuildTree(files), fakeforge.Epoch, "add\n")
	files["a1.txt"] = fakeforge.File{Data: []byte("1\n")}
	c1 := repo.CommitTree(repo.BuildTree(files), fakeforge.Epoch.Add(time.Minute), "a1\n", c0)
	files["a[1].txt"] = fakeforge.File{Data: []byte("2\n")}
	c2 := repo.CommitTree(repo.BuildTree(files), fakeforge.Epoch.Add(2*time.Minute), "a[1]\n", c1)
	repo.SetRef("refs/heads/main", c2)
	r := RepositoryRef{Source: "fj", Repository: "o/glob"}

	// Forgejo hands path to git as a pathspec, where [1] is a glob.
	got, _ := forgejoAllHistory(t, e.c, r, HistoryOptions{Path: "a[1].txt"}, nil)
	if want := []OID{forgejoOID(c2), forgejoOID(c0)}; !slices.Equal(got, want) {
		t.Errorf("a[1].txt = %v, want %v", got, want)
	}
	if got, _ := forgejoAllHistory(t, e.c, r, HistoryOptions{Path: "*.txt"}, nil); len(got) != 0 {
		t.Errorf("*.txt = %v, want none", got)
	}
}

// forgejoEntries returns the expected entries of a ListTree listing below
// dir.
func forgejoEntries(t *testing.T, repo *fakeforge.Repo, root gitobj.ID, dir string, recursive bool) []Entry {
	t.Helper()
	if dir != "" {
		e, err := repo.Lookup(root, dir)
		if err != nil {
			t.Fatal(err)
		}
		root = e.ID
	}
	list, err := repo.ListTree(root, recursive)
	if err != nil {
		t.Fatal(err)
	}
	var out []Entry
	for _, pe := range list {
		p := pe.Path
		if dir != "" {
			p = dir + "/" + p
		}
		mode := FileMode(pe.Mode)
		e := Entry{Name: pe.Name, Path: p, Mode: mode, Type: typeOf(mode), ID: forgejoOID(pe.ID), Size: -1}
		if e.Type == ObjectBlob {
			e.Size = pe.Size
		}
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	return out
}

func TestForgejoTree(t *testing.T) {
	for _, perPage := range []int{0, 2} {
		t.Run(fmt.Sprintf("per_page=%d", perPage), func(t *testing.T) {
			e := newForgejoEnv(t, fakeforge.ForgejoOptions{TreesPerPage: perPage}, Limits{})
			ctx := context.Background()
			head := string(forgejoOID(e.fx.Head))
			root, _ := e.repo.Lookup(e.fx.Head, "")
			for _, tc := range []struct {
				path      string
				recursive bool
			}{
				{"", false}, {"", true}, {"docs", false}, {"docs", true}, {"dir with space", true}, {"docs/guide", false},
			} {
				got, err := e.c.Tree(ctx, forgejoTestRepo, head, TreeOptions{Path: tc.path, Recursive: tc.recursive})
				if err != nil {
					t.Fatal(err)
				}
				want := forgejoEntries(t, e.repo, e.fx.Head, tc.path, tc.recursive)
				if !slices.Equal(got.Entries, want) || got.Truncated || got.Commit != OID(head) || got.Path != tc.path {
					t.Fatalf("%+v: got %+v\nwant %+v", tc, got, want)
				}
				if tc.path == "" && got.ID != forgejoOID(root.ID) {
					t.Fatalf("tree ID %s", got.ID)
				}
			}

			got, err := e.c.Tree(ctx, forgejoTestRepo, head, TreeOptions{Recursive: true, MaxEntries: 3})
			if err != nil || len(got.Entries) != 3 || !got.Truncated {
				t.Fatalf("max: %+v, %v", got, err)
			}
			all := forgejoEntries(t, e.repo, e.fx.Head, "", true)
			got, err = e.c.Tree(ctx, forgejoTestRepo, head, TreeOptions{Recursive: true, MaxEntries: len(all)})
			if err != nil || len(got.Entries) != len(all) || got.Truncated {
				t.Fatalf("exact max: %d entries, truncated %v, %v", len(got.Entries), got.Truncated, err)
			}

			for _, p := range []string{"nope", fakeforge.PathReadme, "docs/nope", fakeforge.PathReadme + "/x", fakeforge.PathSubmodule} {
				_, err := e.c.Tree(ctx, forgejoTestRepo, head, TreeOptions{Path: p})
				forgejoMissing(t, err, ErrPathNotFound)
			}
		})
	}
}

func TestForgejoTreeCalls(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{TreesPerPage: 4}, Limits{})
	ctx := context.Background()
	head := string(forgejoOID(e.fx.Head))
	all := forgejoEntries(t, e.repo, e.fx.Head, "", true)
	e.s.ResetCalls()
	if _, err := e.c.Tree(ctx, forgejoTestRepo, head, TreeOptions{Recursive: true}); err != nil {
		t.Fatal(err)
	}
	// git/commits, then pages until total_count; the truncated flag on
	// the last page is ignored.
	if want := 1 + (len(all)+3)/4; e.s.Calls("", "*") != want {
		t.Fatalf("%d calls, want %d: %v", e.s.Calls("", "*"), want, e.s.Requests())
	}

	// A small call budget cuts the listing short.
	c, err := NewWithLimits(Limits{MaxCalls: 3}, Source{ID: "fj", Session: e.s.Session})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Tree(ctx, forgejoTestRepo, head, TreeOptions{Recursive: true})
	if err != nil || !got.Truncated || len(got.Entries) != 8 {
		t.Fatalf("budget: %d entries, truncated %v, %v", len(got.Entries), got.Truncated, err)
	}
}

func TestForgejoStat(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{}, Limits{})
	ctx := context.Background()
	head := forgejoOID(e.fx.Head)
	for _, tc := range []struct {
		path string
		mode FileMode
		size int64
	}{
		{fakeforge.PathSymlink, ModeSymlink, int64(len(fakeforge.PathReadme))},
		{fakeforge.PathExec, ModeExec, -2},
		{fakeforge.PathSubmodule, ModeSubmodule, -1},
		{fakeforge.PathNested, ModeFile, -2},
		{fakeforge.PathUnicode, ModeFile, -2},
		{"docs", ModeTree, -1},
	} {
		got, err := e.c.Stat(ctx, forgejoTestRepo, string(head), tc.path)
		if err != nil {
			t.Fatal(err)
		}
		le, _ := e.repo.Lookup(e.fx.Head, tc.path)
		name := tc.path[strings.LastIndexByte(tc.path, '/')+1:]
		if got.Commit != head || got.Path != tc.path || got.Name != name || got.Mode != tc.mode ||
			got.Type != typeOf(tc.mode) || got.ID != forgejoOID(le.ID) || (tc.size != -2 && got.Size != tc.size) {
			t.Fatalf("%s: %+v", tc.path, got)
		}
	}
	got, _ := e.c.Stat(ctx, forgejoTestRepo, string(head), fakeforge.PathSubmodule)
	if got.ID != forgejoOID(fakeforge.SubmoduleCommit) || got.Type != ObjectCommit {
		t.Fatalf("submodule = %+v", got)
	}
	for _, p := range []string{"nope", "docs/nope", fakeforge.PathReadme + "/x", "nope/x"} {
		_, err := e.c.Stat(ctx, forgejoTestRepo, string(head), p)
		forgejoMissing(t, err, ErrPathNotFound)
	}
}

// forgejoRead reads and closes r.
func forgejoRead(t *testing.T, r *Reader, err error) []byte {
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

// forgejoBytes returns forgejoRead for t, to wrap a two-value call.
func forgejoBytes(t *testing.T) func(*Reader, error) []byte {
	return func(r *Reader, err error) []byte {
		t.Helper()
		return forgejoRead(t, r, err)
	}
}

func TestForgejoReadFile(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{}, Limits{})
	ctx := context.Background()
	head := forgejoOID(e.fx.Head)
	for _, p := range []string{
		fakeforge.PathReadme, fakeforge.PathBinary, fakeforge.PathLarge, fakeforge.PathUnicode,
		fakeforge.PathSymlink, fakeforge.PathLFS, fakeforge.PathExec,
	} {
		want, err := e.repo.ReadBlob(e.fx.Blobs[p])
		if err != nil {
			t.Fatal(err)
		}
		r, err := e.c.ReadFile(ctx, forgejoTestRepo, string(head), p)
		if err != nil {
			t.Fatal(err)
		}
		blob := r.Blob
		if got := forgejoRead(t, r, nil); !bytes.Equal(got, want) {
			t.Fatalf("%s: %d bytes, want %d", p, len(got), len(want))
		}
		wantBlob := Blob{ID: forgejoOID(e.fx.Blobs[p]), Commit: head, Path: p, Size: int64(len(want)), LFSPointer: p == fakeforge.PathLFS}
		if blob != wantBlob {
			t.Fatalf("%s: blob %+v, want %+v", p, blob, wantBlob)
		}
	}
	if got := forgejoBytes(t)(e.c.ReadFile(ctx, forgejoTestRepo, string(head), fakeforge.PathSymlink)); string(got) != fakeforge.PathReadme {
		t.Fatalf("symlink = %q", got)
	}

	for _, p := range []string{"docs", fakeforge.PathSubmodule} {
		if _, err := e.c.ReadFile(ctx, forgejoTestRepo, string(head), p); !errors.Is(err, ErrNotFile) {
			t.Fatalf("%s: err = %v, want ErrNotFile", p, err)
		}
	}
	for _, p := range []string{"nope", "docs/nope", fakeforge.PathReadme + "/x"} {
		_, err := e.c.ReadFile(ctx, forgejoTestRepo, string(head), p)
		forgejoMissing(t, err, ErrPathNotFound)
	}

	// Two calls: git/commits to check the ID, then raw.
	e.s.ResetCalls()
	forgejoBytes(t)(e.c.ReadFile(ctx, forgejoTestRepo, string(head), fakeforge.PathReadme))
	if n := e.s.Calls("", "*"); n != 2 {
		t.Fatalf("%d calls: %v", n, e.s.Requests())
	}

	t.Run("snapshot", func(t *testing.T) {
		// main moves to the root commit after it was resolved.
		e.s.SetHook(func(r *http.Request) {
			if strings.Contains(r.URL.Path, "/raw/") {
				e.repo.SetRef("refs/heads/main", e.fx.Main[0])
			}
		})
		defer func() { e.s.SetHook(nil); e.repo.SetRef("refs/heads/main", e.fx.Head) }()
		r, err := e.c.ReadFile(ctx, forgejoTestRepo, "main", fakeforge.PathFeature)
		if got := forgejoRead(t, r, err); string(got) != "feature v2\n" || r.Commit != head {
			t.Fatalf("got %q at %s", got, r.Commit)
		}
	})

	t.Run("too large", func(t *testing.T) {
		c, err := NewWithLimits(Limits{MaxBlobBytes: 1000}, Source{ID: "fj", Session: e.s.Session})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ReadFile(ctx, forgejoTestRepo, string(head), fakeforge.PathLarge); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("err = %v, want ErrTooLarge", err)
		}
	})

	t.Run("no etag", func(t *testing.T) {
		// Without an ETag the blob ID comes from the tree.
		got, err := forgejoReadNoETag(t, e, OID(head))
		if err != nil || got != forgejoOID(e.fx.Blobs[fakeforge.PathReadme]) {
			t.Fatalf("id = %s, %v", got, err)
		}
	})
}

// forgejoReadNoETag reads README.md through a Forgejo front mounted at
// /noetag that drops the ETag header.
func forgejoReadNoETag(t *testing.T, e *forgejoEnv, head OID) (OID, error) {
	t.Helper()
	e.s.Mount("/noetag", func(f *fakeforge.Forge) http.Handler {
		h := fakeforge.ForgejoAPI(f)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(noETagWriter{w}, r) })
	})
	s, err := forgejo.Provider{}.Open(context.Background(), forgeauth.Connection{
		Kind: forgeauth.Forgejo, BaseURL: e.s.HTTP.URL + "/noetag", Transport: e.s.HTTP.Client().Transport,
	}, forgeauth.StaticToken(fakeforge.Token))
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Source{ID: "fj", Session: s})
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.ReadFile(context.Background(), forgejoTestRepo, string(head), fakeforge.PathReadme)
	if err != nil {
		return "", err
	}
	defer r.Close()
	return r.ID, nil
}

// noETagWriter drops the ETag header.
type noETagWriter struct{ http.ResponseWriter }

func (w noETagWriter) WriteHeader(code int) {
	w.Header().Del("ETag")
	w.ResponseWriter.WriteHeader(code)
}

func (w noETagWriter) Write(p []byte) (int, error) {
	w.Header().Del("ETag")
	return w.ResponseWriter.Write(p)
}

func TestForgejoReadBlob(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{MaxBlobSize: 100 << 10}, Limits{})
	ctx := context.Background()
	for _, p := range []string{fakeforge.PathBinary, fakeforge.PathLFS, fakeforge.PathReadme} {
		id := e.fx.Blobs[p]
		want, _ := e.repo.ReadBlob(id)
		r, err := e.c.ReadBlob(ctx, forgejoTestRepo, forgejoOID(id))
		if err != nil {
			t.Fatal(err)
		}
		blob := r.Blob
		if got := forgejoRead(t, r, nil); !bytes.Equal(got, want) {
			t.Fatalf("%s: got %q", p, got)
		}
		if blob != (Blob{ID: forgejoOID(id), Size: int64(len(want)), LFSPointer: p == fakeforge.PathLFS}) {
			t.Fatalf("%s: blob %+v", p, blob)
		}
	}

	// Over the forge's blob API limit: content is "", so ErrTooLarge.
	large := forgejoOID(e.fx.Blobs[fakeforge.PathLarge])
	if _, err := e.c.ReadBlob(ctx, forgejoTestRepo, large); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	_, err := e.c.ReadBlob(ctx, forgejoTestRepo, OID(strings.Repeat("cd", 20)))
	forgejoMissing(t, err, ErrRevisionNotFound)

	t.Run("default limit", func(t *testing.T) {
		e := newForgejoEnv(t, fakeforge.ForgejoOptions{}, Limits{})
		want, _ := e.repo.ReadBlob(e.fx.Blobs[fakeforge.PathLarge])
		if got := forgejoBytes(t)(e.c.ReadBlob(ctx, forgejoTestRepo, large)); !bytes.Equal(got, want) {
			t.Fatalf("got %d bytes, want %d", len(got), len(want))
		}
		c, err := NewWithLimits(Limits{MaxBlobBytes: 1000}, Source{ID: "fj", Session: e.s.Session})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ReadBlob(ctx, forgejoTestRepo, large); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("err = %v, want ErrTooLarge", err)
		}
	})

	t.Run("raised forge limit", func(t *testing.T) {
		// The forge inlines a 25 MiB blob: a JSON body over 32 MiB.
		e := newForgejoEnv(t, fakeforge.ForgejoOptions{MaxBlobSize: 64 << 20}, Limits{})
		want := bytes.Repeat([]byte("forgejo\n"), 25<<20/8)
		id := forgejoOID(e.repo.WriteBlob(want))
		if got := forgejoBytes(t)(e.c.ReadBlob(ctx, forgejoTestRepo, id)); !bytes.Equal(got, want) {
			t.Fatalf("got %d bytes, want %d", len(got), len(want))
		}
		// Past MaxBlobBytes the body is cut short: ErrTooLarge, not a
		// decode error.
		c, err := NewWithLimits(Limits{MaxBlobBytes: 1 << 20}, Source{ID: "fj", Session: e.s.Session})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.ReadBlob(ctx, forgejoTestRepo, id); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("err = %v, want ErrTooLarge", err)
		}
	})
}

func TestForgejoMergeBase(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{}, Limits{})
	fx := e.fx
	ctx := context.Background()
	for _, tc := range []struct {
		a, b string
		want gitobj.ID
	}{
		{"main", "feature", fx.MergeBase},
		{"feature", "main", fx.MergeBase},
		{string(forgejoOID(fx.Main[1])), "main", fx.Main[1]}, // a is an ancestor of b
		{"main", string(forgejoOID(fx.Main[1])), fx.Main[1]}, // b is an ancestor of a
		{"main", "main", fx.Head},                            // same commit
		{"refs/tags/v1.0", "feature", fx.Main[2]},            // fork point
		{string(forgejoOID(fx.Feature[0])), "main~1", fx.Feature[0]},
	} {
		got, err := e.c.MergeBase(ctx, forgejoTestRepo, tc.a, tc.b)
		if err != nil || got != forgejoOID(tc.want) {
			t.Fatalf("%s %s: got %s, %v, want %s", tc.a, tc.b, got, err, tc.want)
		}
	}

	// Unrelated history.
	orphan := e.repo.CommitTree(e.repo.BuildTree(map[string]fakeforge.File{"o": {Data: []byte("o\n")}}), fakeforge.Epoch, "orphan\n")
	e.repo.SetRef("refs/heads/orphan", orphan)
	if _, err := e.c.MergeBase(ctx, forgejoTestRepo, "main", "orphan"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("orphan: err = %v, want ErrInvalid", err)
	}

	// Merges from the base: feature merges main twice.
	r := e.s.NewRepo("o/merges")
	tree := r.BuildTree(map[string]fakeforge.File{"a": {Data: []byte("a\n")}})
	at := func(h int) time.Time { return fakeforge.Epoch.Add(time.Duration(h) * time.Hour) }
	m0 := r.CommitTree(tree, at(0), "m0\n")
	m1 := r.CommitTree(tree, at(1), "m1\n", m0)
	f0 := r.CommitTree(tree, at(2), "f0\n", m0)
	f1 := r.CommitTree(tree, at(3), "f1\n", f0, m1)
	m2 := r.CommitTree(tree, at(4), "m2\n", m1)
	f2 := r.CommitTree(tree, at(5), "f2\n", f1, m2)
	f3 := r.CommitTree(tree, at(6), "f3\n", f2)
	m3 := r.CommitTree(tree, at(7), "m3\n", m2)
	r.SetRef("refs/heads/main", m3)
	r.SetRef("refs/heads/feature", f3)
	rr := RepositoryRef{Source: "fj", Repository: "o/merges"}
	got, err := e.c.MergeBase(ctx, rr, "main", "feature")
	if err != nil || got != forgejoOID(m2) {
		t.Fatalf("merges from base: %s, %v, want %s", got, err, m2)
	}

	// Criss-cross: two best bases.
	x1 := r.CommitTree(tree, at(10), "x1\n", m0)
	y1 := r.CommitTree(tree, at(11), "y1\n", m0)
	x2 := r.CommitTree(tree, at(12), "x2\n", x1, y1)
	y2 := r.CommitTree(tree, at(13), "y2\n", y1, x1)
	r.SetRef("refs/heads/x", x2)
	r.SetRef("refs/heads/y", y2)
	if bases, _ := r.MergeBases(x2, y2); len(bases) != 2 {
		t.Fatalf("fixture: %d bases", len(bases))
	}
	if _, err := e.c.MergeBase(ctx, rr, "x", "y"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("criss-cross: err = %v, want ErrUnsupported", err)
	}
}

func TestForgejoMergeBaseBudget(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{}, Limits{MaxCalls: 4})
	repo, tip := forgejoLongRepo(e.s, 400)
	log, _ := repo.Log(tip, fakeforge.LogOptions{})
	r := RepositoryRef{Source: "fj", Repository: "o/long"}
	// Resolving takes two calls; listing 399 commits takes 8 pages.
	_, err := e.c.MergeBase(context.Background(), r, string(forgejoOID(log[len(log)-1])), string(forgejoOID(tip)))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestForgejoErrors(t *testing.T) {
	e := newForgejoEnv(t, fakeforge.ForgejoOptions{}, Limits{})
	ctx := context.Background()
	head := string(forgejoOID(e.fx.Head))

	bad, err := e.s.SessionWithToken("wrong")
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Source{ID: "fj", Session: bad})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve(ctx, forgejoTestRepo, "HEAD"); !errors.Is(err, forgehttp.ErrUnauthorized) {
		t.Fatalf("401: err = %v", err)
	}

	for _, tc := range []struct {
		fault fakeforge.Fault
		want  error
	}{
		{fakeforge.Fault{Path: "*", Status: 403}, forgehttp.ErrForbidden},
		{fakeforge.Fault{Path: "*", Status: 429}, forgehttp.ErrRateLimited},
		{fakeforge.Fault{Path: "*", Status: 403, Header: http.Header{"X-Ratelimit-Remaining": {"0"}}}, forgehttp.ErrRateLimited},
	} {
		e.s.Inject(tc.fault)
		_, err := e.c.Commit(ctx, forgejoTestRepo, "main")
		if !errors.Is(err, tc.want) {
			t.Fatalf("%d: err = %v, want %v", tc.fault.Status, err, tc.want)
		}
		if _, err := e.c.ReadFile(ctx, forgejoTestRepo, head, fakeforge.PathReadme); !errors.Is(err, tc.want) {
			t.Fatalf("%d: read: err = %v, want %v", tc.fault.Status, err, tc.want)
		}
		e.s.ClearFaults()
	}

	// A private repository without access is 404, never unauthorized.
	e.repo.SetPrivate(true)
	e.s.SetAccess("o/r", fakeforge.AccessNone)
	for _, call := range []func() error{
		func() error { _, err := e.c.Resolve(ctx, forgejoTestRepo, "HEAD"); return err },
		func() error { _, err := e.c.Commit(ctx, forgejoTestRepo, head); return err },
		func() error { _, err := e.c.ListRefs(ctx, forgejoTestRepo, RefsOptions{}); return err },
		func() error { _, err := e.c.ReadBlob(ctx, forgejoTestRepo, OID(head)); return err },
	} {
		if err := call(); !errors.Is(err, forgehttp.ErrNotFound) || errors.Is(err, forgehttp.ErrUnauthorized) {
			t.Fatalf("private: err = %v", err)
		}
	}

	// Nested GitLab-style paths do not exist on Forgejo.
	if _, err := e.c.Resolve(ctx, RepositoryRef{Source: "fj", Repository: "g/s/p"}, "HEAD"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nested: err = %v", err)
	}
}
