package code

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-code/library/internal/fakeforge"
	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-code/library/internal/gitwire"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// realGit pairs a fake forge's REST front, for reads, with a real bare
// repository behind git http-backend, for pushes. Both start with the
// Seed fixture; sync copies pushed objects and refs back to the fake.
type realGit struct {
	*writeEnv
	git func(args ...string) string // output trimmed
	raw func(args ...string) []byte
	dir string

	mu     sync.Mutex
	before func(r *http.Request) // runs before each request reaches git
}

// gitEnviron isolates git from the user's and system's config.
func gitEnviron(home string) []string {
	return append(os.Environ(), "HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
}

// forEachRealGit runs fn on a fresh realGit per forge kind. It skips the
// test when git is not on PATH.
func forEachRealGit(t *testing.T, fn func(t *testing.T, g *realGit)) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	for _, f := range confForges {
		t.Run(string(f.kind), func(t *testing.T) {
			fn(t, newRealGit(t, gitPath, f.kind, f.front, f.repo))
		})
	}
}

func newRealGit(t *testing.T, gitPath string, kind forgeauth.Kind, front fakeforge.Front, path string) *realGit {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, filepath.FromSlash(path)+".git")
	run := func(stdin []byte, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(gitPath, args...)
		cmd.Dir = dir
		cmd.Env = gitEnviron(root)
		cmd.Stdin = bytes.NewReader(stdin)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
		}
		return out
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = run(nil, "init", "-q", "--bare", "-b", "main", "--object-format=sha1", ".")
	for _, kv := range [][2]string{
		{"http.receivepack", "true"},
		{"receive.fsckObjects", "true"},
		{"uploadpack.allowFilter", "true"},
		{"uploadpack.allowAnySHA1InWant", "true"},
	} {
		run(nil, "config", kv[0], kv[1])
	}

	s := fakeforge.NewServer(t, kind, front)
	r := s.NewRepo(path)
	fx := fakeforge.Seed(r)
	var objs []gitobj.Object
	for _, id := range r.IDs() {
		o, _ := r.Object(id)
		objs = append(objs, o)
	}
	var pack bytes.Buffer
	if err := gitwire.WritePack(&pack, objs); err != nil {
		t.Fatal(err)
	}
	run(pack.Bytes(), "unpack-objects", "-q")
	for _, ref := range r.Refs("refs/") {
		run(nil, "update-ref", ref.Name, ref.ID.String())
	}

	g := &realGit{dir: dir}
	backend := &cgi.Handler{
		Path:   gitPath,
		Args:   []string{"http-backend"},
		Env:    append(gitEnviron(root), "GIT_PROJECT_ROOT="+root, "GIT_HTTP_EXPORT_ALL=1"),
		Stderr: io.Discard,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		g.mu.Lock()
		before := g.before
		g.mu.Unlock()
		if before != nil {
			before(req)
		}
		backend.ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)

	remote := &forgeauth.GitRemote{BaseURL: srv.URL, Client: srv.Client()}
	c, err := New(Source{ID: "src", Session: s.Session, Git: remote})
	if err != nil {
		t.Fatal(err)
	}
	g.writeEnv = &writeEnv{kind: kind, s: s, repo: r, fx: fx, c: c, path: path,
		ref: RepositoryRef{Source: "src", Repository: path}}
	g.git = func(args ...string) string { return strings.TrimSpace(string(run(nil, args...))) }
	g.raw = func(args ...string) []byte { return run(nil, args...) }
	return g
}

// setBefore sets the request hook.
func (g *realGit) setBefore(fn func(r *http.Request)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.before = fn
}

