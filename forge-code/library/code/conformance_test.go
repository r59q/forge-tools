package code

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/fakeforge"
	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// confForges are the fronts every conformance case runs against. The
// first one is the reference the others are compared with.
var confForges = []struct {
	kind  forgeauth.Kind
	front fakeforge.Front
	repo  string
}{
	{forgeauth.GitHub, fakeforge.GitHub, "octo/fixture"},
	{forgeauth.GitLab, fakeforge.GitLab, "group/sub/fixture"},
	{forgeauth.Forgejo, fakeforge.ForgejoAPI, "owner/fixture"},
}

// confEnv is one fake forge with the Seed fixture and an extra
// unrelated root commit at refs/heads/orphan.
type confEnv struct {
	kind   forgeauth.Kind
	s      *fakeforge.Server
	repo   *fakeforge.Repo
	fx     fakeforge.Fixture
	orphan gitobj.ID
	c      *Client
	ref    RepositoryRef
}

func newConfEnvs(t *testing.T) []*confEnv {
	t.Helper()
	var envs []*confEnv
	for _, f := range confForges {
		s := fakeforge.NewServer(t, f.kind, f.front)
		r := s.NewRepo(f.repo)
		fx := fakeforge.Seed(r)
		orphan := r.CommitTree(r.BuildTree(map[string]fakeforge.File{"x": {Data: []byte("x\n")}}),
			fakeforge.Epoch, "Orphan\n")
		r.SetRef("refs/heads/orphan", orphan)
		c, err := New(Source{ID: "src", Session: s.Session})
		if err != nil {
			t.Fatal(err)
		}
		envs = append(envs, &confEnv{kind: f.kind, s: s, repo: r, fx: fx, orphan: orphan, c: c,
			ref: RepositoryRef{Source: "src", Repository: f.repo}})
	}
	return envs
}

// confOID converts a fixture ID.
func confOID(id gitobj.ID) OID { return OID(id.String()) }

// confRead is what a ReadFile or ReadBlob case returns.
type confRead struct {
	Blob Blob
	Data []byte
}

// confHistory is every page of a history listing joined.
type confHistory struct {
	Tip     OID
	Commits []Commit
}

// confCase is one read. run returns a comparable result; want, if set,
// returns the expected result from the fixture.
type confCase struct {
	name string
	run  func(ctx context.Context, e *confEnv) (any, error)
	want func(e *confEnv) any
	err  string // expected confErr class
}

// confErr names an error's class, so errors compare across forges.
func confErr(err error) string {
	if err == nil {
		return ""
	}
	classes := []struct {
		err  error
		name string
	}{
		{ErrAmbiguous, "ambiguous"},
		{ErrNotFile, "not-file"},
		{ErrRevisionNotFound, "revision-not-found"},
		{ErrPathNotFound, "path-not-found"},
		{ErrInvalid, "invalid"},
		{ErrTooLarge, "too-large"},
		{ErrUnsupported, "unsupported"},
	}
	for _, c := range classes {
		if !errors.Is(err, c.err) {
			continue
		}
		if c.err == ErrRevisionNotFound || c.err == ErrPathNotFound {
			if !errors.Is(err, forgehttp.ErrNotFound) {
				return c.name + " without forgehttp.ErrNotFound"
			}
			var fe *forgehttp.Error
			if errors.As(err, &fe) && fe.StatusCode != http.StatusNotFound {
				return fmt.Sprintf("%s wrapping forge status %d", c.name, fe.StatusCode)
			}
		}
		return c.name
	}
	return "other: " + err.Error()
}

// confNorm returns v with times in UTC and, if gitlab is set, with the
// fields GitLab does not report cleared: commit tree IDs, tree IDs and
// tree entry sizes.
func confNorm(v any, gitlab bool) any {
	sig := func(s Signature) Signature { s.When = s.When.UTC(); return s }
	commit := func(c Commit) Commit {
		c.Author, c.Committer = sig(c.Author), sig(c.Committer)
		if gitlab {
			c.Tree = ""
		}
		return c
	}
	entry := func(e Entry) Entry {
		if gitlab {
			e.Size = -1
		}
		return e
	}
	switch v := v.(type) {
	case Commit:
		return commit(v)
	case confHistory:
		out := confHistory{Tip: v.Tip, Commits: []Commit{}}
		for _, c := range v.Commits {
			out.Commits = append(out.Commits, commit(c))
		}
		return out
	case Tree:
		if gitlab {
			v.ID = ""
		}
		v.Entries = slices.Clone(v.Entries)
		for i := range v.Entries {
			v.Entries[i] = entry(v.Entries[i])
		}
		return v
	case PathInfo:
		v.Entry = entry(v.Entry)
		return v
	}
	return v
}

