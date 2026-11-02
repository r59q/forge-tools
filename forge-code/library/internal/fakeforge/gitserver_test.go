package fakeforge

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-code/library/internal/gitwire"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// gitEnv is a seeded repository at o/r behind the Git front.
type gitEnv struct {
	s    *Server
	r    *Repo
	fx   Fixture
	git  *forgeauth.GitRemote
	url  string
	main string
}

func newGitEnv(t *testing.T, opts GitOptions) *gitEnv {
	t.Helper()
	s := NewServer(t, forgeauth.GitHub, nil)
	s.Mount(GitPrefix, GitFront(opts))
	r := s.NewRepo("o/r")
	fx := Seed(r)
	git, err := s.GitRemote()
	if err != nil {
		t.Fatal(err)
	}
	url, err := git.URL("o/r")
	if err != nil {
		t.Fatal(err)
	}
	return &gitEnv{s: s, r: r, fx: fx, git: git, url: url, main: "refs/heads/main"}
}

// push pushes cmds with caps and a pack of objs (nil for none).
func (e *gitEnv) push(t *testing.T, cmds []gitwire.Command, caps []string, objs []gitobj.Object) *gitwire.Report {
	t.Helper()
	rep, err := e.pushErr(cmds, caps, objs)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	return rep
}

func (e *gitEnv) pushErr(cmds []gitwire.Command, caps []string, objs []gitobj.Object) (*gitwire.Report, error) {
	var pack *bytes.Buffer
	if objs != nil {
		pack = &bytes.Buffer{}
		if err := gitwire.WritePack(pack, objs); err != nil {
			return nil, err
		}
		return gitwire.Push(context.Background(), e.git.Client, e.url, cmds, caps, pack)
	}
	return gitwire.Push(context.Background(), e.git.Client, e.url, cmds, caps, nil)
}

func (e *gitEnv) ref(name string) gitobj.ID {
	id, _ := e.r.Ref(name)
	return id
}

// newCommit returns a commit object on top of parent that is not stored,
// with a new blob and tree.
func newCommit(t *testing.T, parent gitobj.ID, msg string) (gitobj.ID, []gitobj.Object) {
	t.Helper()
	blob := gitobj.Object{Type: gitobj.TypeBlob, Data: []byte(msg)}
	tdata, err := gitobj.EncodeTree([]gitobj.TreeEntry{{Name: "f", Mode: gitobj.ModeFile, ID: blob.ID()}})
	if err != nil {
		t.Fatal(err)
	}
	tree := gitobj.Object{Type: gitobj.TypeTree, Data: tdata}
	c := gitobj.Commit{Tree: tree.ID(), Author: Signature(Epoch), Committer: Signature(Epoch), Message: msg}
	if !parent.IsZero() {
		c.Parents = []gitobj.ID{parent}
	}
	cdata, err := gitobj.EncodeCommit(c)
	if err != nil {
		t.Fatal(err)
	}
	commit := gitobj.Object{Type: gitobj.TypeCommit, Data: cdata}
	return commit.ID(), []gitobj.Object{blob, tree, commit}
}

func wantStatus(t *testing.T, rep *gitwire.Report, name string, ok bool, reason string) {
	t.Helper()
	st, found := rep.Refs[name]
	if !found || st.OK != ok || (!ok && st.Reason != reason) {
		t.Errorf("%s: status %+v (reported %v), want ok=%v reason %q", name, st, found, ok, reason)
	}
}

func TestGitFrontAdvertise(t *testing.T) {
	e := newGitEnv(t, GitOptions{})
	adv, err := gitwire.Advertise(context.Background(), e.git.Client, e.url)
	if err != nil {
		t.Fatal(err)
	}
	if adv.Refs[e.main] != e.fx.Head || adv.Refs["refs/tags/v1.0"] != e.fx.Tag || len(adv.Refs) != 7 {
		t.Errorf("refs = %v", adv.Refs)
	}
	for _, c := range []string{"report-status", "atomic", "delete-refs", "side-band-64k"} {
		if !adv.Caps.Has(c) {
			t.Errorf("caps %v lack %s", adv.Caps, c)
		}
	}

	e = newGitEnv(t, GitOptions{NoAtomic: true, NoDeleteRefs: true})
	adv, err = gitwire.Advertise(context.Background(), e.git.Client, e.url)
	if err != nil || adv.Caps.Has("atomic") || adv.Caps.Has("delete-refs") {
		t.Errorf("caps without atomic and delete-refs: %v, %v", adv, err)
	}

	e = newGitEnv(t, GitOptions{ObjectFormat: "sha256"})
	if _, err := gitwire.Advertise(context.Background(), e.git.Client, e.url); !errors.Is(err, gitwire.ErrUnsupported) {
		t.Errorf("sha256: %v, want ErrUnsupported", err)
	}
}

