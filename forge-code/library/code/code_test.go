package code

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// id returns a test object ID.
func id(n int) OID { return OID(fmt.Sprintf("%040x", n)) }

// Test commits: c1 <- c2 <- c3 <- m, s1 <- m, s1's parent is c2.
var (
	c1, c2, c3, s1, m = id(1), id(2), id(3), id(4), id(5)
	tagObj            = id(9)
	blobA, blobLFS    = id(20), id(21)
)

const lfsPointer = "version https://git-lfs.github.com/spec/v1\n" +
	"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n" +
	"size 12345\n"

// fake is an in-memory backend. Every method spends one call.
type fake struct {
	mu      sync.Mutex
	def     string
	commits map[OID]Commit
	refMap  map[string]Ref
	files   map[OID][]Entry // per commit, every entry with its full path
	blobs   map[OID][]byte
	body    func(OID) (io.ReadCloser, int64) // overrides blobs if set
	calls   atomic.Int64
	onCall  func(method string) // runs before each call
}

func newFake() *fake {
	commit := func(id OID, parents ...OID) Commit {
		return Commit{ID: id, Parents: parents, Message: "commit " + string(id[38:])}
	}
	f := &fake{
		def: "main",
		commits: map[OID]Commit{
			c1: commit(c1), c2: commit(c2, c1), c3: commit(c3, c2),
			s1: commit(s1, c2), m: commit(m, c3, s1),
		},
		refMap: map[string]Ref{
			"refs/heads/main": {Name: "refs/heads/main", Target: m, Commit: m},
			"refs/heads/dev":  {Name: "refs/heads/dev", Target: c2, Commit: c2},
			"refs/tags/v1":    {Name: "refs/tags/v1", Target: tagObj, Commit: c1},
			"refs/heads/x":    {Name: "refs/heads/x", Target: c1, Commit: c1},
			"refs/tags/x":     {Name: "refs/tags/x", Target: c3, Commit: c3},
		},
		blobs: map[OID][]byte{blobA: []byte("hello\n"), blobLFS: []byte(lfsPointer)},
	}
	f.files = map[OID][]Entry{m: {
		{Name: "b.txt", Path: "b.txt", Mode: ModeFile, Type: ObjectBlob, ID: blobA, Size: 6},
		{Name: "a.txt", Path: "a.txt", Mode: ModeExec, Type: ObjectBlob, ID: blobA, Size: 6},
		{Name: "big.bin", Path: "big.bin", Mode: ModeFile, Type: ObjectBlob, ID: blobLFS, Size: int64(len(lfsPointer))},
		{Name: "dir", Path: "dir", Mode: ModeTree, Type: ObjectTree, ID: id(30), Size: -1},
		{Name: "c.txt", Path: "dir/c.txt", Mode: ModeFile, Type: ObjectBlob, ID: blobA, Size: 6},
		{Name: "sub", Path: "sub", Mode: ModeSubmodule, Type: ObjectCommit, ID: id(40), Size: -1},
	}}
	return f
}

func (f *fake) call(o *op, method string) error {
	f.calls.Add(1)
	if f.onCall != nil {
		f.onCall(method)
	}
	return o.spend()
}

func (f *fake) capabilities() Capabilities { return Capabilities{TreeIDs: true, MergeBase: true} }

func (f *fake) defaultBranch(o *op) (string, error) {
	if err := f.call(o, "defaultBranch"); err != nil {
		return "", err
	}
	return f.def, nil
}