// sync copies every object and ref of the real repository to the fake.
func (g *realGit) sync(t *testing.T) {
	t.Helper()
	br := bufio.NewReader(bytes.NewReader(g.raw("cat-file", "--batch-all-objects", "--batch")))
	for {
		header, err := br.ReadString('\n')
		if err == io.EOF {
			break
		}
		f := strings.Fields(header)
		if err != nil || len(f) != 3 {
			t.Fatalf("cat-file header %q: %v", header, err)
		}
		size, _ := strconv.Atoi(f[2])
		data := make([]byte, size+1)
		if _, err := io.ReadFull(br, data); err != nil {
			t.Fatal(err)
		}
		if _, err := g.repo.Put(gitobj.Object{Type: gitobj.Type(f[1]), Data: data[:size]}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ref := range g.repo.Refs("refs/") {
		g.repo.SetRef(ref.Name, gitobj.ZeroID)
	}
	for line := range strings.SplitSeq(g.git("for-each-ref", "--format=%(objectname) %(refname)"), "\n") {
		hex, name, _ := strings.Cut(line, " ")
		id, err := gitobj.ParseID(hex)
		if err != nil {
			t.Fatalf("for-each-ref line %q", line)
		}
		g.repo.SetRef(name, id)
	}
}

// fsck runs a strict git fsck.
func (g *realGit) fsck(t *testing.T) {
	t.Helper()
	g.git("fsck", "--strict", "--no-dangling", "--no-progress")
}

func TestRealGitCreateCommit(t *testing.T) {
	ids := map[forgeauth.Kind]OID{}
	forEachRealGit(t, func(t *testing.T, g *realGit) {
		ctx := context.Background()
		head := confOID(g.fx.Head)
		cm, err := g.c.CreateCommit(ctx, g.ref, NewCommit{
			Parents: []OID{head}, Changes: editChanges, Author: testAuthor,
			Message: "Edit files\n", Ref: mainRef, Old: head,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[g.kind] = cm.ID
		g.fsck(t)
		if got := g.git("rev-parse", "refs/heads/main", "main^{tree}"); got != string(cm.ID)+"\n"+string(cm.Tree) {
			t.Errorf("rev-parse = %q, want %s %s", got, cm.ID, cm.Tree)
		}
		ls := g.git("ls-tree", "-r", "main")
		for _, want := range []string{
			"\t" + fakeforge.PathMain,
			"\tlink2",
			"160000 commit " + string(otherSubmodule) + "\tvendor/other",
			"160000 commit " + fakeforge.SubmoduleCommit.String() + "\tvendor/lib",
			"100644 blob e69de29bb2d1d6434b8b29ae775ad8c2e48c5391\tempty.txt",
			"\ta/b/c.txt",
		} {
			if !strings.Contains(ls, want) {
				t.Errorf("ls-tree lacks %q:\n%s", want, ls)
			}
		}
		for p, mode := range map[string]string{fakeforge.PathMain: "100755", fakeforge.PathExec: "100755", "link2": "120000"} {
			if got := g.git("ls-tree", "main", p); !strings.HasPrefix(got, mode+" ") {
				t.Errorf("%s: ls-tree %q, want mode %s", p, got, mode)
			}
		}
		if strings.Contains(ls, "docs/") || strings.Contains(ls, "\tlink\n") {
			t.Errorf("deleted paths still in ls-tree:\n%s", ls)
		}
		if got := g.git("cat-file", "-p", "main:link2"); got != "src/main.go" {
			t.Errorf("symlink target %q", got)
		}
		raw := g.git("cat-file", "commit", "main")
		sig := fmt.Sprintf("Ada Lovelace <ada@example.com> %d +0200", testAuthor.When.Unix())
		if !strings.Contains(raw, "author "+sig) || !strings.Contains(raw, "committer "+sig) ||
			!strings.Contains(raw, "parent "+string(head)) {
			t.Errorf("commit object:\n%s", raw)
		}

		// Build on the pushed commit: a merge, then a root commit and a
		// branch from a commit that is not a tip.
		g.sync(t)
		_, err = g.c.CreateCommit(ctx, g.ref, NewCommit{
			Parents: []OID{cm.ID, confOID(g.fx.Feature[2])}, Author: testAuthor, Message: "Merge\n",
			Changes: []FileChange{{Path: fakeforge.PathFeature, Content: []byte("merged\n")}},
			Ref:     mainRef, Old: cm.ID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := g.git("rev-parse", "main^1", "main^2"); got != string(cm.ID)+"\n"+g.fx.Feature[2].String() {
			t.Errorf("merge parents %q", got)
		}
		root, err := g.c.CreateCommit(ctx, g.ref, NewCommit{
			Author: testAuthor, Message: "Root\n", Ref: "refs/heads/root", Old: ZeroOID,
			Changes: []FileChange{{Path: "x/y", Content: []byte("y\n")}, {Path: "l", Mode: ModeSymlink, Content: []byte("x/y")}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if raw := g.git("cat-file", "commit", "refs/heads/root"); strings.Contains(raw, "parent ") {
			t.Errorf("root commit has a parent:\n%s", raw)
		}
		if got := g.git("rev-parse", "refs/heads/root^{tree}"); got != string(root.Tree) {
			t.Errorf("root tree %s, want %s", got, root.Tree)
		}
		side, err := g.c.CreateCommit(ctx, g.ref, NewCommit{
			Parents: []OID{confOID(g.fx.Main[1])}, Author: testAuthor, Message: "Side\n",
			Changes: []FileChange{{Path: fakeforge.PathReadme, Delete: true}},
			Ref:     "refs/heads/side", Old: ZeroOID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := g.git("rev-parse", "refs/heads/side"); got != string(side.ID) {
			t.Errorf("side at %s, want %s", got, side.ID)
		}
		g.fsck(t)
	})
	if len(ids) == 3 && (ids[forgeauth.GitHub] != ids[forgeauth.GitLab] || ids[forgeauth.GitHub] != ids[forgeauth.Forgejo]) {
		t.Errorf("same commit got different IDs: %v", ids)
	}
}

func TestRealGitUpdateRefs(t *testing.T) {
	forEachRealGit(t, func(t *testing.T, g *realGit) {
		ctx := context.Background()
		head, main4, feat := confOID(g.fx.Head), confOID(g.fx.Main[4]), confOID(g.fx.Feature[2])
		up := func(u ...RefUpdate) error { return g.c.UpdateRefs(ctx, g.ref, u...) }

		wantErr(t, "create", up(RefUpdate{Name: "refs/heads/new", Old: ZeroOID, New: head}), nil)
		wantErr(t, "create on existing", up(RefUpdate{Name: "refs/heads/new", Old: ZeroOID, New: main4}), ErrStale)
		wantErr(t, "atomic move and delete", up(
			RefUpdate{Name: mainRef, Old: head, New: main4},
			RefUpdate{Name: "refs/heads/new", Old: head, New: ZeroOID},
		), nil)
		if got := g.git("for-each-ref", "--format=%(refname)", "refs/heads/new"); got != "" {
			t.Errorf("refs/heads/new not deleted: %q", got)
		}

		// Real git refuses the stale command after the advertisement.
		moveMain := func(r *http.Request) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
				g.git("update-ref", mainRef, g.fx.Main[0].String())
			}
		}
		g.setBefore(moveMain)
		wantErr(t, "moved before push", up(RefUpdate{Name: mainRef, Old: main4, New: head}), ErrStale)
		g.git("update-ref", mainRef, string(main4))
		err := up(
			RefUpdate{Name: "refs/heads/feature", Old: feat, New: head},
			RefUpdate{Name: mainRef, Old: main4, New: head},
		)
		wantErr(t, "atomic rollback", err, ErrStale)
		if got := g.git("rev-parse", "refs/heads/feature"); got != string(feat) {
			t.Errorf("feature moved to %s in a failed atomic push", got)
		}
		g.setBefore(nil)

		hook := filepath.Join(g.dir, "hooks", "pre-receive")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\necho no >&2\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		wantErr(t, "hook declined", up(RefUpdate{Name: "refs/heads/feature", Old: feat, New: head}), ErrRejected)
		if got := g.git("rev-parse", "refs/heads/feature"); got != string(feat) {
			t.Errorf("declined push moved feature to %s", got)
		}
		g.fsck(t)
	})
}