func TestGitFrontPush(t *testing.T) {
	e := newGitEnv(t, GitOptions{})
	caps := []string{"report-status", "side-band-64k"}

	// A move with an empty pack.
	rep := e.push(t, []gitwire.Command{{Name: e.main, Old: e.fx.Head, New: e.fx.Main[4]}}, caps, nil)
	wantStatus(t, rep, e.main, true, "")
	if !rep.UnpackOK || e.ref(e.main) != e.fx.Main[4] {
		t.Errorf("move: %+v, main at %s", rep, e.ref(e.main))
	}
	// Old must match.
	rep = e.push(t, []gitwire.Command{{Name: e.main, Old: e.fx.Head, New: e.fx.Main[3]}}, caps, nil)
	wantStatus(t, rep, e.main, false, "failed to update ref")
	// Creating an existing ref fails; creating a new one with a pack works.
	id, objs := newCommit(t, e.fx.Head, "new")
	rep = e.push(t, []gitwire.Command{{Name: e.main, New: id}}, []string{"report-status"}, objs)
	wantStatus(t, rep, e.main, false, "failed to update ref")
	if _, ok := e.r.Object(id); ok {
		t.Error("objects of a failed push were stored")
	}
	rep = e.push(t, []gitwire.Command{{Name: "refs/heads/new", New: id}}, caps, objs)
	wantStatus(t, rep, "refs/heads/new", true, "")
	if c, err := e.r.ReadCommit(id); err != nil || e.ref("refs/heads/new") != id || c.Message != "new" {
		t.Errorf("created commit %v, %v", c, err)
	}
	// A delete sends no pack.
	rep = e.push(t, []gitwire.Command{{Name: "refs/heads/new", Old: id}}, []string{"report-status", "delete-refs"}, nil)
	wantStatus(t, rep, "refs/heads/new", true, "")
	if _, ok := e.r.Ref("refs/heads/new"); ok {
		t.Error("ref not deleted")
	}

	// Atomic: one stale command fails all.
	cmds := []gitwire.Command{
		{Name: e.main, Old: e.fx.Main[4], New: e.fx.Head},
		{Name: "refs/heads/feature", Old: e.fx.Head, New: e.fx.Main[0]},
	}
	rep = e.push(t, cmds, []string{"report-status", "atomic"}, nil)
	wantStatus(t, rep, e.main, false, "atomic push failure")
	wantStatus(t, rep, "refs/heads/feature", false, "failed to update ref")
	if e.ref(e.main) != e.fx.Main[4] {
		t.Error("atomic push applied in part")
	}
	// Without atomic, each command applies on its own.
	rep = e.push(t, cmds, []string{"report-status"}, nil)
	wantStatus(t, rep, e.main, true, "")
	wantStatus(t, rep, "refs/heads/feature", false, "failed to update ref")
	if e.ref(e.main) != e.fx.Head {
		t.Error("non-atomic push did not apply the good command")
	}

	// Missing objects, a branch to a non-commit, a bad pack and a
	// non-canonical tree.
	missing := gitobj.Hash(gitobj.TypeCommit, []byte("missing"))
	rep = e.push(t, []gitwire.Command{{Name: "refs/heads/x", New: missing}}, caps, nil)
	wantStatus(t, rep, "refs/heads/x", false, "missing necessary objects")
	partial, objs := newCommit(t, e.fx.Head, "partial")
	rep = e.push(t, []gitwire.Command{{Name: "refs/heads/x", New: partial}}, caps, objs[2:])
	wantStatus(t, rep, "refs/heads/x", false, "missing necessary objects")
	rep = e.push(t, []gitwire.Command{{Name: "refs/heads/x", New: e.fx.Tag}}, caps, nil)
	wantStatus(t, rep, "refs/heads/x", false, "non-commit object for a branch")
	rep = e.push(t, []gitwire.Command{{Name: "refs/tags/x", New: e.fx.Tag}}, caps, nil)
	wantStatus(t, rep, "refs/tags/x", true, "")
	blob := gitobj.Object{Type: gitobj.TypeBlob, Data: []byte("b")}
	bad := gitobj.Object{Type: gitobj.TypeTree, Data: append(append([]byte("100644 b\x00"), blob.ID().String()[:20]...),
		append([]byte("100644 a\x00"), blob.ID().String()[:20]...)...)}
	rep = e.push(t, []gitwire.Command{{Name: "refs/heads/x", New: bad.ID()}}, caps, []gitobj.Object{blob, bad})
	if rep.UnpackOK || !strings.Contains(rep.Unpack, "not canonical") {
		t.Errorf("non-canonical tree: %+v", rep)
	}
	wantStatus(t, rep, "refs/heads/x", false, "unpacker error")
}

