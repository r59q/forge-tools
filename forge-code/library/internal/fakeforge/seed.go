package fakeforge

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// Paths in the Seed fixture.
const (
	PathReadme    = "README.md"
	PathMain      = "src/main.go"
	PathNested    = "docs/guide/intro.md"
	PathOldName   = "old-name.txt" // renamed to PathNewName in Fixture.Rename
	PathNewName   = "new-name.txt"
	PathExec      = "bin/run.sh"
	PathSymlink   = "link" // points to README.md
	PathSubmodule = "vendor/lib"
	PathBinary    = "assets/blob.bin" // NUL bytes and invalid UTF-8
	PathLFS       = "assets/model.bin"
	PathLarge     = "large.txt" // LargeSize bytes of text lines
	PathUnicode   = "dir with space/ünïcödé file.txt"
	PathFeature   = "feature.txt" // only on the feature line and after the merge
)

// LFSPointer is the Git LFS pointer stored at PathLFS.
const LFSPointer = "version https://git-lfs.github.com/spec/v1\n" +
	"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n" +
	"size 12345\n"

// LargeSize is the size of the file at PathLarge. It ends in a newline.
const LargeSize = 300 << 10

// Binary is the content at PathBinary.
var Binary = []byte{0x89, 'P', 'N', 'G', 0x00, 0xff, 0xfe, 0x00, 0xc3, 0x28, 0x80, '\n', 0x00, 0xed, 0xa0, 0x80}

// SubmoduleCommit is the commit the submodule at PathSubmodule points to.
// It is not stored in the repository.
var SubmoduleCommit = gitobj.Hash(gitobj.TypeCommit, []byte("fakeforge submodule"))

// Epoch is the time of the fixture's first commit. Later commits are
// whole hours after it.
var Epoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// Fixture names the objects Seed writes. Every ID is real and stable.
//
// History, oldest first (hours after Epoch in brackets):
//
//	Main[0] [0] root
//	Main[1] [1] adds exec, symlink; edits src/main.go
//	Main[2] [2] adds submodule, binary, LFS pointer   <- refs/heads/clash
//	Feature[0] [3] from Main[2], adds feature.txt
//	Main[3] [4] renames old-name.txt, adds unicode name <- refs/tags/clash
//	Feature[1] [5] edits feature.txt                  <- MergeBase
//	Main[4] [6] adds large.txt, edits README.md
//	Merge [7] merges Feature[1] into Main[4]
//	Head [8] edits docs/guide/intro.md                <- refs/heads/main
//	Feature[2] [9] edits feature.txt                  <- refs/heads/feature
type Fixture struct {
	// Main is the first-parent line of main before the merge, root first.
	Main []gitobj.ID
	// Feature is the feature branch line, oldest first.
	Feature []gitobj.ID
	// Merge merges Feature[1] into Main[4].
	Merge gitobj.ID
	// Head is the tip of main, a child of Merge.
	Head gitobj.ID
	// MergeBase is the merge base of main and feature: Feature[1].
	MergeBase gitobj.ID
	// Rename is the commit that renames PathOldName to PathNewName.
	Rename gitobj.ID

	// Tag is the annotated tag object of refs/tags/v1.0, on Main[3].
	Tag gitobj.ID
	// TagOuter and TagInner form refs/tags/chain: TagOuter -> TagInner
	// -> Main[1].
	TagOuter, TagInner gitobj.ID
	// Light is the commit of the lightweight tag refs/tags/light:
	// Main[0].
	Light gitobj.ID
	// ClashBranch and ClashTag are the commits of refs/heads/clash and
	// refs/tags/clash.
	ClashBranch, ClashTag gitobj.ID

	// Blobs maps every path at Head to its entry ID; the submodule maps
	// to SubmoduleCommit.
	Blobs map[string]gitobj.ID
}