func (f *fake) commit(o *op, id OID) (Commit, error) {
	if err := f.call(o, "commit"); err != nil {
		return Commit{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cm, ok := f.commits[id]
	if !ok {
		return Commit{}, missing(&forgehttp.Error{StatusCode: 422}, objectMissing(id))
	}
	return cm, nil
}

func (f *fake) ref(o *op, name string) (Ref, bool, error) {
	if err := f.call(o, "ref"); err != nil {
		return Ref{}, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.refMap[name]
	return r, ok, nil
}

func (f *fake) refs(o *op, prefix string, limit int) ([]Ref, bool, error) {
	if err := f.call(o, "refs"); err != nil {
		return nil, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Ref
	for name, r := range f.refMap {
		if strings.HasPrefix(name, prefix) {
			if len(out) == limit {
				return out, true, nil
			}
			out = append(out, r)
		}
	}
	return out, false, nil
}

// history walks first parents; pos.Offset is the number of commits done.
func (f *fake) history(o *op, tip OID, path string, n int, pos historyPos) ([]Commit, historyPos, bool, error) {
	if err := f.call(o, "history"); err != nil {
		return nil, pos, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []Commit
	for at := tip; at != ""; {
		cm := f.commits[at]
		all = append(all, cm)
		at = ""
		if len(cm.Parents) > 0 {
			at = cm.Parents[0]
		}
	}
	all = all[min(pos.Offset, len(all)):]
	page := all[:min(n, len(all))]
	return page, historyPos{Offset: pos.Offset + len(page)}, len(all) > n, nil
}

func (f *fake) tree(o *op, commit OID, path string, recursive bool, max int) (Tree, error) {
	if err := f.call(o, "tree"); err != nil {
		return Tree{}, err
	}
	t := Tree{ID: id(99)}
	for _, e := range f.files[commit] {
		dir := e.Path[:max0(strings.LastIndexByte(e.Path, '/'))]
		if dir == path || recursive && (path == "" || strings.HasPrefix(e.Path, path+"/")) {
			t.Entries = append(t.Entries, e)
		}
	}
	return t, nil
}

func max0(i int) int { return max(i, 0) }

func (f *fake) stat(o *op, commit OID, path string) (Entry, error) {
	if err := f.call(o, "stat"); err != nil {
		return Entry{}, err
	}
	for _, e := range f.files[commit] {
		if e.Path == path {
			return e, nil
		}
	}
	return Entry{}, pathMissing(path)
}

func (f *fake) readFile(o *op, commit OID, path string) (*Reader, error) {
	e, err := f.stat(o, commit, path)
	if err != nil {
		return nil, err
	}
	if e.Type != ObjectBlob {
		return nil, fmt.Errorf("%w: %s", ErrNotFile, path)
	}
	return f.readBlob(o, e.ID)
}

func (f *fake) readBlob(o *op, id OID) (*Reader, error) {
	if err := f.call(o, "readBlob"); err != nil {
		return nil, err
	}
	if f.body != nil {
		rc, size := f.body(id)
		return &Reader{Blob: Blob{ID: id, Size: size}, ReadCloser: rc}, nil
	}
	data, ok := f.blobs[id]
	if !ok {
		return nil, objectMissing(id)
	}
	return &Reader{Blob: Blob{ID: id, Size: int64(len(data))}, ReadCloser: io.NopCloser(bytes.NewReader(data))}, nil
}

func (f *fake) mergeBase(o *op, a, b OID) (OID, error) {
	if err := f.call(o, "mergeBase"); err != nil {
		return "", err
	}
	return c2, nil
}

var repo = RepositoryRef{Source: "src", Repository: "mrbean/teddy"}

// newTest returns a Client over one fake source.
func newTest(t *testing.T, f *fake, l Limits) *Client {
	t.Helper()
	s := &forgeauth.Session{Kind: "fake", BaseURL: "http://forge.invalid", Client: http.DefaultClient}
	c, err := newClient(l, func(forgeauth.Kind) (backend, bool) { return f, true }, []Source{{ID: "src", Session: s}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestParseRevision(t *testing.T) {
	full := strings.Repeat("ab", 20)
	cases := []struct {
		in   string
		want revision
	}{
		{"", revision{head: true}},
		{"HEAD", revision{head: true}},
		{full, revision{oid: OID(full)}},
		{strings.ToUpper(full), revision{oid: OID(full)}},
		{strings.Repeat("c", 64), revision{oid: OID(strings.Repeat("c", 64))}},
		{"refs/heads/main", revision{ref: "refs/heads/main"}},
		{"refs/tags/v1.0", revision{ref: "refs/tags/v1.0"}},
		{"main", revision{bare: "main"}},
		{"feature/x-1", revision{bare: "feature/x-1"}},
		{"abc", revision{bare: "abc"}}, // too short to be an object ID
		{"main~", revision{bare: "main", steps: []step{{n: 1}}}},
		{"main^", revision{bare: "main", steps: []step{{parent: true, n: 1}}}},
		{"HEAD~2^2", revision{head: true, steps: []step{{n: 2}, {parent: true, n: 2}}}},
		{"main~100^0", revision{bare: "main", steps: []step{{n: 100}, {parent: true}}}},
	}
	for _, tc := range cases {
		got, err := parseRevision(tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		got.text, tc.want.text = "", ""
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%q = %+v, want %+v", tc.in, got, tc.want)
		}
	}
	for _, in := range []string{
		"abcd", "abc1234", strings.Repeat("a", 39), strings.Repeat("a", 41), // short or odd IDs
		"main..dev", "main...dev", "main@{1}", "@{-1}", "@", "HEAD:README", "main:x",
		"refs/remotes/origin/main", "refs/pull/1/head", "refs/heads/", "refs/heads",
		"main~101", "main^101", "main~0100", "main~x", "~1", "-main", "a b", "a\x00b",
		"a\tb", "dir/", "/main", "a//b", ".hidden", "a.lock", "a/.b", "x%2Fy", "a*", "a?",
		"a[b", `a\b`, "main.", strings.Repeat("a/", 700) + "b", "\xff",
	} {
		if _, err := parseRevision(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: err = %v, want ErrInvalid", in, err)
		}
	}
}

func TestCheckRefName(t *testing.T) {
	for _, ok := range []string{"refs/heads/main", "refs/tags/v1.2.3", "refs/heads/feat/ü", "refs/heads/a-b_c", "refs/heads/a@b"} {
		if err := checkRefName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"main", "refs/heads/", "refs/notes/x", "refs/heads/-x", "refs/heads/a..b", "refs/heads/a/",
		"refs/heads/a//b", "refs/heads/.a", "refs/heads/a.lock", "refs/heads/a.", "refs/heads/a b",
		"refs/heads/a~1", "refs/heads/a^", "refs/heads/a:b", "refs/heads/a?", "refs/heads/a*",
		"refs/heads/a[", `refs/heads/a\b`, "refs/heads/a%2f", "refs/heads/@", "refs/heads/a@{1}",
		"refs/heads/a\x7f", "refs/heads/\xff", "refs/heads/" + strings.Repeat("a", 1100),
	} {
		if err := checkRefName(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: err = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestFilePaths(t *testing.T) {
	for _, ok := range []string{"a", "a/b.txt", "dir/sub/ü.md", "-leading-dash", "with space", "100%", ".github/x", "a..b"} {
		if _, err := cleanFilePath(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
		if err := checkWritePath(ok); err != nil {
			t.Errorf("write %q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/a", "a/", "a//b", "./a", "a/../b", "..", "a\x00b", "a\nb", "\x7f", "\xff", strings.Repeat("a", 4097)} {
		if _, err := cleanFilePath(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: err = %v, want ErrInvalid", bad, err)
		}
	}
	for _, bad := range []string{".git", "a/.git/config", ".GIT/x", "x/.Git", ".git.", ".git ", "a/.git"} {
		if err := checkWritePath(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("write %q: err = %v, want ErrInvalid", bad, err)
		}
	}
	if got := escapePath("a b/c%d/é"); got != "a%20b/c%25d/%C3%A9" {
		t.Errorf("escapePath = %q", got)
	}
	if p, ok := reposPath("o/n"); !ok || p != "/repos/o/n" {
		t.Errorf("reposPath = %q, %v", p, ok)
	}
	if _, ok := reposPath("g/s/p"); ok {
		t.Error("reposPath accepted three segments")
	}
}

func TestResolve(t *testing.T) {
	ctx := context.Background()
	c := newTest(t, newFake(), Limits{})
	cases := []struct {
		rev  string
		want Resolved
	}{
		{"HEAD", Resolved{m, "refs/heads/main"}},
		{"", Resolved{m, "refs/heads/main"}},
		{"main", Resolved{m, "refs/heads/main"}},
		{"dev", Resolved{c2, "refs/heads/dev"}},
		{"v1", Resolved{c1, "refs/tags/v1"}}, // annotated, peeled
		{"refs/heads/x", Resolved{c1, "refs/heads/x"}},
		{"refs/tags/x", Resolved{c3, "refs/tags/x"}},
		{string(c3), Resolved{c3, ""}},
		{"main~1", Resolved{c3, "refs/heads/main"}},
		{"main~2", Resolved{c2, "refs/heads/main"}},
		{"main^2", Resolved{s1, "refs/heads/main"}},
		{"main^2~1", Resolved{c2, "refs/heads/main"}},
		{"main^0", Resolved{m, "refs/heads/main"}},
		{string(m) + "~3", Resolved{c1, ""}},
		{"HEAD^^", Resolved{c2, "refs/heads/main"}},
	}
	for _, tc := range cases {
		got, err := c.Resolve(ctx, repo, tc.rev)
		if err != nil || got != tc.want {
			t.Errorf("%q = %+v, %v; want %+v", tc.rev, got, err, tc.want)
		}
	}
	if _, err := c.Resolve(ctx, repo, "x"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("tag and branch x: err = %v, want ErrAmbiguous", err)
	}
	for _, rev := range []string{"nope", "refs/heads/nope", "main~4", "main^3", "dev^2", string(id(77)), string(id(77)) + "^0"} {
		_, err := c.Resolve(ctx, repo, rev)
		if !errors.Is(err, ErrRevisionNotFound) || !errors.Is(err, forgehttp.ErrNotFound) {
			t.Errorf("%q: err = %v, want ErrRevisionNotFound and forgehttp.ErrNotFound", rev, err)
		}
		var fe *forgehttp.Error
		if errors.As(err, &fe) {
			t.Errorf("%q: err wraps the forge error %v", rev, fe)
		}
	}
	if _, err := c.Resolve(ctx, repo, "abc1234"); !errors.Is(err, ErrInvalid) {
		t.Errorf("short SHA: err = %v, want ErrInvalid", err)
	}
}

func TestResolveEmptyRepository(t *testing.T) {
	f := newFake()
	f.def = ""
	_, err := newTest(t, f, Limits{}).Resolve(context.Background(), repo, "HEAD")
	if !errors.Is(err, ErrRevisionNotFound) {
		t.Errorf("err = %v, want ErrRevisionNotFound", err)
	}
}

func TestArguments(t *testing.T) {
	ctx := context.Background()
	c := newTest(t, newFake(), Limits{})
	if _, err := c.Resolve(ctx, RepositoryRef{Source: "src", Repository: "noslash"}, "main"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad repository: %v", err)
	}
	if _, err := c.Resolve(ctx, RepositoryRef{Source: "other", Repository: "a/b"}, "main"); !errors.Is(err, ErrUnknownSource) {
		t.Errorf("unknown source: %v", err)
	}
	if _, err := c.Resolve(ctx, RepositoryRef{Repository: "a/b"}, "main"); !errors.Is(err, ErrInvalid) {
		t.Errorf("no source: %v", err)
	}
	if _, err := c.ReadBlob(ctx, repo, "ABC"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad blob ID: %v", err)
	}
	if _, err := c.Stat(ctx, repo, "main", "../etc/passwd"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad path: %v", err)
	}
	if _, err := c.Tree(ctx, repo, "main", TreeOptions{MaxEntries: -1}); !errors.Is(err, ErrInvalid) {
		t.Errorf("negative max entries: %v", err)
	}
	if _, err := c.ListRefs(ctx, repo, RefsOptions{Prefix: "refs/notes/"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad prefix: %v", err)
	}
	if _, err := c.History(ctx, repo, HistoryOptions{PageSize: 101}); !errors.Is(err, ErrInvalid) {
		t.Errorf("page size: %v", err)
	}
}

func TestNew(t *testing.T) {
	s := &forgeauth.Session{Kind: forgeauth.GitHub, BaseURL: "http://x", Client: http.DefaultClient}
	if _, err := New(Source{ID: "a", Session: s}, Source{ID: "b", Session: s, Git: &forgeauth.GitRemote{}}); err != nil {
		t.Fatal(err)
	}
	bad := [][]Source{
		{{ID: " ", Session: s}},
		{{ID: "a", Session: s}, {ID: "a", Session: s}},
		{{ID: "a"}},
		{{ID: "a", Session: &forgeauth.Session{Kind: "gitea", Client: http.DefaultClient}}},
	}
	for i, srcs := range bad {
		if _, err := New(srcs...); !errors.Is(err, ErrInvalid) {
			t.Errorf("%d: err = %v, want ErrInvalid", i, err)
		}
	}
	if _, err := NewWithLimits(Limits{MaxCalls: -1}); !errors.Is(err, ErrInvalid) {
		t.Errorf("negative limit: %v", err)
	}
	c, _ := New(Source{ID: "a", Session: s}, Source{ID: "b", Session: s, Git: &forgeauth.GitRemote{}})
	if got := len(c.Sources()); got != 2 {
		t.Errorf("Sources = %d", got)
	}
	if caps, _ := c.Capabilities("a"); caps.Writes {
		t.Error("Writes without Git remote")
	}
	if caps, _ := c.Capabilities("b"); !caps.Writes {
		t.Error("no Writes with Git remote")
	}
	if _, err := c.Capabilities("z"); !errors.Is(err, ErrUnknownSource) {
		t.Errorf("unknown source: %v", err)
	}
	if c.limits != (Limits{defaultMaxBlobBytes, defaultMaxCalls, defaultMaxWriteBytes}) {
		t.Errorf("limits = %+v", c.limits)
	}
}

func TestCallBudget(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	// Walking ~3 needs a ref call and three commit calls.
	c := newTest(t, f, Limits{MaxCalls: 3})
	if _, err := c.Resolve(ctx, repo, "refs/heads/main~3"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
	c = newTest(t, f, Limits{MaxCalls: 4})
	if got, err := c.Resolve(ctx, repo, "refs/heads/main~3"); err != nil || got.Commit != c1 {
		t.Errorf("= %v, %v", got, err)
	}
	// Each call has its own budget.
	if _, err := c.Resolve(ctx, repo, "refs/heads/main~3"); err != nil {
		t.Errorf("second call: %v", err)
	}
}

func TestCountingSession(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("{}"))
	}))
	defer srv.Close()
	s := &forgeauth.Session{Kind: forgeauth.GitHub, BaseURL: srv.URL, Client: srv.Client()}
	o := newOp(context.Background(), s, "o/n", Limits{MaxCalls: 2})
	for i := range 3 {
		err := forgerest.Send(o.ctx, o.s, "GET", "/x", nil, nil)
		if i < 2 && err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if i == 2 && (!errors.Is(err, ErrTooLarge) || !exhausted(err)) {
			t.Fatalf("call 3: err = %v, want the budget error", err)
		}
	}
	if hits.Load() != 2 || o.left() != 0 {
		t.Errorf("hits = %d, left = %d", hits.Load(), o.left())
	}
	if s.Client.Transport != srv.Client().Transport {
		t.Error("the source's client was changed")
	}
}

func TestCached(t *testing.T) {
	o := newOp(context.Background(), &forgeauth.Session{Client: http.DefaultClient}, "o/n", Limits{MaxCalls: 1})
	loads := 0
	load := func() ([]Entry, error) { loads++; return []Entry{{Name: "a"}}, nil }
	for range 3 {
		if v, err := cached(o, "tree:x", load); err != nil || len(v) != 1 {
			t.Fatal(v, err)
		}
	}
	if _, err := cached(o, "fail", func() (int, error) { return 0, errors.New("x") }); err == nil {
		t.Error("error not returned")
	}
	if loads != 1 {
		t.Errorf("loads = %d", loads)
	}
}

func TestCommit(t *testing.T) {
	f := newFake()
	c := newTest(t, f, Limits{})
	cm, err := c.Commit(context.Background(), repo, string(c3))
	if err != nil || cm.ID != c3 || !slices.Equal(cm.Parents, []OID{c2}) {
		t.Fatalf("= %+v, %v", cm, err)
	}
	if n := f.calls.Load(); n != 1 {
		t.Errorf("calls = %d, want 1 (the resolve read is reused)", n)
	}
	cm, err = c.Commit(context.Background(), repo, "main~1")
	if err != nil || cm.ID != c3 {
		t.Fatalf("= %+v, %v", cm, err)
	}
}

func TestListRefs(t *testing.T) {
	ctx := context.Background()
	c := newTest(t, newFake(), Limits{})
	list, err := c.ListRefs(ctx, repo, RefsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range list.Refs {
		names = append(names, r.Name)
	}
	want := []string{"refs/heads/dev", "refs/heads/main", "refs/heads/x", "refs/tags/v1", "refs/tags/x"}
	if !slices.Equal(names, want) || list.Default != "refs/heads/main" || list.Truncated {
		t.Errorf("= %v %q %v", names, list.Default, list.Truncated)
	}
	for _, tc := range []struct {
		opts      RefsOptions
		n         int
		truncated bool
	}{
		{RefsOptions{Limit: 3}, 3, true},
		{RefsOptions{Limit: 4}, 4, true},
		{RefsOptions{Limit: 5}, 5, false},
		{RefsOptions{Prefix: "refs/tags/"}, 2, false},
		{RefsOptions{Prefix: "refs/heads/", Limit: 3}, 3, false},
		{RefsOptions{Prefix: "refs/heads/", Limit: 2}, 2, true},
	} {
		list, err := c.ListRefs(ctx, repo, tc.opts)
		if err != nil || len(list.Refs) != tc.n || list.Truncated != tc.truncated {
			t.Errorf("%+v: %d refs, truncated %v, %v", tc.opts, len(list.Refs), list.Truncated, err)
		}
	}
}

func TestHistory(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	c := newTest(t, f, Limits{})
	page, err := c.History(ctx, repo, HistoryOptions{Revision: "main", PageSize: 2})
	if err != nil || page.Tip != m || len(page.Commits) != 2 || page.Next == "" {
		t.Fatalf("page 1 = %+v, %v", page, err)
	}
	// Moving the branch does not move the listing.
	f.mu.Lock()
	f.refMap["refs/heads/main"] = Ref{Name: "refs/heads/main", Target: c1, Commit: c1}
	f.mu.Unlock()
	page, err = c.History(ctx, repo, HistoryOptions{Revision: "main", Cursor: page.Next})
	if err != nil || page.Tip != m || len(page.Commits) != 2 || page.Commits[0].ID != c2 || page.Next != "" {
		t.Fatalf("page 2 = %+v, %v", page, err)
	}
	empty, err := c.History(ctx, repo, HistoryOptions{Revision: string(c1)})
	if err != nil || len(empty.Commits) != 1 || empty.Next != "" {
		t.Errorf("single = %+v, %v", empty, err)
	}
}

func TestHistoryCursor(t *testing.T) {
	ctx := context.Background()
	c := newTest(t, newFake(), Limits{})
	page, err := c.History(ctx, repo, HistoryOptions{Path: "dir", PageSize: 1})
	if err != nil || page.Next == "" {
		t.Fatal(page, err)
	}
	cur, err := decodeCursor(page.Next)
	if err != nil || cur.Tip != m || cur.Path != "dir" || cur.N != 1 {
		t.Fatalf("cursor = %+v, %v", cur, err)
	}
	if _, err := c.History(ctx, repo, HistoryOptions{Cursor: page.Next, Path: "dir", PageSize: 1}); err != nil {
		t.Errorf("same options: %v", err)
	}
	if _, err := c.History(ctx, repo, HistoryOptions{Cursor: page.Next, PageSize: 2}); !errors.Is(err, ErrInvalid) {
		t.Errorf("other page size: %v", err)
	}
	if _, err := c.History(ctx, repo, HistoryOptions{Cursor: page.Next, Path: "other"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("other path: %v", err)
	}
	tamper := func(c historyCursor) string { return c.encode() }
	bad := []string{
		"!!", page.Next + "A", page.Next[:len(page.Next)-2], strings.Repeat("A", maxCursorBytes+1),
		b64(`{"v":1,"tip":"` + string(m) + `","path":"","n":1,"page":0,"off":1,"x":1}`),
		b64(` {"v":1,"tip":"` + string(m) + `","path":"","n":1,"page":0,"off":1}`),
		tamper(historyCursor{V: 2, Tip: m, N: 1}),
		tamper(historyCursor{V: 1, Tip: "abc", N: 1}),
		tamper(historyCursor{V: 1, Tip: m, N: 0}),
		tamper(historyCursor{V: 1, Tip: m, N: 101}),
		tamper(historyCursor{V: 1, Tip: m, N: 1, Page: -1}),
		tamper(historyCursor{V: 1, Tip: m, N: 1, Offset: maxCursorOffset + 1}),
		tamper(historyCursor{V: 1, Tip: m, N: 1, Path: "../x"}),
		tamper(historyCursor{V: 1, Tip: m, N: 1, Path: "/x"}),
	}
	for _, s := range bad {
		if _, err := c.History(ctx, repo, HistoryOptions{Cursor: s}); !errors.Is(err, ErrInvalid) {
			t.Errorf("cursor %q: err = %v, want ErrInvalid", s, err)
		}
	}
}

// b64 encodes s like a cursor.
func b64(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

func TestTree(t *testing.T) {
	ctx := context.Background()
	c := newTest(t, newFake(), Limits{})
	tr, err := c.Tree(ctx, repo, "main", TreeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range tr.Entries {
		paths = append(paths, e.Path)
	}
	if tr.Commit != m || tr.Path != "" || tr.ID != id(99) || tr.Truncated ||
		!slices.Equal(paths, []string{"a.txt", "b.txt", "big.bin", "dir", "sub"}) {
		t.Errorf("= %+v", tr)
	}
	tr, err = c.Tree(ctx, repo, "main", TreeOptions{Recursive: true, MaxEntries: 4})
	if err != nil || len(tr.Entries) != 4 || !tr.Truncated || tr.Entries[3].Path != "dir" {
		t.Errorf("recursive = %+v, %v", tr, err)
	}
	tr, err = c.Tree(ctx, repo, "main", TreeOptions{Path: "dir"})
	if err != nil || tr.Path != "dir" || len(tr.Entries) != 1 {
		t.Errorf("dir = %+v, %v", tr, err)
	}
}

func TestStat(t *testing.T) {
	ctx := context.Background()
	c := newTest(t, newFake(), Limits{})
	pi, err := c.Stat(ctx, repo, "main", "a.txt")
	if err != nil || pi.Commit != m || pi.Mode != ModeExec || pi.Mode.String() != "100755" {
		t.Errorf("= %+v, %v", pi, err)
	}
	_, err = c.Stat(ctx, repo, "main", "nope")
	if !errors.Is(err, ErrPathNotFound) || !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}

func TestReadFile(t *testing.T) {
	ctx := context.Background()
	c := newTest(t, newFake(), Limits{})
	r, err := c.ReadFile(ctx, repo, "main", "dir/c.txt")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil || string(data) != "hello\n" || r.Commit != m || r.Path != "dir/c.txt" || r.ID != blobA || r.LFSPointer {
		t.Errorf("= %+v %q %v", r.Blob, data, err)
	}
	r, err = c.ReadFile(ctx, repo, "main", "big.bin")
	if err != nil || !r.LFSPointer {
		t.Errorf("LFS pointer = %+v, %v", r, err)
	}
	if data, _ := io.ReadAll(r); string(data) != lfsPointer {
		t.Errorf("pointer content = %q", data)
	}
	for _, p := range []string{"dir", "sub"} {
		if _, err := c.ReadFile(ctx, repo, "main", p); !errors.Is(err, ErrNotFile) {
			t.Errorf("%s: err = %v, want ErrNotFile", p, err)
		}
	}
	r, err = c.ReadBlob(ctx, repo, blobA)
	if err != nil || r.Commit != "" || r.Path != "" || r.ID != blobA {
		t.Errorf("ReadBlob = %+v, %v", r, err)
	}
	if _, err := c.ReadBlob(ctx, repo, id(66)); !errors.Is(err, ErrRevisionNotFound) {
		t.Errorf("missing blob: %v", err)
	}
}

// countingBody is an endless body that counts the bytes read from it.
type countingBody struct {
	n      atomic.Int64
	closed bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	b.n.Add(int64(len(p)))
	return len(p), nil
}

func (b *countingBody) Close() error { b.closed = true; return nil }

func TestReaderLimit(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	body := &countingBody{}
	size := int64(-1)
	f.body = func(OID) (io.ReadCloser, int64) { return body, size }
	c := newTest(t, f, Limits{MaxBlobBytes: 64 << 10})

	r, err := c.ReadBlob(ctx, repo, blobA)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, r)
	if !errors.Is(err, ErrTooLarge) || n != 64<<10 {
		t.Errorf("read %d, err = %v; want 64 KiB and ErrTooLarge", n, err)
	}
	if got := body.n.Load(); got > 64<<10+64<<10 {
		t.Errorf("read %d bytes from the forge, want about the limit", got)
	}
	r.Close()
	if !body.closed {
		t.Error("body not closed")
	}

	// A known size over the limit fails before reading.
	body, size = &countingBody{}, 1<<20
	if _, err := c.ReadBlob(ctx, repo, blobA); !errors.Is(err, ErrTooLarge) || !body.closed || body.n.Load() != 0 {
		t.Errorf("err = %v, closed %v, read %d", err, body.closed, body.n.Load())
	}
}

func TestReaderExactLimit(t *testing.T) {
	data := bytes.Repeat([]byte("y"), 2000)
	r, err := finishReader(&Reader{Blob: Blob{Size: -1}, ReadCloser: io.NopCloser(bytes.NewReader(data))}, 2000)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, data) || r.Size != -1 {
		t.Errorf("read %d, %v, size %d", len(got), err, r.Size)
	}
	r, _ = finishReader(&Reader{Blob: Blob{Size: -1}, ReadCloser: io.NopCloser(strings.NewReader("short"))}, 2000)
	if r.Size != 5 {
		t.Errorf("short size = %d, want 5", r.Size)
	}
}

func TestLFSPointer(t *testing.T) {
	if !isLFSPointer([]byte(lfsPointer)) {
		t.Error("pointer not detected")
	}
	ext := "version https://git-lfs.github.com/spec/v1\next-0-foo sha256:" + strings.Repeat("a", 64) +
		"\noid sha256:" + strings.Repeat("b", 64) + "\nsize 0\n"
	if !isLFSPointer([]byte(ext)) {
		t.Error("pointer with extension not detected")
	}
	for _, s := range []string{
		"", "hello\n",
		strings.TrimSuffix(lfsPointer, "\n"),
		strings.Replace(lfsPointer, "v1", "v2", 1),
		strings.Replace(lfsPointer, "sha256:4d", "sha256:", 1),
		strings.Replace(lfsPointer, "size 12345", "size -1", 1),
		strings.Replace(lfsPointer, "size 12345\n", "", 1),
		lfsPointer + "junk\n",
		lfsPointer + strings.Repeat("x y\n", 300),
	} {
		if isLFSPointer([]byte(s)) {
			t.Errorf("%q detected as a pointer", s)
		}
	}
	// Known sizes of 1024 bytes or more are never peeked.
	r, err := finishReader(&Reader{Blob: Blob{Size: 2048}, ReadCloser: io.NopCloser(strings.NewReader(lfsPointer))}, 1<<20)
	if err != nil || r.LFSPointer {
		t.Errorf("= %+v, %v", r, err)
	}
}

func TestMissing(t *testing.T) {
	for _, code := range []int{400, 404, 422} {
		err := missing(&forgehttp.Error{StatusCode: code, Err: forgehttp.ErrNotFound}, pathMissing("a"))
		if !errors.Is(err, ErrPathNotFound) || !errors.Is(err, forgehttp.ErrNotFound) || errors.Is(err, ErrRevisionNotFound) {
			t.Errorf("%d: %v", code, err)
		}
	}
	for _, err := range []error{
		&forgehttp.Error{StatusCode: 403, Err: forgehttp.ErrForbidden},
		&forgehttp.Error{StatusCode: 500},
		errors.New("network"),
	} {
		if got := missing(err, pathMissing("a")); got != err {
			t.Errorf("%v mapped to %v", err, got)
		}
	}
	if got := objectMissing(c1).Error(); got != "code: revision not found: object "+string(c1) {
		t.Errorf("message = %q", got)
	}
}

func TestMergeBase(t *testing.T) {
	ctx := context.Background()
	c := newTest(t, newFake(), Limits{})
	if got, err := c.MergeBase(ctx, repo, "main", "dev"); err != nil || got != c2 {
		t.Errorf("= %s, %v", got, err)
	}
	if got, err := c.MergeBase(ctx, repo, "main", string(m)); err != nil || got != m {
		t.Errorf("same commit = %s, %v", got, err)
	}
	if _, err := c.MergeBase(ctx, repo, "main", "a..b"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad revision: %v", err)
	}
}

func TestCompareOptions(t *testing.T) {
	o, err := CompareOptions{}.withDefaults()
	want := CompareOptions{Mode: MergeBase, Context: 3, MaxChanges: 3000, MaxPatchFiles: 100, MaxPatchBytes: 256 << 10}
	if err != nil || fmt.Sprint(o) != fmt.Sprint(want) {
		t.Errorf("= %+v, %v", o, err)
	}
	if o, _ := (CompareOptions{Context: -1}).withDefaults(); o.Context != 0 {
		t.Errorf("Context = %d", o.Context)
	}
	for _, bad := range []CompareOptions{{Mode: "x"}, {Paths: []string{"../a"}}, {MaxChanges: -1}} {
		if _, err := bad.withDefaults(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	c := newTest(t, newFake(), Limits{})
	// dev is the merge base of main and dev, so nothing changed.
	if d, err := c.Compare(context.Background(), repo, "main", "dev", CompareOptions{}); err != nil || len(d.Changes) != 0 || d.MergeBase != c2 {
		t.Errorf("Compare = %+v, %v", d, err)
	}
}

func TestWriteValidation(t *testing.T) {
	ctx := context.Background()
	c := newTest(t, newFake(), Limits{MaxWriteBytes: 10})
	sig := Signature{Name: "Mr Bean", Email: "bean@example.com"}
	good := NewCommit{
		Parents: []OID{c3}, Message: "msg", Author: sig, Ref: "refs/heads/main", Old: c3,
		Changes: []FileChange{
			{Path: "a", Content: []byte("x")}, {Path: "b", Delete: true}, {Path: "b/c", Mode: ModeSymlink, Content: []byte("a")},
			{Path: "s", Mode: ModeSubmodule, Submodule: c1},
		},
	}
	if _, err := c.CreateCommit(ctx, repo, good); !errors.Is(err, ErrUnsupported) {
		t.Errorf("valid commit: %v, want ErrUnsupported", err)
	}
	root := good
	root.Parents, root.Old, root.Changes = nil, ZeroOID, []FileChange{{Path: "a"}}
	if _, err := c.CreateCommit(ctx, repo, root); !errors.Is(err, ErrUnsupported) {
		t.Errorf("root commit: %v, want ErrUnsupported", err)
	}
	edit := func(f func(*NewCommit)) NewCommit {
		n := good
		n.Changes = slices.Clone(good.Changes)
		f(&n)
		return n
	}
	for i, n := range []NewCommit{
		edit(func(n *NewCommit) { n.Ref = "main" }),
		edit(func(n *NewCommit) { n.Ref = "refs/heads/a..b" }),
		edit(func(n *NewCommit) { n.Old = "" }),
		edit(func(n *NewCommit) { n.Parents = []OID{"abc"} }),
		edit(func(n *NewCommit) { n.Parents = []OID{c1, c1} }),
		edit(func(n *NewCommit) { n.Parents = []OID{ZeroOID} }),
		edit(func(n *NewCommit) { n.Message = " " }),
		edit(func(n *NewCommit) { n.Message = "a\x00b" }),
		edit(func(n *NewCommit) { n.Author.Email = "a>b" }),
		edit(func(n *NewCommit) { n.Author.Name = "" }),
		edit(func(n *NewCommit) { n.Committer = Signature{Name: "x\ny", Email: "e"} }),
		edit(func(n *NewCommit) { n.Changes[0].Path = ".git/config" }),
		edit(func(n *NewCommit) { n.Changes[0].Path = "a/../b" }),
		edit(func(n *NewCommit) { n.Changes[0].Path = "b" }),
		edit(func(n *NewCommit) { n.Changes[1].Content = []byte{} }),
		edit(func(n *NewCommit) { n.Changes[0].Mode = ModeTree }),
		edit(func(n *NewCommit) { n.Changes[0].Mode = 0o100600 }),
		edit(func(n *NewCommit) { n.Changes[0].Submodule = c1 }),
		edit(func(n *NewCommit) { n.Changes[3].Submodule = "" }),
		edit(func(n *NewCommit) { n.Changes[3].Content = []byte("x") }),
		edit(func(n *NewCommit) { n.Changes = append(n.Changes, FileChange{Path: "a/x"}) }),
		edit(func(n *NewCommit) { n.Parents = nil }), // deletes in a root commit
	} {
		if _, err := c.CreateCommit(ctx, repo, n); !errors.Is(err, ErrInvalid) {
			t.Errorf("%d: err = %v, want ErrInvalid", i, err)
		}
	}
	big := edit(func(n *NewCommit) { n.Changes[0].Content = make([]byte, 11) })
	if _, err := c.CreateCommit(ctx, repo, big); !errors.Is(err, ErrTooLarge) {
		t.Errorf("big: %v, want ErrTooLarge", err)
	}

	if err := c.UpdateRefs(ctx, repo, RefUpdate{Name: "refs/heads/a", Old: c1, New: c2}, RefUpdate{Name: "refs/tags/t", Old: ZeroOID, New: c2}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("valid updates: %v, want ErrUnsupported", err)
	}
	for i, u := range [][]RefUpdate{
		nil,
		{{Name: "main", Old: c1, New: c2}},
		{{Name: "refs/heads/a", Old: c1, New: c1}},
		{{Name: "refs/heads/a", Old: ZeroOID, New: ZeroOID}},
		{{Name: "refs/heads/a", Old: "", New: c1}},
		{{Name: "refs/heads/a", Old: c1, New: "HEAD"}},
		{{Name: "refs/heads/a", Old: c1, New: c2}, {Name: "refs/heads/a", Old: c2, New: c3}},
	} {
		if err := c.UpdateRefs(ctx, repo, u...); !errors.Is(err, ErrInvalid) {
			t.Errorf("%d: err = %v, want ErrInvalid", i, err)
		}
	}
}

func TestParseFileMode(t *testing.T) {
	for s, want := range map[string]FileMode{"100644": ModeFile, "100755": ModeExec, "40000": ModeTree, "040000": ModeTree, "120000": ModeSymlink, "160000": ModeSubmodule} {
		if got, err := ParseFileMode(s); err != nil || got != want {
			t.Errorf("%q = %v, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "644", "100600", "0100644", "x", "-1"} {
		if _, err := ParseFileMode(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", s, err)
		}
	}
	if ModeTree.String() != "040000" || typeOf(ModeSubmodule) != ObjectCommit || typeOf(ModeSymlink) != ObjectBlob {
		t.Error("mode helpers")
	}
}

func TestOID(t *testing.T) {
	if !c1.Valid() || !OID(strings.Repeat("f", 64)).Valid() || OID(strings.Repeat("F", 40)).Valid() || OID("abc").Valid() {
		t.Error("Valid")
	}
	if !ZeroOID.zero() || c1.zero() {
		t.Error("zero")
	}
	if got, err := parseOID(strings.Repeat("AB", 20)); err != nil || got != OID(strings.Repeat("ab", 20)) {
		t.Errorf("parseOID = %q, %v", got, err)
	}
	if _, err := parseOID("../x"); err == nil {
		t.Error("parseOID accepted junk")
	}
}

// stuck is a backend whose history never advances.
type stuck struct{ *fake }

func (stuck) history(o *op, tip OID, path string, n int, pos historyPos) ([]Commit, historyPos, bool, error) {
	return nil, pos, true, nil
}

func TestBackendMisbehaviour(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	f.def = "a..b"
	if _, err := newTest(t, f, Limits{}).Resolve(ctx, repo, "HEAD"); err == nil || errors.Is(err, ErrRevisionNotFound) {
		t.Errorf("invalid default branch: %v", err)
	}
	if _, err := newTest(t, f, Limits{}).ListRefs(ctx, repo, RefsOptions{}); err == nil {
		t.Error("ListRefs accepted an invalid default branch")
	}
	// Git allows these names but the library refuses them: ListRefs still
	// works and HEAD is ErrUnsupported.
	for _, def := range []string{"100%-coverage", "-dash"} {
		f := newFake()
		f.def = def
		c := newTest(t, f, Limits{})
		list, err := c.ListRefs(ctx, repo, RefsOptions{})
		if err != nil || list.Default != "refs/heads/"+def || len(list.Refs) == 0 {
			t.Errorf("default %q: ListRefs = %+v, %v", def, list, err)
		}
		if _, err := c.Resolve(ctx, repo, "HEAD"); !errors.Is(err, ErrUnsupported) {
			t.Errorf("default %q: HEAD = %v, want ErrUnsupported", def, err)
		}
	}
	s := &forgeauth.Session{Kind: "fake", BaseURL: "http://forge.invalid", Client: http.DefaultClient}
	c, _ := newClient(Limits{}, func(forgeauth.Kind) (backend, bool) { return stuck{newFake()}, true }, []Source{{ID: "src", Session: s}})
	if _, err := c.History(ctx, repo, HistoryOptions{}); err == nil {
		t.Error("history that does not advance was accepted")
	}
}