func TestGitFrontOptions(t *testing.T) {
	caps := []string{"report-status", "atomic", "delete-refs"}

	e := newGitEnv(t, GitOptions{NoDeleteRefs: true, NoAtomic: true})
	rep := e.push(t, []gitwire.Command{
		{Name: "refs/heads/feature", Old: e.fx.Feature[2]},
		{Name: e.main, Old: e.fx.Head, New: e.fx.Main[4]},
	}, caps, nil)
	wantStatus(t, rep, "refs/heads/feature", false, "deletion prohibited")
	wantStatus(t, rep, e.main, true, "") // atomic was not advertised

	e = newGitEnv(t, GitOptions{Decline: func(cmds []gitwire.Command) bool { return cmds[0].Name == "refs/heads/main" }})
	rep = e.push(t, []gitwire.Command{{Name: e.main, Old: e.fx.Head, New: e.fx.Main[4]}}, caps, nil)
	wantStatus(t, rep, e.main, false, "pre-receive hook declined")
	if e.ref(e.main) != e.fx.Head {
		t.Error("declined push applied")
	}

	var seen []gitwire.Command
	e = newGitEnv(t, GitOptions{BeforeReceive: func(r *Repo, cmds []gitwire.Command) {
		seen = cmds
		r.SetRef("refs/heads/main", gitobj.ZeroID)
	}})
	rep = e.push(t, []gitwire.Command{{Name: e.main, Old: e.fx.Head, New: e.fx.Main[4]}}, caps, nil)
	wantStatus(t, rep, e.main, false, "failed to update ref")
	if len(seen) != 1 || seen[0].New != e.fx.Main[4] {
		t.Errorf("BeforeReceive saw %v", seen)
	}

	e = newGitEnv(t, GitOptions{AbortBeforeApply: true})
	if _, err := e.pushErr([]gitwire.Command{{Name: e.main, Old: e.fx.Head, New: e.fx.Main[4]}}, caps, nil); err == nil {
		t.Error("abort before apply: no error")
	}
	if e.ref(e.main) != e.fx.Head {
		t.Error("abort before apply moved the ref")
	}
	e = newGitEnv(t, GitOptions{AbortAfterApply: true})
	if _, err := e.pushErr([]gitwire.Command{{Name: e.main, Old: e.fx.Head, New: e.fx.Main[4]}}, caps, nil); err == nil {
		t.Error("abort after apply: no error")
	}
	if e.ref(e.main) != e.fx.Main[4] {
		t.Error("abort after apply did not move the ref")
	}
}