// confReadAll reads and closes r.
func confReadAll(r *Reader, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return confRead{Blob: r.Blob, Data: data}, nil
}

// confAllHistory reads every page of a history listing.
func confAllHistory(ctx context.Context, e *confEnv, o HistoryOptions) (any, error) {
	out := confHistory{Commits: []Commit{}}
	size := cmp(o.PageSize, defaultPageSize)
	for range 100 {
		p, err := e.c.History(ctx, e.ref, o)
		if err != nil {
			return nil, err
		}
		if len(p.Commits) > size {
			return nil, fmt.Errorf("page of %d commits, want at most %d", len(p.Commits), size)
		}
		if len(p.Commits) == 0 && len(out.Commits) > 0 && p.Next != "" {
			return nil, errors.New("empty page in the middle")
		}
		out.Tip = p.Tip
		out.Commits = append(out.Commits, p.Commits...)
		if p.Next == "" {
			return out, nil
		}
		o = HistoryOptions{Cursor: p.Next}
	}
	return nil, errors.New("history did not end")
}

// confCommit returns the fixture's commit id as the library reports it.
func confCommit(e *confEnv, id gitobj.ID) Commit {
	c, err := e.repo.ReadCommit(id)
	if err != nil {
		panic(err)
	}
	sig := func(s gitobj.Signature) Signature {
		return Signature{Name: s.Name, Email: s.Email, When: s.When.UTC()}
	}
	out := Commit{ID: confOID(id), Tree: confOID(c.Tree), Parents: []OID{},
		Author: sig(c.Author), Committer: sig(c.Committer), Message: strings.TrimSuffix(c.Message, "\n")}
	for _, p := range c.Parents {
		out.Parents = append(out.Parents, confOID(p))
	}
	return out
}

// confLog returns the fixture's log from tip as commits.
func confLog(e *confEnv, tip gitobj.ID, path string) any {
	ids, err := e.repo.Log(tip, fakeforge.LogOptions{Path: path})
	if err != nil {
		panic(err)
	}
	out := confHistory{Tip: confOID(tip), Commits: []Commit{}}
	for _, id := range ids {
		out.Commits = append(out.Commits, confCommit(e, id))
	}
	return out
}

