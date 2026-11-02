package fakeforge

import (
	"bytes"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// gitRepo exports r into a new bare repository and returns a func that
// runs git in it. IDs from `git hash-object` must match the store's. It
// skips the test when git is missing.
func gitRepo(t *testing.T, r *Repo) func(args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(stdin []byte, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		cmd.Stdin = bytes.NewReader(stdin)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
		}
		return string(out)
	}
	run(nil, "init", "-q", "--bare", "--object-format=sha1", ".")
	for _, id := range r.IDs() {
		o, _ := r.Object(id)
		got := strings.TrimSpace(run(o.Data, "hash-object", "-w", "--stdin", "-t", string(o.Type)))
		if got != id.String() {
			t.Fatalf("git hash-object %s = %s, store has %s", o.Type, got, id)
		}
	}
	for _, ref := range r.Refs("refs/") {
		run(nil, "update-ref", ref.Name, ref.ID.String())
	}
	return func(args ...string) string { return run(nil, args...) }
}

func ids(out string) []gitobj.ID {
	var res []gitobj.ID
	for _, f := range strings.Fields(out) {
		id, err := gitobj.ParseID(f)
		if err != nil {
			panic(err)
		}
		res = append(res, id)
	}
	return res
}

func TestSeedMatchesGit(t *testing.T) {
	r, fx := seeded(t)
	git := gitRepo(t, r)
	git("fsck", "--strict", "--no-dangling")

	head := fx.Head.String()
	for _, tc := range []struct {
		tip  string
		args []string
		o    LogOptions
	}{
		{tip: head},
		{tip: fx.Feature[2].String()},
		{tip: head, args: []string{"--", PathFeature}, o: LogOptions{Path: PathFeature}},
		{tip: head, args: []string{"--", PathReadme}, o: LogOptions{Path: PathReadme}},
		{tip: head, args: []string{"--", PathOldName}, o: LogOptions{Path: PathOldName}},
		{tip: head, args: []string{"--", PathNewName}, o: LogOptions{Path: PathNewName}},
		{tip: head, args: []string{"--", "docs"}, o: LogOptions{Path: "docs"}},
		{tip: head, args: []string{"--", "assets"}, o: LogOptions{Path: "assets"}},
		{tip: head, args: []string{"--", "nope"}, o: LogOptions{Path: "nope"}},
		{tip: head, args: []string{"^" + fx.Feature[2].String()}, o: LogOptions{Not: []gitobj.ID{fx.Feature[2]}}},
		{tip: head, args: []string{"^" + fx.Main[4].String(), "--", PathFeature}, o: LogOptions{Path: PathFeature, Not: []gitobj.ID{fx.Main[4]}}},
		{tip: fx.Feature[2].String(), args: []string{"^" + head, "--", PathFeature}, o: LogOptions{Path: PathFeature, Not: []gitobj.ID{fx.Head}}},
	} {
		want := ids(git(append([]string{"log", "--format=%H", tc.tip}, tc.args...)...))
		tip, _ := gitobj.ParseID(tc.tip)
		got, err := r.Log(tip, tc.o)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("log %s %v:\n got %v, %v\nwant %v", tc.tip[:7], tc.args, got, err, want)
		}
	}

	if want := ids(git("merge-base", "--all", "main", "feature")); !slices.Equal(want, []gitobj.ID{fx.MergeBase}) {
		t.Errorf("git merge-base = %v", want)
	}
	for ref, want := range map[string]gitobj.ID{"chain": fx.Main[1], "v1.0": fx.Main[3]} {
		if got := ids(git("rev-parse", "refs/tags/"+ref+"^{commit}")); got[0] != want {
			t.Errorf("%s peels to %s in git", ref, got[0])
		}
	}

	// ls-tree -r -t -l -z: "mode type id size\tpath\0".
	var want []string
	for _, rec := range strings.Split(strings.TrimSuffix(git("ls-tree", "-r", "-t", "-l", "-z", "main"), "\x00"), "\x00") {
		meta, path, _ := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		want = append(want, fmt.Sprintf("%s %s %s %s", strings.TrimLeft(f[0], "0"), f[2], f[3], path))
	}
	list, err := r.ListTree(fx.Head, true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range list {
		size := "-"
		if e.Mode != gitobj.ModeTree && e.Mode != gitobj.ModeSubmodule {
			size = fmt.Sprint(e.Size)
		}
		got = append(got, fmt.Sprintf("%s %s %s %s", e.Mode, e.ID, size, e.Path))
	}
	if !slices.Equal(got, want) {
		t.Errorf("ListTree:\n got %q\nwant %q", got, want)
	}
}

func TestCrissCrossMatchesGit(t *testing.T) {
	r := NewRepo()
	x, y, _, _, _ := crissCross(r)
	r.SetRef("refs/heads/x", x)
	r.SetRef("refs/heads/y", y)
	git := gitRepo(t, r)
	want := ids(git("merge-base", "--all", "x", "y"))
	got, _ := r.MergeBases(x, y)
	slices.SortFunc(want, func(a, b gitobj.ID) int { return bytes.Compare(a[:], b[:]) })
	slices.SortFunc(got, func(a, b gitobj.ID) int { return bytes.Compare(a[:], b[:]) })
	if !slices.Equal(got, want) {
		t.Errorf("MergeBases = %v, git %v", got, want)
	}
	for _, tip := range []gitobj.ID{x, y} {
		want := ids(git("log", "--format=%H", tip.String(), "--", "f"))
		got, _ := r.Log(tip, LogOptions{Path: "f"})
		if !slices.Equal(got, want) {
			t.Errorf("log %s -- f = %v, git %v", tip, got, want)
		}
	}
}

func TestTiesMatchGit(t *testing.T) {
	r := NewRepo()
	tree := r.BuildTree(nil)
	root := r.CommitTree(tree, Epoch, "root")
	a := r.CommitTree(tree, Epoch, "a", root)
	b := r.CommitTree(tree, Epoch, "b", root)
	c := r.CommitTree(tree, Epoch, "c", b)
	m := r.CommitTree(tree, Epoch, "m", a, c)
	r.SetRef("refs/heads/m", m)
	git := gitRepo(t, r)
	want := ids(git("log", "--format=%H", "m"))
	if got, _ := r.Log(m, LogOptions{}); !slices.Equal(got, want) {
		t.Errorf("Log = %v, git %v", got, want)
	}
}
