package gitwire

import (
	"bytes"
	"context"
	"io"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// gitEnv isolates git from the user's and system's config.
func gitEnv(home string) []string {
	return []string{"HOME=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
}

// gitServer serves a new bare repository through git http-backend. It
// returns the repository URL and a function that runs git in it.
func gitServer(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo.git")
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(gitPath, args...)
		cmd.Dir = repo
		cmd.Env = append(cmd.Environ(), gitEnv(root)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	init := exec.Command(gitPath, "init", "-q", "--bare", "-b", "main", repo)
	init.Env = append(init.Environ(), gitEnv(root)...)
	if out, err := init.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for _, kv := range [][2]string{
		{"http.receivepack", "true"},
		{"receive.fsckObjects", "true"},
		{"uploadpack.allowFilter", "true"},
		{"uploadpack.allowAnySHA1InWant", "true"},
	} {
		run("config", kv[0], kv[1])
	}
	srv := httptest.NewServer(&cgi.Handler{
		Path:   gitPath,
		Args:   []string{"http-backend"},
		Env:    append(gitEnv(root), "GIT_PROJECT_ROOT="+root, "GIT_HTTP_EXPORT_ALL=1"),
		Stderr: io.Discard,
	})
	t.Cleanup(srv.Close)
	return srv.URL + "/repo.git", run
}

// e2eObjects builds a root commit with a file, an executable, a symlink,
// a submodule and a subdirectory, and a child commit that edits the file.
func e2eObjects(t *testing.T) (root, child gitobj.ID, objs []gitobj.Object) {
	t.Helper()
	add := func(typ gitobj.Type, data []byte) gitobj.ID {
		o := gitobj.Object{Type: typ, Data: data}
		objs = append(objs, o)
		return o.ID()
	}
	tree := func(es ...gitobj.TreeEntry) gitobj.ID {
		data, err := gitobj.EncodeTree(es)
		if err != nil {
			t.Fatal(err)
		}
		return add(gitobj.TypeTree, data)
	}
	commit := func(c gitobj.Commit) gitobj.ID {
		data, err := gitobj.EncodeCommit(c)
		if err != nil {
			t.Fatal(err)
		}
		return add(gitobj.TypeCommit, data)
	}
	file := add(gitobj.TypeBlob, []byte("hello\n"))
	script := add(gitobj.TypeBlob, []byte("#!/bin/sh\necho hi\n"))
	link := add(gitobj.TypeBlob, []byte("file.txt"))
	bin := add(gitobj.TypeBlob, []byte{0, 0xff, 0xfe, 'x'})
	sub := tree(gitobj.TreeEntry{Name: "bin.dat", Mode: gitobj.ModeFile, ID: bin})
	mod := gitobj.Hash(gitobj.TypeCommit, []byte("elsewhere"))
	t1 := tree(
		gitobj.TreeEntry{Name: "file.txt", Mode: gitobj.ModeFile, ID: file},
		gitobj.TreeEntry{Name: "run.sh", Mode: gitobj.ModeExec, ID: script},
		gitobj.TreeEntry{Name: "link", Mode: gitobj.ModeSymlink, ID: link},
		gitobj.TreeEntry{Name: "dir", Mode: gitobj.ModeTree, ID: sub},
		gitobj.TreeEntry{Name: "dir.txt", Mode: gitobj.ModeFile, ID: file},
		gitobj.TreeEntry{Name: "vendor", Mode: gitobj.ModeSubmodule, ID: mod},
	)
	when := time.Unix(1700000000, 0).In(time.FixedZone("", -7*3600))
	sig := gitobj.Signature{Name: "A U Thor", Email: "author@example.com", When: when}
	root = commit(gitobj.Commit{Tree: t1, Author: sig, Committer: sig, Message: "root\n"})
	edited := add(gitobj.TypeBlob, []byte("hello again\n"))
	t2 := tree(
		gitobj.TreeEntry{Name: "file.txt", Mode: gitobj.ModeFile, ID: edited},
		gitobj.TreeEntry{Name: "run.sh", Mode: gitobj.ModeExec, ID: script},
		gitobj.TreeEntry{Name: "link", Mode: gitobj.ModeSymlink, ID: link},
		gitobj.TreeEntry{Name: "dir", Mode: gitobj.ModeTree, ID: sub},
		gitobj.TreeEntry{Name: "dir.txt", Mode: gitobj.ModeFile, ID: file},
		gitobj.TreeEntry{Name: "vendor", Mode: gitobj.ModeSubmodule, ID: mod},
	)
	child = commit(gitobj.Commit{Tree: t2, Parents: []gitobj.ID{root}, Author: sig, Committer: sig, Message: "edit\n"})
	return root, child, objs
}

func TestGitEndToEnd(t *testing.T) {
	url, git := gitServer(t)
	ctx := context.Background()
	push := func(cmds []Command, objs []gitobj.Object, extra ...string) *Report {
		t.Helper()
		a, err := Advertise(ctx, nil, url)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []string{"report-status", "atomic", "delete-refs", "side-band-64k"} {
			if !a.Caps.Has(c) {
				t.Fatalf("git does not advertise %s: %v", c, a.Caps)
			}
		}
		caps := append([]string{"report-status", "object-format=sha1", "agent=forge-code-test"}, extra...)
		var pack *bytes.Buffer
		if objs != nil {
			pack = &bytes.Buffer{}
			if err := WritePack(pack, objs); err != nil {
				t.Fatal(err)
			}
		}
		var rep *Report
		if pack == nil {
			rep, err = Push(ctx, nil, url, cmds, caps, nil)
		} else {
			rep, err = Push(ctx, nil, url, cmds, caps, pack)
		}
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}

	// Empty repository.
	a, err := Advertise(ctx, nil, url)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Refs) != 0 {
		t.Fatalf("empty repo has refs %v", a.Refs)
	}

	root, child, objs := e2eObjects(t)
	rep := push([]Command{{Name: "refs/heads/main", New: child}}, objs, "side-band-64k")
	if !rep.UnpackOK || !rep.Refs["refs/heads/main"].OK {
		t.Fatalf("push: %+v", rep)
	}
	git("fsck", "--strict", "--no-dangling")
	if got := git("rev-parse", "refs/heads/main"); got != child.String() {
		t.Fatalf("main = %s, want %s", got, child)
	}
	if got := git("cat-file", "-p", "main:file.txt"); got != "hello again" {
		t.Fatalf("file.txt = %q", got)
	}
	if got := git("rev-parse", "main^"); got != root.String() {
		t.Fatalf("parent = %s", got)
	}
	ls := git("ls-tree", "main")
	for _, want := range []string{"100755 blob", "120000 blob", "160000 commit", "040000 tree"} {
		if !strings.Contains(ls, want) {
			t.Fatalf("ls-tree lacks %q:\n%s", want, ls)
		}
	}
	if got := git("log", "-1", "--format=%an <%ae> %ad", "--date=raw", "main"); got != "A U Thor <author@example.com> 1700000000 -0700" {
		t.Fatalf("author %q", got)
	}

	// Stale old value: ng, ref unchanged.
	rep = push([]Command{{Name: "refs/heads/main", Old: root, New: root}}, []gitobj.Object{}, "side-band-64k")
	if st := rep.Refs["refs/heads/main"]; st.OK || st.Reason == "" {
		t.Fatalf("stale push: %+v", rep)
	}
	if got := git("rev-parse", "main"); got != child.String() {
		t.Fatalf("main moved to %s", got)
	}

	// Atomic: a good update and a stale create fail together.
	rep = push([]Command{
		{Name: "refs/heads/old", New: root},
		{Name: "refs/heads/main", Old: gitobj.ZeroID, New: root},
	}, []gitobj.Object{}, "atomic")
	if rep.Refs["refs/heads/old"].OK || rep.Refs["refs/heads/main"].OK {
		t.Fatalf("atomic: %+v", rep)
	}
	if out := git("for-each-ref", "refs/heads/old"); out != "" {
		t.Fatalf("atomic push created %q", out)
	}

	// Atomic success on two refs, moving to existing objects.
	rep = push([]Command{
		{Name: "refs/heads/old", New: root},
		{Name: "refs/tags/v1", New: child},
	}, nil, "atomic")
	if !rep.Refs["refs/heads/old"].OK || !rep.Refs["refs/tags/v1"].OK {
		t.Fatalf("atomic ok: %+v", rep)
	}

	// Delete, with no pack.
	rep = push([]Command{{Name: "refs/heads/old", Old: root}}, nil, "delete-refs")
	if !rep.Refs["refs/heads/old"].OK {
		t.Fatalf("delete: %+v", rep)
	}
	a, err = Advertise(ctx, nil, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Refs["refs/heads/old"]; ok || a.Refs["refs/heads/main"] != child || a.Refs["refs/tags/v1"] != child {
		t.Fatalf("refs after delete: %v", a.Refs)
	}

	// A bad pack is rejected by fsck.
	bad := gitobj.Object{Type: gitobj.TypeTree, Data: []byte("100644 .git\x00" + strings.Repeat("\x01", 20))}
	badCommit := gitobj.Object{Type: gitobj.TypeCommit, Data: []byte("tree " + bad.ID().String() + "\nauthor a <b> 1 +0000\ncommitter a <b> 1 +0000\n\nx\n")}
	rep = push([]Command{{Name: "refs/heads/bad", New: badCommit.ID()}}, []gitobj.Object{bad, badCommit})
	if rep.UnpackOK || rep.Refs["refs/heads/bad"].OK {
		t.Fatalf("fsck did not reject: %+v", rep)
	}

	// Single-commit fetch of a non-tip commit.
	data, err := FetchCommit(ctx, nil, url, root)
	if err != nil {
		t.Fatal(err)
	}
	c, err := gitobj.DecodeCommit(data)
	if err != nil {
		t.Fatal(err)
	}
	if gitobj.Hash(gitobj.TypeCommit, data) != root || c.Message != "root\n" || len(c.Parents) != 0 {
		t.Fatalf("fetched %+v", c)
	}
	if got := git("rev-parse", root.String()+"^{tree}"); got != c.Tree.String() {
		t.Fatalf("tree %s, git says %s", c.Tree, got)
	}
}