// Seed writes the standard fixture to r, sets its refs and makes main the
// default branch.
func Seed(r *Repo) Fixture {
	at := func(h int) time.Time { return Epoch.Add(time.Duration(h) * time.Hour) }
	files := map[string]File{
		PathReadme:  {Data: []byte("# Fixture\n\nA test repository.\n")},
		PathMain:    {Data: []byte("package main\n\nfunc main() {}\n")},
		PathNested:  {Data: []byte("Intro\n")},
		PathOldName: {Data: []byte("this file moves\n")},
	}
	commit := func(h int, msg string, parents ...gitobj.ID) gitobj.ID {
		return r.CommitTree(r.BuildTree(files), at(h), msg+"\n", parents...)
	}
	var fx Fixture

	c0 := commit(0, "Initial commit")
	files[PathExec] = File{Mode: gitobj.ModeExec, Data: []byte("#!/bin/sh\necho run\n")}
	files[PathSymlink] = File{Mode: gitobj.ModeSymlink, Data: []byte(PathReadme)}
	files[PathMain] = File{Data: []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hi\") }\n")}
	c1 := commit(1, "Add run script and link", c0)
	files[PathSubmodule] = File{Mode: gitobj.ModeSubmodule, Submodule: SubmoduleCommit}
	files[PathBinary] = File{Data: Binary}
	files[PathLFS] = File{Data: []byte(LFSPointer)}
	c2 := commit(2, "Add submodule and assets", c1)

	// The feature line forks at c2; it has its own file set.
	feat := maps.Clone(files)
	feat[PathFeature] = File{Data: []byte("feature v1\n")}
	f0 := r.CommitTree(r.BuildTree(feat), at(3), "Start feature\n", c2)

	files[PathNewName] = files[PathOldName]
	delete(files, PathOldName)
	files[PathUnicode] = File{Data: []byte("Grüße, 世界\n")}
	c3 := commit(4, "Rename and add unicode name", c2)

	feat[PathFeature] = File{Data: []byte("feature v2\n")}
	f1 := r.CommitTree(r.BuildTree(feat), at(5), "Improve feature\n", f0)

	files[PathLarge] = File{Data: largeText()}
	files[PathReadme] = File{Data: []byte("# Fixture\n\nA test repository with history.\n")}
	c4 := commit(6, "Add large file", c3)

	files[PathFeature] = feat[PathFeature]
	merge := commit(7, "Merge branch 'feature'", c4, f1)
	files[PathNested] = File{Data: []byte("Intro\n\nMore words.\n")}
	head := commit(8, "Expand intro", merge)

	feat[PathFeature] = File{Data: []byte("feature v3\n")}
	f2 := r.CommitTree(r.BuildTree(feat), at(9), "Feature after merge\n", f1)

	fx.Main = []gitobj.ID{c0, c1, c2, c3, c4}
	fx.Feature = []gitobj.ID{f0, f1, f2}
	fx.Merge, fx.Head, fx.MergeBase, fx.Rename = merge, head, f1, c3

	tagger := Signature(at(10))
	fx.Tag = r.WriteTag(gitobj.Tag{Object: c3, Name: "v1.0", Tagger: &tagger, Message: "Release 1.0\n"})
	fx.TagInner = r.WriteTag(gitobj.Tag{Object: c1, Name: "chain-inner", Tagger: &tagger, Message: "Inner\n"})
	fx.TagOuter = r.WriteTag(gitobj.Tag{Object: fx.TagInner, Name: "chain", Tagger: &tagger, Message: "Outer\n"})
	fx.Light = c0
	fx.ClashBranch, fx.ClashTag = c2, c3

	r.SetRef("refs/heads/main", head)
	r.SetRef("refs/heads/feature", f2)
	r.SetRef("refs/heads/clash", c2)
	r.SetRef("refs/tags/clash", c3)
	r.SetRef("refs/tags/v1.0", fx.Tag)
	r.SetRef("refs/tags/chain", fx.TagOuter)
	r.SetRef("refs/tags/light", c0)
	r.SetDefaultBranch("main")

	fx.Blobs = map[string]gitobj.ID{}
	for p := range files {
		e, err := r.Lookup(head, p)
		if err != nil {
			panic("fakeforge: seed: " + err.Error())
		}
		fx.Blobs[p] = e.ID
	}
	return fx
}

// largeText returns LargeSize bytes of numbered lines.
func largeText() []byte {
	var b strings.Builder
	for i := 0; b.Len() < LargeSize; i++ {
		fmt.Fprintf(&b, "line %06d: the quick brown fox jumps over the lazy dog\n", i)
	}
	out := []byte(b.String()[:LargeSize])
	out[len(out)-1] = '\n'
	return out
}