func TestGitFrontRace(t *testing.T) {
	e := newGitEnv(t, GitOptions{})
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := range n {
		id, objs := newCommit(t, e.fx.Head, strings.Repeat("x", i+1))
		wg.Go(func() {
			rep, err := e.pushErr([]gitwire.Command{{Name: e.main, Old: e.fx.Head, New: id}}, []string{"report-status"}, objs)
			if err != nil {
				t.Error(err)
				return
			}
			if rep.Refs[e.main].OK {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if wins != 1 {
		t.Errorf("%d pushes won, want 1", wins)
	}
}

func TestGitFrontFetch(t *testing.T) {
	ctx := context.Background()
	e := newGitEnv(t, GitOptions{})
	for _, id := range []gitobj.ID{e.fx.Main[0], e.fx.Main[1], e.fx.Head} {
		data, err := gitwire.FetchCommit(ctx, e.git.Client, e.url, id)
		o, _ := e.r.Object(id)
		if err != nil || !bytes.Equal(data, o.Data) {
			t.Errorf("FetchCommit(%s) = %q, %v", id, data, err)
		}
	}
	orphan := e.r.CommitTree(e.r.BuildTree(map[string]File{"o": {Data: []byte("o")}}), Epoch, "orphan\n")
	if _, err := gitwire.FetchCommit(ctx, e.git.Client, e.url, orphan); !errors.Is(err, gitwire.ErrRemote) {
		t.Errorf("unreachable commit: %v, want ErrRemote", err)
	}
	if _, err := gitwire.FetchCommit(ctx, e.git.Client, e.url, e.fx.Blobs[PathReadme]); !errors.Is(err, gitwire.ErrRemote) {
		t.Errorf("blob: %v, want ErrRemote", err)
	}

	e = newGitEnv(t, GitOptions{NoFilter: true})
	if _, err := gitwire.FetchCommit(ctx, e.git.Client, e.url, e.fx.Head); !errors.Is(err, gitwire.ErrUnsupported) {
		t.Errorf("no filter: %v, want ErrUnsupported", err)
	}
}

func TestGitFrontAccess(t *testing.T) {
	ctx := context.Background()
	e := newGitEnv(t, GitOptions{})
	status := func(err error) int {
		var fe *forgehttp.Error
		if errors.As(err, &fe) {
			return fe.StatusCode
		}
		return 0
	}

	e.s.SetAccess("o/r", AccessRead)
	if _, err := gitwire.Advertise(ctx, e.git.Client, e.url); status(err) != http.StatusForbidden {
		t.Errorf("read-only advertise: %v, want 403", err)
	}
	if _, err := e.pushErr([]gitwire.Command{{Name: e.main, Old: e.fx.Head, New: e.fx.Main[4]}}, []string{"report-status"}, nil); status(err) != http.StatusForbidden {
		t.Errorf("read-only push: %v, want 403", err)
	}
	if _, err := gitwire.FetchCommit(ctx, e.git.Client, e.url, e.fx.Head); err != nil {
		t.Errorf("read-only fetch: %v", err)
	}
	e.r.SetPrivate(true)
	e.s.SetAccess("o/r", AccessNone)
	if _, err := gitwire.FetchCommit(ctx, e.git.Client, e.url, e.fx.Head); status(err) != http.StatusNotFound {
		t.Errorf("private fetch: %v, want 404", err)
	}
	e.s.SetAccess("o/r", AccessWrite)

	missing, _ := e.git.URL("o/missing")
	if _, err := gitwire.Advertise(ctx, e.git.Client, missing); status(err) != http.StatusNotFound {
		t.Errorf("missing repo: %v, want 404", err)
	}
	e.s.SetToken("other")
	if _, err := gitwire.Advertise(ctx, e.git.Client, e.url); !errors.Is(err, forgehttp.ErrUnauthorized) {
		t.Errorf("wrong token: %v, want 401", err)
	}
	e.s.SetToken(Token)

	for _, tc := range []struct{ method, path, ctype string }{
		{"GET", "/git/o/r.git/info/refs?service=git-upload-pack", ""}, // v2 only
		{"GET", "/git/o/r.git/info/refs", ""},
		{"GET", "/git/o/r/info/refs?service=git-receive-pack", ""},
		{"GET", "/git/.git/info/refs?service=git-receive-pack", ""},
		{"GET", "/git/o/r.git/HEAD", ""},
		{"POST", "/git/o/r.git/git-receive-pack", "text/plain"},
		{"POST", "/git/o/r.git/git-upload-pack", "text/plain"},
		{"GET", "/git/o/r.git/git-receive-pack", ""},
	} {
		req, _ := http.NewRequest(tc.method, e.s.HTTP.URL+tc.path, strings.NewReader("0000"))
		req.SetBasicAuth("x-access-token", Token)
		if tc.ctype != "" {
			req.Header.Set("Content-Type", tc.ctype)
		}
		resp, err := e.s.HTTP.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Errorf("%s %s: status %d, want an error", tc.method, tc.path, resp.StatusCode)
		}
	}
}