// confEntries lists the directory dir of commit in the store.
func confEntries(e *confEnv, commit gitobj.ID, dir string, recursive bool) []Entry {
	root, err := e.repo.Lookup(commit, dir)
	if err != nil {
		panic(err)
	}
	list, err := e.repo.ListTree(root.ID, recursive)
	if err != nil {
		panic(err)
	}
	out := []Entry{}
	for _, pe := range list {
		mode := FileMode(pe.Mode)
		path := pe.Path
		if dir != "" {
			path = dir + "/" + path
		}
		size := int64(-1)
		if typeOf(mode) == ObjectBlob {
			size = pe.Size
		}
		out = append(out, Entry{Name: pe.Name, Path: path, Mode: mode, Type: typeOf(mode), ID: confOID(pe.ID), Size: size})
	}
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// confTree returns the fixture's Tree of dir at commit.
func confTree(e *confEnv, commit gitobj.ID, dir string, recursive bool) Tree {
	root, err := e.repo.Lookup(commit, dir)
	if err != nil {
		panic(err)
	}
	return Tree{Commit: confOID(commit), ID: confOID(root.ID), Path: dir, Entries: confEntries(e, commit, dir, recursive)}
}

// confStat returns the fixture's PathInfo of path at commit.
func confStat(e *confEnv, commit gitobj.ID, path string) PathInfo {
	te, err := e.repo.Lookup(commit, path)
	if err != nil {
		panic(err)
	}
	mode := FileMode(te.Mode)
	size := int64(-1)
	if typeOf(mode) == ObjectBlob {
		data, err := e.repo.ReadBlob(te.ID)
		if err != nil {
			panic(err)
		}
		size = int64(len(data))
	}
	name := path[strings.LastIndex(path, "/")+1:]
	return PathInfo{Commit: confOID(commit), Entry: Entry{Name: name, Path: path, Mode: mode, Type: typeOf(mode), ID: confOID(te.ID), Size: size}}
}

// confFile returns the fixture's ReadFile result for path at commit.
func confFile(e *confEnv, commit gitobj.ID, path string) confRead {
	te, err := e.repo.Lookup(commit, path)
	if err != nil {
		panic(err)
	}
	data, err := e.repo.ReadBlob(te.ID)
	if err != nil {
		panic(err)
	}
	return confRead{
		Blob: Blob{ID: confOID(te.ID), Commit: confOID(commit), Path: path, Size: int64(len(data)),
			LFSPointer: path == fakeforge.PathLFS},
		Data: data,
	}
}

func confCases(fx fakeforge.Fixture) []confCase {
	var cases []confCase
	resolve := func(rev string, want gitobj.ID, ref string) {
		cases = append(cases, confCase{
			name: "Resolve/" + rev,
			run: func(ctx context.Context, e *confEnv) (any, error) {
				return e.c.Resolve(ctx, e.ref, rev)
			},
			want: func(*confEnv) any { return Resolved{Commit: confOID(want), Ref: ref} },
		})
	}
	resolveErr := func(rev, class string) {
		cases = append(cases, confCase{
			name: "Resolve/" + rev,
			run: func(ctx context.Context, e *confEnv) (any, error) {
				return e.c.Resolve(ctx, e.ref, rev)
			},
			err: class,
		})
	}
	head := fx.Head.String()
	resolve(head, fx.Head, "")
	resolve(strings.ToUpper(head), fx.Head, "")
	resolve("", fx.Head, "refs/heads/main")
	resolve("HEAD", fx.Head, "refs/heads/main")
	resolve("main", fx.Head, "refs/heads/main")
	resolve("refs/heads/main", fx.Head, "refs/heads/main")
	resolve("feature", fx.Feature[2], "refs/heads/feature")
	resolve("refs/heads/clash", fx.ClashBranch, "refs/heads/clash")
	resolve("refs/tags/clash", fx.ClashTag, "refs/tags/clash")
	resolve("v1.0", fx.Main[3], "refs/tags/v1.0")
	resolve("refs/tags/chain", fx.Main[1], "refs/tags/chain")
	resolve("light", fx.Light, "refs/tags/light")
	resolve("HEAD~2", fx.Main[4], "refs/heads/main")
	resolve("main^", fx.Merge, "refs/heads/main")
	resolve("main~1^2", fx.Feature[1], "refs/heads/main")
	resolve("main^^2~1", fx.Feature[0], "refs/heads/main")
	resolve("v1.0~3", fx.Main[0], "refs/tags/v1.0")
	resolve(head+"^0", fx.Head, "")
	resolveErr("clash", "ambiguous")
	resolveErr("clash~1", "ambiguous")
	resolveErr(head[:7], "invalid")
	resolveErr(head[:39], "invalid")
	resolveErr("main..feature", "invalid")
	resolveErr("main@{1}", "invalid")
	resolveErr("refs/pull/1/head", "invalid")
	resolveErr("-main", "invalid")
	resolveErr("nope", "revision-not-found")
	resolveErr("refs/heads/nope", "revision-not-found")
	resolveErr("refs/tags/nope", "revision-not-found")
	resolveErr("refs/heads/mai", "revision-not-found")
	resolveErr(strings.Repeat("ab", 20), "revision-not-found")
	resolveErr(string(ZeroOID), "revision-not-found")
	resolveErr(fx.Blobs[fakeforge.PathReadme].String(), "revision-not-found")
	resolveErr(fx.Tag.String(), "revision-not-found")
	resolveErr("v1.0~4", "revision-not-found")
	resolveErr("HEAD^3", "revision-not-found")

	for _, rev := range []struct {
		rev string
		id  gitobj.ID
	}{{"main", fx.Head}, {"v1.0", fx.Main[3]}, {fx.Merge.String(), fx.Merge}, {"light", fx.Main[0]}, {"refs/tags/chain", fx.Main[1]}} {
		cases = append(cases, confCase{
			name: "Commit/" + rev.rev,
			run: func(ctx context.Context, e *confEnv) (any, error) {
				return e.c.Commit(ctx, e.ref, rev.rev)
			},
			want: func(e *confEnv) any { return confCommit(e, rev.id) },
		})
	}
	cases = append(cases, confCase{
		name: "Commit/missing",
		run: func(ctx context.Context, e *confEnv) (any, error) {
			return e.c.Commit(ctx, e.ref, strings.Repeat("cd", 20))
		},
		err: "revision-not-found",
	})

	ref := func(name string, target, commit gitobj.ID) Ref {
		return Ref{Name: name, Target: confOID(target), Commit: confOID(commit)}
	}
	heads := []Ref{
		ref("refs/heads/clash", fx.ClashBranch, fx.ClashBranch),
		ref("refs/heads/feature", fx.Feature[2], fx.Feature[2]),
		ref("refs/heads/main", fx.Head, fx.Head),
		ref("refs/heads/orphan", gitobj.ZeroID, gitobj.ZeroID), // filled in want
	}
	tags := []Ref{
		ref("refs/tags/chain", fx.TagOuter, fx.Main[1]),
		ref("refs/tags/clash", fx.ClashTag, fx.ClashTag),
		ref("refs/tags/light", fx.Light, fx.Light),
		ref("refs/tags/v1.0", fx.Tag, fx.Main[3]),
	}
	refsWant := func(prefix string, limit int) func(e *confEnv) any {
		return func(e *confEnv) any {
			h := slices.Clone(heads)
			h[3] = ref("refs/heads/orphan", e.orphan, e.orphan)
			var all []Ref
			switch prefix {
			case headsPrefix:
				all = h
			case tagsPrefix:
				all = tags
			default:
				all = append(h, tags...)
			}
			list := RefList{Default: "refs/heads/main", Refs: slices.Clone(all)}
			if limit > 0 && limit < len(all) {
				list.Refs, list.Truncated = list.Refs[:limit], true
			}
			return list
		}
	}
	for _, o := range []RefsOptions{{}, {Prefix: headsPrefix}, {Prefix: tagsPrefix}, {Limit: 3}, {Limit: 6}, {Prefix: tagsPrefix, Limit: 2}, {Limit: 8}} {
		cases = append(cases, confCase{
			name: fmt.Sprintf("ListRefs/%q/%d", o.Prefix, o.Limit),
			run: func(ctx context.Context, e *confEnv) (any, error) {
				return e.c.ListRefs(ctx, e.ref, o)
			},
			want: refsWant(o.Prefix, o.Limit),
		})
	}

	history := func(o HistoryOptions, tip gitobj.ID) {
		cases = append(cases, confCase{
			name: fmt.Sprintf("History/%s/%q/%d", o.Revision, o.Path, o.PageSize),
			run: func(ctx context.Context, e *confEnv) (any, error) {
				return confAllHistory(ctx, e, o)
			},
			want: func(e *confEnv) any { return confLog(e, tip, o.Path) },
		})
	}
	history(HistoryOptions{Revision: "main"}, fx.Head)
	history(HistoryOptions{Revision: "main", PageSize: 1}, fx.Head)
	history(HistoryOptions{Revision: "main", PageSize: 3}, fx.Head)
	history(HistoryOptions{Revision: "main", PageSize: 5}, fx.Head)
	history(HistoryOptions{Revision: "feature", PageSize: 2}, fx.Feature[2])
	history(HistoryOptions{Revision: "v1.0", PageSize: 2}, fx.Main[3])
	history(HistoryOptions{Revision: "main", Path: fakeforge.PathMain, PageSize: 1}, fx.Head)
	history(HistoryOptions{Revision: "main", Path: fakeforge.PathFeature}, fx.Head)
	history(HistoryOptions{Revision: "main", Path: fakeforge.PathReadme, PageSize: 2}, fx.Head)
	history(HistoryOptions{Revision: "main", Path: "docs"}, fx.Head)
	history(HistoryOptions{Revision: "main", Path: fakeforge.PathOldName}, fx.Head)
	history(HistoryOptions{Revision: "main", Path: fakeforge.PathUnicode}, fx.Head)
	history(HistoryOptions{Revision: "main", Path: "nope"}, fx.Head)
	cases = append(cases, confCase{
		name: "History/missing",
		run: func(ctx context.Context, e *confEnv) (any, error) {
			return confAllHistory(ctx, e, HistoryOptions{Revision: "nope"})
		},
		err: "revision-not-found",
	})

	tree := func(rev string, commit gitobj.ID, dir string, recursive bool) {
		cases = append(cases, confCase{
			name: fmt.Sprintf("Tree/%s/%q/%v", rev, dir, recursive),
			run: func(ctx context.Context, e *confEnv) (any, error) {
				return e.c.Tree(ctx, e.ref, rev, TreeOptions{Path: dir, Recursive: recursive})
			},
			want: func(e *confEnv) any { return confTree(e, commit, dir, recursive) },
		})
	}
	tree("main", fx.Head, "", false)
	tree("main", fx.Head, "", true)
	tree("main", fx.Head, "docs", false)
	tree("main", fx.Head, "docs", true)
	tree("main", fx.Head, "docs/guide", false)
	tree("main", fx.Head, "dir with space", false)
	tree("light", fx.Main[0], "", true)
	tree("feature", fx.Feature[2], "", false)
	// Which entries a truncated listing keeps follows the forge's listing
	// order, so only the count and membership are compared.
	for _, max := range []int{1, 5, 17, 18} {
		cases = append(cases, confCase{
			name: fmt.Sprintf("Tree/main/recursive/max%d", max),
			run: func(ctx context.Context, e *confEnv) (any, error) {
				t, err := e.c.Tree(ctx, e.ref, "main", TreeOptions{Recursive: true, MaxEntries: max})
				if err != nil {
					return nil, err
				}
				gitlab := e.kind == forgeauth.GitLab
				all := confNorm(confTree(e, fx.Head, "", true), gitlab).(Tree).Entries
				for _, en := range confNorm(t, gitlab).(Tree).Entries {
					if !slices.Contains(all, en) {
						return nil, fmt.Errorf("entry %+v is not in the tree", en)
					}
				}
				return struct {
					Commit    OID
					Entries   int
					Truncated bool
				}{t.Commit, len(t.Entries), t.Truncated}, nil
			},
			want: func(e *confEnv) any {
				n := len(confEntries(e, fx.Head, "", true))
				return struct {
					Commit    OID
					Entries   int
					Truncated bool
				}{confOID(fx.Head), min(n, max), n > max}
			},
		})
	}
	for _, dir := range []string{"nope", fakeforge.PathReadme, fakeforge.PathSubmodule, fakeforge.PathSymlink, "docs/nope"} {
		cases = append(cases, confCase{
			name: fmt.Sprintf("Tree/main/%q", dir),
			run: func(ctx context.Context, e *confEnv) (any, error) {
				return e.c.Tree(ctx, e.ref, "main", TreeOptions{Path: dir})
			},
			err: "path-not-found",
		})
	}

	paths := []string{}
	for p := range fx.Blobs {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for _, p := range append(slices.Clone(paths), "docs", "docs/guide", "vendor", "dir with space") {
		cases = append(cases, confCase{
			name: "Stat/" + p,
			run: func(ctx context.Context, e *confEnv) (any, error) {
				return e.c.Stat(ctx, e.ref, "main", p)
			},
			want: func(e *confEnv) any { return confStat(e, fx.Head, p) },
		})
	}
	cases = append(cases, confCase{
		name: "Stat/old",
		run: func(ctx context.Context, e *confEnv) (any, error) {
			return e.c.Stat(ctx, e.ref, "v1.0~1", fakeforge.PathOldName)
		},
		want: func(e *confEnv) any { return confStat(e, fx.Main[2], fakeforge.PathOldName) },
	})
	for _, p := range []string{"nope", fakeforge.PathOldName, "README.md/x", "docs/nope", fakeforge.PathSubmodule + "/x"} {
		cases = append(cases, confCase{
			name: "Stat/" + p,
			run: func(ctx context.Context, e *confEnv) (any, error) {
				return e.c.Stat(ctx, e.ref, "main", p)
			},
			err: "path-not-found",
		})
	}

	for _, p := range paths {
		if p == fakeforge.PathSubmodule {
			continue
		}
		cases = append(cases, confCase{
			name: "ReadFile/" + p,
			run: func(ctx context.Context, e *confEnv) (any, error) {
				return confReadAll(e.c.ReadFile(ctx, e.ref, "main", p))
			},
			want: func(e *confEnv) any { return confFile(e, fx.Head, p) },
		})
		cases = append(cases, confCase{
			name: "ReadBlob/" + p,
			run: func(ctx context.Context, e *confEnv) (any, error) {
				return confReadAll(e.c.ReadBlob(ctx, e.ref, confOID(fx.Blobs[p])))
			},
			want: func(e *confEnv) any {
				r := confFile(e, fx.Head, p)
				r.Blob.Commit, r.Blob.Path = "", ""
				return r
			},
		})
	}
	cases = append(cases, confCase{
		name: "ReadFile/old",
		run: func(ctx context.Context, e *confEnv) (any, error) {
			return confReadAll(e.c.ReadFile(ctx, e.ref, fx.Main[0].String(), fakeforge.PathOldName))
		},
		want: func(e *confEnv) any { return confFile(e, fx.Main[0], fakeforge.PathOldName) },
	})
	for _, p := range []struct{ path, err string }{
		{"docs", "not-file"},
		{"docs/guide", "not-file"},
		{fakeforge.PathSubmodule, "not-file"},
		{"nope", "path-not-found"},
		{fakeforge.PathOldName, "path-not-found"},
		{"docs/nope", "path-not-found"},
		{"README.md/x", "path-not-found"},
	} {
		cases = append(cases, confCase{
			name: "ReadFile/" + p.path,
			run: func(ctx context.Context, e *confEnv) (any, error) {
				return confReadAll(e.c.ReadFile(ctx, e.ref, "main", p.path))
			},
			err: p.err,
		})
	}
	cases = append(cases, confCase{
		name: "ReadBlob/missing",
		run: func(ctx context.Context, e *confEnv) (any, error) {
			return confReadAll(e.c.ReadBlob(ctx, e.ref, OID(strings.Repeat("ef", 20))))
		},
		err: "revision-not-found",
	})

	mergeBase := func(a, b string, want gitobj.ID) {
		cases = append(cases, confCase{
			name: "MergeBase/" + a + "/" + b,
			run: func(ctx context.Context, e *confEnv) (any, error) {
				return e.c.MergeBase(ctx, e.ref, a, b)
			},
			want: func(*confEnv) any { return confOID(want) },
		})
	}
	mergeBase("main", "feature", fx.MergeBase)
	mergeBase("feature", "main", fx.MergeBase)
	mergeBase("refs/heads/feature", "v1.0", fx.Main[2])
	mergeBase(fx.Main[1].String(), "main", fx.Main[1])
	mergeBase("main", "light", fx.Main[0])
	mergeBase("main", "HEAD", fx.Head)
	mergeBase("main~1^2", "feature", fx.Feature[1])
	cases = append(cases, confCase{
		name: "MergeBase/orphan",
		run: func(ctx context.Context, e *confEnv) (any, error) {
			return e.c.MergeBase(ctx, e.ref, "main", "orphan")
		},
		err: "invalid",
	})
	cases = append(cases, confCase{
		name: "MergeBase/missing",
		run: func(ctx context.Context, e *confEnv) (any, error) {
			return e.c.MergeBase(ctx, e.ref, "main", "nope")
		},
		err: "revision-not-found",
	})
	return cases
}

// confGap adjusts a forge's result for its documented gaps, after
// checking the gap looks as documented.
func confGap(t *testing.T, e *confEnv, name string, got any) any {
	if e.kind == forgeauth.Forgejo && strings.HasPrefix(name, "ListRefs/") {
		// Forgejo's /tags peels one level only, so a tag on a tag with no
		// ref of its own reports the inner tag object as its commit.
		list := got.(RefList)
		list.Refs = slices.Clone(list.Refs)
		for i, r := range list.Refs {
			if r.Name == "refs/tags/chain" {
				if r.Commit != confOID(e.fx.TagInner) {
					t.Errorf("%s: forgejo chain commit = %s, want the documented gap %s", name, r.Commit, e.fx.TagInner)
				}
				list.Refs[i].Commit = confOID(e.fx.Main[1])
			}
		}
		return list
	}
	return got
}

// TestConformance runs the same reads on every forge and checks that
// they agree with each other and with the fixture, apart from the
// documented gaps (GitLab tree IDs and entry sizes, Forgejo nested tag
// peeling in ListRefs).
func TestConformance(t *testing.T) {
	envs := newConfEnvs(t)
	ctx := context.Background()
	for _, tc := range confCases(envs[0].fx) {
		t.Run(tc.name, func(t *testing.T) {
			var ref any
			for i, e := range envs {
				got, err := tc.run(ctx, e)
				if class := confErr(err); class != tc.err {
					t.Errorf("%s: error %q (%v), want %q", e.kind, class, err, tc.err)
					continue
				}
				if err != nil {
					continue
				}
				got = confNorm(confGap(t, e, tc.name, got), false)
				gitlab := e.kind == forgeauth.GitLab
				if tc.want != nil {
					if want := confNorm(tc.want(e), gitlab); !reflect.DeepEqual(confNorm(got, gitlab), want) {
						t.Errorf("%s: got\n%+v\nwant\n%+v", e.kind, got, want)
					}
				}
				if i == 0 {
					ref = got
				} else if !reflect.DeepEqual(confNorm(got, gitlab), confNorm(ref, gitlab)) {
					t.Errorf("%s differs from %s: got\n%+v\nwant\n%+v", e.kind, envs[0].kind, got, ref)
				}
			}
		})
	}
}

func TestConformanceMessage(t *testing.T) {
	// GitHub strips a message's trailing newline, so every forge does;
	// a message without one comes back unchanged.
	for _, e := range newConfEnvs(t) {
		tree := e.repo.BuildTree(map[string]fakeforge.File{"m": {Data: []byte("m\n")}})
		for msg, want := range map[string]string{
			"Subject\n\nBody.\n": "Subject\n\nBody.",
			"No newline":         "No newline",
		} {
			id := e.repo.CommitTree(tree, fakeforge.Epoch, msg, e.fx.Head)
			cm, err := e.c.Commit(context.Background(), e.ref, id.String())
			if err != nil || cm.Message != want {
				t.Errorf("%s: message %q = %q, %v, want %q", e.kind, msg, cm.Message, err, want)
			}
		}
	}
}

func TestConformanceCapabilities(t *testing.T) {
	for _, e := range newConfEnvs(t) {
		caps, err := e.c.Capabilities("src")
		if err != nil {
			t.Fatal(err)
		}
		want := Capabilities{TreeIDs: e.kind != forgeauth.GitLab, MergeBase: true}
		if caps != want {
			t.Errorf("%s: Capabilities = %+v, want %+v", e.kind, caps, want)
		}
	}
}

// snapFronts are the fronts for the snapshot tests, set so that a tree
// listing of the big repository takes several requests on each forge.
var snapFronts = map[forgeauth.Kind]fakeforge.Front{
	forgeauth.GitHub:  fakeforge.GitHubFront(fakeforge.GitHubOptions{TreeLimit: 50}),
	forgeauth.GitLab:  fakeforge.GitLab,
	forgeauth.Forgejo: fakeforge.ForgejoAPIWith(fakeforge.ForgejoOptions{TreesPerPage: 40}),
}

// snapEnv is a forge with the Seed fixture at snap/fixture and a
// repository with 255 entries at snap/big.
type snapEnv struct {
	kind  forgeauth.Kind
	s     *fakeforge.Server
	repo  *fakeforge.Repo
	fx    fakeforge.Fixture
	big   *fakeforge.Repo
	tip   gitobj.ID // main of big
	other gitobj.ID // another commit of big
	c     *Client
}

func newSnapEnvs(t *testing.T) []*snapEnv {
	t.Helper()
	var envs []*snapEnv
	for _, f := range confForges {
		s := fakeforge.NewServer(t, f.kind, snapFronts[f.kind])
		r := s.NewRepo("snap/fixture")
		fx := fakeforge.Seed(r)
		big := s.NewRepo("snap/big")
		files := map[string]fakeforge.File{}
		for d := range 5 {
			for i := range 50 {
				files[fmt.Sprintf("d%d/f%02d.txt", d, i)] = fakeforge.File{Data: fmt.Appendf(nil, "%d %d\n", d, i)}
			}
		}
		root := big.CommitTree(big.BuildTree(files), fakeforge.Epoch, "Big\n")
		tip := big.CommitTree(big.BuildTree(files), fakeforge.Epoch.Add(time.Hour), "Same tree\n", root)
		other := big.CommitTree(big.BuildTree(map[string]fakeforge.File{"other": {Data: []byte("o\n")}}),
			fakeforge.Epoch.Add(2*time.Hour), "Other\n", tip)
		big.SetRef("refs/heads/main", tip)
		big.SetRef("refs/heads/keep", root) // so deleting main leaves the repository non-empty
		c, err := New(Source{ID: "src", Session: s.Session})
		if err != nil {
			t.Fatal(err)
		}
		envs = append(envs, &snapEnv{kind: f.kind, s: s, repo: r, fx: fx, big: big, tip: tip, other: other, c: c})
	}
	return envs
}

// moveAfter sets a hook that, from the request after the first one on,
// points name in r at the next of the targets on each request, ending
// with the ref deleted. It returns the number of requests seen.
func moveAfter(s *fakeforge.Server, r *fakeforge.Repo, name string, targets ...gitobj.ID) *atomic.Int64 {
	var n atomic.Int64
	targets = append(targets, gitobj.ZeroID)
	s.SetHook(func(*http.Request) {
		k := int(n.Add(1)) - 2
		if k >= 0 {
			r.SetRef(name, targets[min(k, len(targets)-1)])
		}
	})
	return &n
}

// TestSnapshotTree moves and then deletes the branch while a recursive
// listing pages through the forge; the result stays on the first tip.
func TestSnapshotTree(t *testing.T) {
	ctx := context.Background()
	for _, e := range newSnapEnvs(t) {
		t.Run(string(e.kind), func(t *testing.T) {
			ce := &confEnv{repo: e.big}
			want := confTree(ce, e.tip, "", true)
			n := moveAfter(e.s, e.big, "refs/heads/main", e.other)
			got, err := e.c.Tree(ctx, RepositoryRef{Source: "src", Repository: "snap/big"}, "refs/heads/main",
				TreeOptions{Recursive: true})
			if err != nil {
				t.Fatal(err)
			}
			if n.Load() < 4 {
				t.Fatalf("listing took %d requests, want several", n.Load())
			}
			gitlab := e.kind == forgeauth.GitLab
			if !reflect.DeepEqual(confNorm(got, gitlab), confNorm(want, gitlab)) {
				t.Errorf("Tree = %d entries at %s, want %d at %s", len(got.Entries), got.Commit, len(want.Entries), want.Commit)
			}
			if _, ok := e.big.Ref("refs/heads/main"); ok {
				t.Error("hook did not delete the branch")
			}
		})
	}
}

// TestSnapshotReads moves the branch after the revision is resolved; each
// read still returns data from the resolved commit.
func TestSnapshotReads(t *testing.T) {
	ctx := context.Background()
	for _, e := range newSnapEnvs(t) {
		t.Run(string(e.kind), func(t *testing.T) {
			ref := RepositoryRef{Source: "src", Repository: "snap/fixture"}
			ce := &confEnv{repo: e.repo, fx: e.fx}
			reset := func() {
				e.s.SetHook(nil)
				e.repo.SetRef("refs/heads/main", e.fx.Head)
				moveAfter(e.s, e.repo, "refs/heads/main", e.fx.Main[0], e.fx.Feature[2])
			}
			gitlab := e.kind == forgeauth.GitLab

			reset()
			st, err := e.c.Stat(ctx, ref, "refs/heads/main", fakeforge.PathNested)
			if err != nil {
				t.Fatal(err)
			}
			if want := confStat(ce, e.fx.Head, fakeforge.PathNested); !reflect.DeepEqual(confNorm(st, gitlab), confNorm(want, gitlab)) {
				t.Errorf("Stat = %+v, want %+v", st, want)
			}

			reset()
			rd, err := confReadAll(e.c.ReadFile(ctx, ref, "refs/heads/main", fakeforge.PathReadme))
			if err != nil {
				t.Fatal(err)
			}
			if want := confFile(ce, e.fx.Head, fakeforge.PathReadme); !reflect.DeepEqual(rd, want) {
				t.Errorf("ReadFile = %+v, want %+v", rd, want)
			}

			reset()
			tr, err := e.c.Tree(ctx, ref, "refs/heads/main", TreeOptions{Path: "docs/guide"})
			if err != nil {
				t.Fatal(err)
			}
			if want := confTree(ce, e.fx.Head, "docs/guide", false); !reflect.DeepEqual(confNorm(tr, gitlab), confNorm(want, gitlab)) {
				t.Errorf("Tree = %+v, want %+v", tr, want)
			}

			reset()
			cm, err := e.c.Commit(ctx, ref, "refs/heads/main~1")
			if err != nil {
				t.Fatal(err)
			}
			if cm.ID != confOID(e.fx.Merge) {
				t.Errorf("Commit(main~1) = %s, want %s", cm.ID, e.fx.Merge)
			}

			reset()
			mb, err := e.c.MergeBase(ctx, ref, "refs/heads/main", "refs/heads/feature")
			if err != nil {
				t.Fatal(err)
			}
			if mb != confOID(e.fx.MergeBase) && mb != confOID(e.fx.Main[0]) {
				t.Errorf("MergeBase = %s", mb)
			}
			e.s.SetHook(nil)
		})
	}
}

// TestSnapshotHistory moves the branch between and during pages; paging
// continues from the first page's tip.
func TestSnapshotHistory(t *testing.T) {
	ctx := context.Background()
	for _, e := range newSnapEnvs(t) {
		t.Run(string(e.kind), func(t *testing.T) {
			ref := RepositoryRef{Source: "src", Repository: "snap/fixture"}
			ce := &confEnv{repo: e.repo, fx: e.fx}
			for _, path := range []string{"", fakeforge.PathFeature} {
				e.s.SetHook(nil)
				e.repo.SetRef("refs/heads/main", e.fx.Head)
				o := HistoryOptions{Revision: "main", Path: path, PageSize: 2}
				got := confHistory{Commits: []Commit{}}
				for page := 0; ; page++ {
					p, err := e.c.History(ctx, ref, o)
					if err != nil {
						t.Fatal(err)
					}
					if page == 0 {
						// Move main on every later request, then delete it.
						moveAfter(e.s, e.repo, "refs/heads/main", e.fx.Feature[2], e.fx.Main[1])
						e.repo.SetRef("refs/heads/main", e.fx.Main[0])
					}
					got.Tip = p.Tip
					got.Commits = append(got.Commits, p.Commits...)
					if p.Next == "" {
						break
					}
					o = HistoryOptions{Cursor: p.Next}
				}
				want := confLog(ce, e.fx.Head, path)
				if !reflect.DeepEqual(confNorm(got, true), confNorm(want, true)) {
					t.Errorf("path %q: history = %v, want %v", path, confIDs(got), confIDs(want.(confHistory)))
				}
			}
			e.s.SetHook(nil)
		})
	}
}

// confIDs returns the commit IDs of h.
func confIDs(h confHistory) []OID {
	var out []OID
	for _, c := range h.Commits {
		out = append(out, c.ID)
	}
	return out
}
