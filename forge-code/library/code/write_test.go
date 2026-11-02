package code

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/fakeforge"
	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-code/library/internal/gitwire"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// writeEnv is one fake forge with the Seed fixture, its REST front and
// the Git front.
type writeEnv struct {
	kind forgeauth.Kind
	s    *fakeforge.Server
	repo *fakeforge.Repo
	fx   fakeforge.Fixture
	c    *Client
	ref  RepositoryRef
	path string
}

// forEachForge runs fn on a fresh writeEnv per forge kind.
func forEachForge(t *testing.T, opts fakeforge.GitOptions, fn func(t *testing.T, e *writeEnv)) {
	for _, f := range confForges {
		t.Run(string(f.kind), func(t *testing.T) {
			fn(t, newWriteEnv(t, f.kind, f.front, f.repo, opts, Limits{}))
		})
	}
}

func newWriteEnv(t *testing.T, kind forgeauth.Kind, front fakeforge.Front, path string, opts fakeforge.GitOptions, l Limits) *writeEnv {
	t.Helper()
	s := fakeforge.NewServer(t, kind, front)
	s.Mount(fakeforge.GitPrefix, fakeforge.GitFront(opts))
	r := s.NewRepo(path)
	fx := fakeforge.Seed(r)
	git, err := s.GitRemote()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewWithLimits(l, Source{ID: "src", Session: s.Session, Git: git})
	if err != nil {
		t.Fatal(err)
	}
	return &writeEnv{kind: kind, s: s, repo: r, fx: fx, c: c, path: path,
		ref: RepositoryRef{Source: "src", Repository: path}}
}

// remount replaces the Git front's options.
func (e *writeEnv) remount(opts fakeforge.GitOptions) {
	e.s.Mount(fakeforge.GitPrefix, fakeforge.GitFront(opts))
}

// at returns the value of a ref, zero if absent.
func (e *writeEnv) at(name string) gitobj.ID {
	id, _ := e.repo.Ref(name)
	return id
}

// pushes counts receive-pack POSTs so far.
func (e *writeEnv) pushes() int { return e.s.Calls(http.MethodPost, "*/git-receive-pack") }

// child stores a commit on top of parent with parent's tree.
func (e *writeEnv) child(t *testing.T, parent gitobj.ID, msg string) gitobj.ID {
	t.Helper()
	pc, err := e.repo.ReadCommit(parent)
	if err != nil {
		t.Fatal(err)
	}
	return e.repo.CommitTree(pc.Tree, fakeforge.Epoch.Add(24*time.Hour), msg+"\n", parent)
}

// wantErr fails unless err wraps want; a nil want means no error.
func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if want == nil && err != nil || want != nil && !errors.Is(err, want) {
		t.Errorf("%s: err = %v, want %v", what, err, want)
	}
}

// wantMsg is wantErr that also wants msg in the error text.
func wantMsg(t *testing.T, what string, err, want error, msg string) {
	t.Helper()
	if !errors.Is(err, want) || !strings.Contains(fmt.Sprint(err), msg) {
		t.Errorf("%s: err = %v, want %v with %q", what, err, want, msg)
	}
}

const mainRef = "refs/heads/main"

func TestUpdateRefsRace(t *testing.T) {
	forEachForge(t, fakeforge.GitOptions{}, func(t *testing.T, e *writeEnv) {
		const n = 12
		news := make([]gitobj.ID, n)
		for i := range news {
			news[i] = e.child(t, e.fx.Head, fmt.Sprintf("race %d", i))
		}
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				errs[i] = e.c.UpdateRefs(context.Background(), e.ref,
					RefUpdate{Name: mainRef, Old: confOID(e.fx.Head), New: confOID(news[i])})
			})
		}
		wg.Wait()
		winner := -1
		for i, err := range errs {
			switch {
			case err == nil && winner >= 0:
				t.Errorf("updates %d and %d both won", winner, i)
			case err == nil:
				winner = i
			case !errors.Is(err, ErrStale):
				t.Errorf("update %d: %v, want ErrStale", i, err)
			}
		}
		if winner < 0 || e.at(mainRef) != news[winner] {
			t.Errorf("winner %d, main at %s", winner, e.at(mainRef))
		}
	})
}

func TestUpdateRefsCAS(t *testing.T) {
	forEachForge(t, fakeforge.GitOptions{}, func(t *testing.T, e *writeEnv) {
		ctx := context.Background()
		head, main4 := confOID(e.fx.Head), confOID(e.fx.Main[4])
		up := func(u ...RefUpdate) error { return e.c.UpdateRefs(ctx, e.ref, u...) }

		wantErr(t, "create if absent", up(RefUpdate{Name: "refs/heads/new", Old: ZeroOID, New: head}), nil)
		if e.at("refs/heads/new") != e.fx.Head {
			t.Error("ref not created")
		}
		// Pre-check failures send nothing.
		before := e.pushes()
		wantErr(t, "create on existing", up(RefUpdate{Name: "refs/heads/new", Old: ZeroOID, New: main4}), ErrStale)
		wantErr(t, "stale move", up(RefUpdate{Name: mainRef, Old: main4, New: head}), ErrStale)
		wantErr(t, "stale delete", up(RefUpdate{Name: "refs/heads/feature", Old: head, New: ZeroOID}), ErrStale)
		wantErr(t, "delete absent", up(RefUpdate{Name: "refs/heads/nope", Old: head, New: ZeroOID}), ErrStale)
		if got := e.pushes() - before; got != 0 {
			t.Errorf("pre-check failures sent %d pushes", got)
		}
		if e.at("refs/heads/new") != e.fx.Head || e.at(mainRef) != e.fx.Head || e.at("refs/heads/feature") != e.fx.Feature[2] {
			t.Error("a stale update changed a ref")
		}

		wantErr(t, "delete", up(RefUpdate{Name: "refs/heads/feature", Old: confOID(e.fx.Feature[2]), New: ZeroOID}), nil)
		if !e.at("refs/heads/feature").IsZero() {
			t.Error("ref not deleted")
		}
		wantErr(t, "annotated tag", up(RefUpdate{Name: "refs/tags/v2", Old: ZeroOID, New: confOID(e.fx.Tag)}), nil)
		wantErr(t, "atomic pair", up(
			RefUpdate{Name: mainRef, Old: head, New: main4},
			RefUpdate{Name: "refs/heads/clash", Old: confOID(e.fx.Main[2]), New: head},
			RefUpdate{Name: "refs/heads/new", Old: head, New: ZeroOID},
		), nil)
		if e.at(mainRef) != e.fx.Main[4] || e.at("refs/heads/clash") != e.fx.Head || !e.at("refs/heads/new").IsZero() {
			t.Error("atomic pair not applied")
		}
		missing := OID(gitobj.Hash(gitobj.TypeCommit, []byte("missing")).String())
		wantErr(t, "missing object", up(RefUpdate{Name: "refs/heads/m", Old: ZeroOID, New: missing}), ErrRejected)
		wantErr(t, "sha256", up(RefUpdate{Name: "refs/heads/m", Old: ZeroOID, New: OID(strings.Repeat("a", 64))}), ErrUnsupported)
	})
}

func TestUpdateRefsMovedBeforePush(t *testing.T) {
	forEachForge(t, fakeforge.GitOptions{}, func(t *testing.T, e *writeEnv) {
		ctx := context.Background()
		head, main4 := confOID(e.fx.Head), confOID(e.fx.Main[4])
		// The ref moves between the advertisement and the POST.
		e.remount(fakeforge.GitOptions{BeforeReceive: func(r *fakeforge.Repo, _ []gitwire.Command) {
			r.SetRef(mainRef, e.fx.Main[0])
		}})
		err := e.c.UpdateRefs(ctx, e.ref, RefUpdate{Name: mainRef, Old: head, New: main4})
		wantErr(t, "moved by hook", err, ErrStale)
		if e.pushes() != 1 || e.at(mainRef) != e.fx.Main[0] {
			t.Errorf("pushes %d, main at %s", e.pushes(), e.at(mainRef))
		}

		// Atomic rollback: feature moves, so main must not move either.
		e.repo.SetRef(mainRef, e.fx.Head)
		e.remount(fakeforge.GitOptions{BeforeReceive: func(r *fakeforge.Repo, _ []gitwire.Command) {
			r.SetRef("refs/heads/feature", e.fx.Main[0])
		}})
		err = e.c.UpdateRefs(ctx, e.ref,
			RefUpdate{Name: mainRef, Old: head, New: main4},
			RefUpdate{Name: "refs/heads/feature", Old: confOID(e.fx.Feature[2]), New: head})
		wantErr(t, "atomic rollback", err, ErrStale)
		if e.at(mainRef) != e.fx.Head {
			t.Error("atomic push applied in part")
		}

		// The forge-wide hook works the same way.
		e.repo.SetRef("refs/heads/feature", e.fx.Feature[2])
		e.remount(fakeforge.GitOptions{})
		e.s.SetHook(func(r *http.Request) {
			if r.Method == http.MethodPost {
				e.repo.SetRef(mainRef, gitobj.ZeroID)
			}
		})
		wantErr(t, "deleted by hook", e.c.UpdateRefs(ctx, e.ref, RefUpdate{Name: mainRef, Old: head, New: main4}), ErrStale)
	})
}

func TestUpdateRefsCapabilities(t *testing.T) {
	forEachForge(t, fakeforge.GitOptions{NoAtomic: true, NoDeleteRefs: true}, func(t *testing.T, e *writeEnv) {
		ctx := context.Background()
		head, main4 := confOID(e.fx.Head), confOID(e.fx.Main[4])
		err := e.c.UpdateRefs(ctx, e.ref,
			RefUpdate{Name: mainRef, Old: head, New: main4},
			RefUpdate{Name: "refs/heads/x", Old: ZeroOID, New: head})
		wantErr(t, "two refs without atomic", err, ErrUnsupported)
		err = e.c.UpdateRefs(ctx, e.ref, RefUpdate{Name: "refs/heads/feature", Old: confOID(e.fx.Feature[2]), New: ZeroOID})
		wantErr(t, "delete without delete-refs", err, ErrUnsupported)
		if e.pushes() != 0 {
			t.Errorf("unsupported updates sent %d pushes", e.pushes())
		}
		wantErr(t, "one ref without atomic", e.c.UpdateRefs(ctx, e.ref, RefUpdate{Name: mainRef, Old: head, New: main4}), nil)

		e.remount(fakeforge.GitOptions{ObjectFormat: "sha256"})
		wantErr(t, "sha256 repository", e.c.UpdateRefs(ctx, e.ref, RefUpdate{Name: mainRef, Old: main4, New: head}), ErrUnsupported)
	})
}

func TestUpdateRefsRejected(t *testing.T) {
	decline := fakeforge.GitOptions{Decline: func([]gitwire.Command) bool { return true }}
	forEachForge(t, decline, func(t *testing.T, e *writeEnv) {
		err := e.c.UpdateRefs(context.Background(), e.ref, RefUpdate{Name: mainRef, Old: confOID(e.fx.Head), New: confOID(e.fx.Main[4])})
		wantErr(t, "declined", err, ErrRejected)
		if errors.Is(err, ErrStale) || !strings.Contains(err.Error(), "pre-receive hook declined") || e.at(mainRef) != e.fx.Head {
			t.Errorf("declined: %v, main at %s", err, e.at(mainRef))
		}
	})
}

func TestUpdateRefsTransportFailure(t *testing.T) {
	forEachForge(t, fakeforge.GitOptions{AbortAfterApply: true}, func(t *testing.T, e *writeEnv) {
		ctx := context.Background()
		head, main4 := confOID(e.fx.Head), confOID(e.fx.Main[4])
		up := RefUpdate{Name: mainRef, Old: head, New: main4}

		// Applied, then the connection dropped: the re-advertisement shows
		// the new value.
		wantErr(t, "abort after apply", e.c.UpdateRefs(ctx, e.ref, up), nil)
		if e.at(mainRef) != e.fx.Main[4] {
			t.Error("ref not moved")
		}

		// Dropped before applying: refs unchanged, not stale or rejected.
		e.repo.SetRef(mainRef, e.fx.Head)
		e.remount(fakeforge.GitOptions{AbortBeforeApply: true})
		err := e.c.UpdateRefs(ctx, e.ref, up)
		if err == nil || errors.Is(err, ErrStale) || errors.Is(err, ErrRejected) || e.at(mainRef) != e.fx.Head {
			t.Errorf("abort before apply: %v, main at %s", err, e.at(mainRef))
		}

		// Dropped while someone else moved the ref: the outcome is
		// unknown, so neither stale nor rejected.
		e.s.SetHook(func(r *http.Request) {
			if r.Method == http.MethodPost {
				e.repo.SetRef(mainRef, e.fx.Main[0])
			}
		})
		err = e.c.UpdateRefs(ctx, e.ref, up)
		if err == nil || errors.Is(err, ErrStale) || errors.Is(err, ErrRejected) {
			t.Errorf("abort and moved: %v", err)
		}
		e.s.SetHook(nil)

		// Applied, dropped, then another push lands on top before the
		// re-advertisement: not ErrStale, as a retry would apply twice.
		e.repo.SetRef(mainRef, e.fx.Head)
		e.remount(fakeforge.GitOptions{AbortAfterApply: true})
		var mu sync.Mutex
		posted, applied := false, false
		e.s.SetHook(func(r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case r.Method == http.MethodPost:
				posted = true
			case posted && !applied:
				applied = e.at(mainRef) == e.fx.Main[4]
				e.repo.SetRef(mainRef, e.fx.Main[0])
			}
		})
		err = e.c.UpdateRefs(ctx, e.ref, up)
		if err == nil || errors.Is(err, ErrStale) || errors.Is(err, ErrRejected) || !applied {
			t.Errorf("applied and moved: %v, applied %v", err, applied)
		}
		e.s.SetHook(nil)

		// A gateway error after the POST is checked the same way.
		e.repo.SetRef(mainRef, e.fx.Head)
		e.remount(fakeforge.GitOptions{})
		e.s.Inject(fakeforge.Fault{Method: http.MethodPost, Path: "*/git-receive-pack", Status: http.StatusBadGateway, Times: 1})
		err = e.c.UpdateRefs(ctx, e.ref, up)
		var fe *forgehttp.Error
		if !errors.As(err, &fe) || fe.StatusCode != http.StatusBadGateway || errors.Is(err, ErrStale) {
			t.Errorf("502: %v", err)
		}
	})
}

// reportFault answers the next push with a report-status body on
// side-band-64k, without applying the push.
func reportFault(lines ...string) fakeforge.Fault {
	var inner, outer strings.Builder
	w := gitwire.NewPktWriter(&inner)
	for _, l := range lines {
		w.Data([]byte(l + "\n"))
	}
	inner.WriteString("0000")
	gitwire.NewPktWriter(&outer).Data([]byte("\x01" + inner.String()))
	outer.WriteString("0000")
	return fakeforge.Fault{Method: http.MethodPost, Path: "*/git-receive-pack", Status: http.StatusOK, Times: 1,
		Header: http.Header{"Content-Type": {"application/x-git-receive-pack-result"}}, Body: outer.String()}
}

func TestUpdateRefsUnclearReport(t *testing.T) {
	forEachForge(t, fakeforge.GitOptions{}, func(t *testing.T, e *writeEnv) {
		ctx := context.Background()
		head, main4 := confOID(e.fx.Head), confOID(e.fx.Main[4])
		up := RefUpdate{Name: mainRef, Old: head, New: main4}
		for _, tc := range []struct {
			name  string
			to    gitobj.ID // where main is after the POST; zero leaves it
			lines []string
			want  error
		}{
			{"no status, at New", e.fx.Main[4], []string{"unpack ok"}, nil},
			{"ng, at New", e.fx.Main[4], []string{"unpack ok", "ng " + mainRef + " failed"}, nil},
			{"unpack failed, at New", e.fx.Main[4], []string{"unpack broken", "ng " + mainRef + " failed"}, ErrRejected},
			{"no status, at Old", gitobj.ZeroID, []string{"unpack ok"}, ErrRejected},
			{"ng, moved", e.fx.Main[0], []string{"unpack ok", "ng " + mainRef + " failed"}, ErrStale},
		} {
			e.repo.SetRef(mainRef, e.fx.Head)
			e.s.SetHook(func(r *http.Request) {
				if r.Method == http.MethodPost && !tc.to.IsZero() {
					e.repo.SetRef(mainRef, tc.to)
				}
			})
			e.s.Inject(reportFault(tc.lines...))
			err := e.c.UpdateRefs(ctx, e.ref, up)
			wantErr(t, tc.name, err, tc.want)
			if tc.want != ErrStale && errors.Is(err, ErrStale) {
				t.Errorf("%s: false ErrStale: %v", tc.name, err)
			}
		}
		e.s.SetHook(nil)
	})
}

func TestUpdateRefsAuth(t *testing.T) {
	forEachForge(t, fakeforge.GitOptions{}, func(t *testing.T, e *writeEnv) {
		ctx := context.Background()
		up := RefUpdate{Name: mainRef, Old: confOID(e.fx.Head), New: confOID(e.fx.Main[4])}

		e.s.SetToken("other")
		wantErr(t, "wrong token", e.c.UpdateRefs(ctx, e.ref, up), forgehttp.ErrUnauthorized)
		e.s.SetToken(fakeforge.Token)

		e.s.SetAccess(e.path, fakeforge.AccessRead)
		wantErr(t, "read only", e.c.UpdateRefs(ctx, e.ref, up), forgehttp.ErrForbidden)
		_, err := e.c.CreateCommit(ctx, e.ref, NewCommit{
			Parents: []OID{up.Old}, Ref: mainRef, Old: up.Old, Message: "m\n",
			Author: Signature{Name: "A", Email: "a@example.com"},
		})
		wantErr(t, "read-only commit", err, forgehttp.ErrForbidden)

		e.repo.SetPrivate(true)
		e.s.SetAccess(e.path, fakeforge.AccessNone)
		wantErr(t, "private", e.c.UpdateRefs(ctx, e.ref, up), forgehttp.ErrNotFound)
		e.s.SetAccess(e.path, fakeforge.AccessWrite)

		missing := RepositoryRef{Source: "src", Repository: "nobody/missing"}
		wantErr(t, "missing repository", e.c.UpdateRefs(ctx, missing, up), forgehttp.ErrNotFound)
		if e.pushes() != 0 || e.at(mainRef) != e.fx.Head {
			t.Errorf("auth failures pushed %d times", e.pushes())
		}
	})
}

func TestWritesNeedGitRemote(t *testing.T) {
	s := fakeforge.NewServer(t, forgeauth.GitHub, fakeforge.GitHub)
	r := s.NewRepo("o/r")
	fx := fakeforge.Seed(r)
	c, err := New(Source{ID: "src", Session: s.Session})
	if err != nil {
		t.Fatal(err)
	}
	ref := RepositoryRef{Source: "src", Repository: "o/r"}
	head := confOID(fx.Head)
	wantErr(t, "UpdateRefs", c.UpdateRefs(context.Background(), ref, RefUpdate{Name: mainRef, Old: head, New: confOID(fx.Main[4])}), ErrUnsupported)
	_, err = c.CreateCommit(context.Background(), ref, NewCommit{
		Parents: []OID{head}, Ref: mainRef, Old: head, Message: "m\n",
		Author: Signature{Name: "A", Email: "a@example.com"},
	})
	wantErr(t, "CreateCommit", err, ErrUnsupported)
	if caps, _ := c.Capabilities("src"); caps.Writes {
		t.Error("Writes without a Git remote")
	}
	if len(s.Requests()) != 0 {
		t.Errorf("requests without a Git remote: %v", s.Requests())
	}
}

// expectTree builds the tree that changes on base give, in a scratch
// repository, so the forge's store is not touched.
func expectTree(t *testing.T, r *fakeforge.Repo, base gitobj.ID, changes []FileChange) gitobj.ID {
	t.Helper()
	files := map[string]fakeforge.File{}
	if !base.IsZero() {
		entries, err := r.ListTree(base, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			switch e.Mode {
			case gitobj.ModeTree:
			case gitobj.ModeSubmodule:
				files[e.Path] = fakeforge.File{Mode: e.Mode, Submodule: e.ID}
			default:
				data, err := r.ReadBlob(e.ID)
				if err != nil {
					t.Fatal(err)
				}
				files[e.Path] = fakeforge.File{Mode: e.Mode, Data: data}
			}
		}
	}
	for _, ch := range changes {
		if ch.Delete {
			delete(files, ch.Path)
			continue
		}
		mode := gitobj.Mode(ch.Mode)
		if old, ok := files[ch.Path]; mode == 0 && ok && old.Mode != gitobj.ModeSubmodule {
			mode = old.Mode
		} else if mode == 0 {
			mode = gitobj.ModeFile
		}
		f := fakeforge.File{Mode: mode, Data: ch.Content}
		if mode == gitobj.ModeSubmodule {
			f.Submodule, _ = gitobj.ParseID(string(ch.Submodule))
		}
		files[ch.Path] = f
	}
	return fakeforge.NewRepo().BuildTree(files)
}

var (
	testAuthor = Signature{Name: "Ada Lovelace", Email: "ada@example.com",
		When: time.Date(2025, 3, 4, 5, 6, 7, 0, time.FixedZone("", 2*3600))}
	otherSubmodule = OID(gitobj.Hash(gitobj.TypeCommit, []byte("other submodule")).String())
)

// editChanges touch every kind of entry in the Seed fixture's head.
var editChanges = []FileChange{
	{Path: fakeforge.PathReadme, Content: []byte("# New readme\n")},
	{Path: fakeforge.PathExec, Content: []byte("#!/bin/sh\necho v2\n")}, // keeps 100755
	{Path: fakeforge.PathMain, Mode: ModeExec, Content: []byte("package main\n")},
	{Path: fakeforge.PathSymlink, Delete: true},
	{Path: "link2", Mode: ModeSymlink, Content: []byte("src/main.go")},
	{Path: "vendor/other", Mode: ModeSubmodule, Submodule: otherSubmodule},
	{Path: "a/b/c.txt", Content: []byte("deep\n")},
	{Path: fakeforge.PathNested, Delete: true}, // empties docs/
	{Path: fakeforge.PathUnicode, Content: []byte("neu\n")},
	{Path: "empty.txt"},
	{Path: fakeforge.PathBinary, Mode: ModeFile, Content: fakeforge.Binary[:5]},
}

func TestCreateCommit(t *testing.T) {
	ids := map[forgeauth.Kind]OID{}
	forEachForge(t, fakeforge.GitOptions{}, func(t *testing.T, e *writeEnv) {
		ctx := context.Background()
		head := confOID(e.fx.Head)
		n := NewCommit{
			Parents: []OID{head}, Changes: editChanges, Author: testAuthor,
			Message: "Edit files\n\nBody.\n", Ref: mainRef, Old: head,
		}
		cm, err := e.c.CreateCommit(ctx, e.ref, n)
		if err != nil {
			t.Fatal(err)
		}
		ids[e.kind] = cm.ID
		want := expectTree(t, e.repo, e.fx.Head, editChanges)
		if cm.Tree != confOID(want) || !slices.Equal(cm.Parents, []OID{head}) || cm.Message != strings.TrimSuffix(n.Message, "\n") ||
			!cm.Author.When.Equal(testAuthor.When) || cm.Author.Name != testAuthor.Name || cm.Committer != cm.Author {
			t.Errorf("commit = %+v, want tree %s", cm, want)
		}
		if e.at(mainRef).String() != string(cm.ID) || e.pushes() != 1 {
			t.Errorf("main at %s, pushes %d", e.at(mainRef), e.pushes())
		}
		fetches := e.s.Calls(http.MethodPost, "*/git-upload-pack")
		if wantFetch := map[bool]int{true: 1, false: 0}[e.kind == forgeauth.GitLab]; fetches != wantFetch {
			t.Errorf("%d upload-pack fetches, want %d", fetches, wantFetch)
		}

		// Read the commit back through the Client.
		got, err := e.c.Commit(ctx, e.ref, "main")
		if err != nil {
			t.Fatal(err)
		}
		wantTree := cm.Tree
		if e.kind == forgeauth.GitLab {
			wantTree = ""
		}
		if got.ID != cm.ID || got.Tree != wantTree || !slices.Equal(got.Parents, cm.Parents) || got.Message != cm.Message ||
			!got.Author.When.Equal(cm.Author.When) || got.Committer.Email != cm.Committer.Email {
			t.Errorf("read back %+v, want %+v", got, cm)
		}
		modes := map[string]FileMode{
			fakeforge.PathReadme: ModeFile, fakeforge.PathExec: ModeExec, fakeforge.PathMain: ModeExec,
			"link2": ModeSymlink, "vendor/other": ModeSubmodule, "vendor/lib": ModeSubmodule,
			"a/b/c.txt": ModeFile, "empty.txt": ModeFile, fakeforge.PathUnicode: ModeFile,
		}
		for p, m := range modes {
			pi, err := e.c.Stat(ctx, e.ref, string(cm.ID), p)
			if err != nil || pi.Mode != m {
				t.Errorf("Stat(%s) = %+v, %v, want mode %s", p, pi, err, m)
			}
		}
		if pi, _ := e.c.Stat(ctx, e.ref, string(cm.ID), "vendor/other"); pi.ID != otherSubmodule {
			t.Errorf("submodule ID %s", pi.ID)
		}
		for _, p := range []string{fakeforge.PathSymlink, fakeforge.PathNested} {
			if _, err := e.c.Stat(ctx, e.ref, string(cm.ID), p); !errors.Is(err, ErrPathNotFound) {
				t.Errorf("Stat(%s) after delete: %v", p, err)
			}
		}
		if _, err := e.c.Tree(ctx, e.ref, string(cm.ID), TreeOptions{Path: "docs"}); !errors.Is(err, ErrPathNotFound) {
			t.Errorf("emptied docs/ still there: %v", err)
		}
		for p, data := range map[string]string{"link2": "src/main.go", fakeforge.PathReadme: "# New readme\n", "empty.txt": ""} {
			rd, err := e.c.ReadFile(ctx, e.ref, "main", p)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(rd)
			rd.Close()
			if string(b) != data {
				t.Errorf("ReadFile(%s) = %q, want %q", p, b, data)
			}
		}
	})
	if ids[forgeauth.GitHub] != ids[forgeauth.GitLab] || ids[forgeauth.GitHub] != ids[forgeauth.Forgejo] {
		t.Errorf("same commit got different IDs: %v", ids)
	}
}

func TestCreateCommitDirectoryToFile(t *testing.T) {
	forEachForge(t, fakeforge.GitOptions{}, func(t *testing.T, e *writeEnv) {
		ctx := context.Background()
		head := confOID(e.fx.Head)
		// docs/ holds only docs/guide/intro.md; deleting it lets a file
		// named docs take the directory's place.
		changes := []FileChange{
			{Path: fakeforge.PathNested, Delete: true},
			{Path: "docs", Content: []byte("now a file\n")},
		}
		cm, err := e.c.CreateCommit(ctx, e.ref, NewCommit{
			Parents: []OID{head}, Changes: changes, Author: testAuthor, Message: "m\n", Ref: mainRef, Old: head,
		})
		if err != nil {
			t.Fatal(err)
		}
		if want := expectTree(t, e.repo, e.fx.Head, changes); cm.Tree != confOID(want) {
			t.Errorf("tree %s, want %s", cm.Tree, want)
		}
		if pi, err := e.c.Stat(ctx, e.ref, string(cm.ID), "docs"); err != nil || pi.Mode != ModeFile {
			t.Errorf("Stat(docs) = %+v, %v", pi, err)
		}
	})
}

func TestCreateCommitMergeRootTag(t *testing.T) {
	forEachForge(t, fakeforge.GitOptions{}, func(t *testing.T, e *writeEnv) {
		ctx := context.Background()
		head, feat := confOID(e.fx.Head), confOID(e.fx.Feature[2])

		// A merge takes the first parent's tree plus the changes.
		changes := []FileChange{{Path: fakeforge.PathFeature, Content: []byte("merged\n")}}
		cm, err := e.c.CreateCommit(ctx, e.ref, NewCommit{
			Parents: []OID{head, feat}, Changes: changes, Author: testAuthor,
			Message: "Merge feature\n", Ref: mainRef, Old: head,
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.c.Commit(ctx, e.ref, "main")
		if err != nil || got.ID != cm.ID || !slices.Equal(got.Parents, []OID{head, feat}) {
			t.Errorf("merge read back %+v, %v", got, err)
		}
		if cm.Tree != confOID(expectTree(t, e.repo, e.fx.Head, changes)) {
			t.Errorf("merge tree %s", cm.Tree)
		}
		// No changes keep the tree.
		cm2, err := e.c.CreateCommit(ctx, e.ref, NewCommit{
			Parents: []OID{cm.ID, confOID(e.fx.Main[1])}, Author: testAuthor,
			Message: "Ours\n", Ref: mainRef, Old: cm.ID,
		})
		if err != nil || cm2.Tree != cm.Tree || len(cm2.Parents) != 2 {
			t.Errorf("merge without changes: %+v, %v", cm2, err)
		}

		// A root commit on a new branch.
		root := []FileChange{{Path: "x/y.txt", Content: []byte("y\n")}, {Path: "run", Mode: ModeExec, Content: []byte("r\n")}}
		rc, err := e.c.CreateCommit(ctx, e.ref, NewCommit{
			Changes: root, Author: testAuthor, Message: "Root\n", Ref: "refs/heads/root", Old: ZeroOID,
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err = e.c.Commit(ctx, e.ref, "refs/heads/root")
		if err != nil || got.ID != rc.ID || len(got.Parents) != 0 || rc.Parents == nil || len(rc.Parents) != 0 {
			t.Errorf("root read back %+v, %v (created %+v)", got, err, rc)
		}
		if rc.Tree != confOID(expectTree(t, e.repo, gitobj.ZeroID, root)) {
			t.Errorf("root tree %s", rc.Tree)
		}
		tr, err := e.c.Tree(ctx, e.ref, "refs/heads/root", TreeOptions{Recursive: true})
		if err != nil || len(tr.Entries) != 3 {
			t.Errorf("root tree listing %+v, %v", tr, err)
		}

		// A lightweight tag created with its commit.
		tc, err := e.c.CreateCommit(ctx, e.ref, NewCommit{
			Parents: []OID{confOID(e.fx.Main[0])}, Author: testAuthor, Message: "Tagged\n",
			Changes: []FileChange{{Path: "TAG", Content: []byte("t\n")}}, Ref: "refs/tags/t1", Old: ZeroOID,
		})
		if err != nil || e.at("refs/tags/t1").String() != string(tc.ID) {
			t.Errorf("tag commit %+v, %v", tc, err)
		}
	})
}

func TestCreateCommitErrors(t *testing.T) {
	forEachForge(t, fakeforge.GitOptions{}, func(t *testing.T, e *writeEnv) {
		ctx := context.Background()
		head := confOID(e.fx.Head)
		commit := func(ch ...FileChange) error {
			_, err := e.c.CreateCommit(ctx, e.ref, NewCommit{
				Parents: []OID{head}, Changes: ch, Author: testAuthor, Message: "m\n", Ref: mainRef, Old: head,
			})
			return err
		}
		for _, tc := range []struct {
			name string
			ch   FileChange
			want error
		}{
			{"delete missing", FileChange{Path: "nope.txt", Delete: true}, ErrPathNotFound},
			{"delete in missing dir", FileChange{Path: "no/dir/x", Delete: true}, ErrPathNotFound},
			{"delete below file", FileChange{Path: "README.md/x", Delete: true}, ErrPathNotFound},
			{"delete directory", FileChange{Path: "docs", Delete: true}, ErrNotFile},
			{"write below file", FileChange{Path: "README.md/x", Content: []byte("x")}, ErrInvalid},
			{"write below submodule", FileChange{Path: "vendor/lib/x", Content: []byte("x")}, ErrInvalid},
			{"write over directory", FileChange{Path: "docs/guide", Content: []byte("x")}, ErrInvalid},
			{"8.3 .git name", FileChange{Path: "a/GIT~1", Content: []byte("x")}, ErrInvalid},
			{"symlinked .gitmodules", FileChange{Path: ".gitmodules", Mode: ModeSymlink, Content: []byte("x")}, ErrInvalid},
			{"sha256 submodule", FileChange{Path: "s", Mode: ModeSubmodule, Submodule: OID(strings.Repeat("b", 64))}, ErrUnsupported},
		} {
			wantErr(t, tc.name, commit(tc.ch), tc.want)
		}
		if err := commit(FileChange{Path: "nope.txt", Delete: true}); !errors.Is(err, forgehttp.ErrNotFound) {
			t.Errorf("delete missing does not match forgehttp.ErrNotFound: %v", err)
		}

		_, err := e.c.CreateCommit(ctx, e.ref, NewCommit{
			Parents: []OID{head}, Author: testAuthor, Message: "m\n", Ref: mainRef, Old: confOID(e.fx.Main[4]),
		})
		wantErr(t, "stale old", err, ErrStale)
		missing := OID(gitobj.Hash(gitobj.TypeCommit, []byte("missing")).String())
		_, err = e.c.CreateCommit(ctx, e.ref, NewCommit{
			Parents: []OID{head, missing}, Author: testAuthor, Message: "m\n", Ref: mainRef, Old: head,
		})
		wantErr(t, "missing parent", err, ErrRevisionNotFound)
		if e.pushes() != 0 || e.at(mainRef) != e.fx.Head {
			t.Errorf("failed commits pushed %d times", e.pushes())
		}

		e.remount(fakeforge.GitOptions{Decline: func([]gitwire.Command) bool { return true }})
		wantErr(t, "declined", commit(FileChange{Path: "x", Content: []byte("x")}), ErrRejected)
		e.remount(fakeforge.GitOptions{BeforeReceive: func(r *fakeforge.Repo, _ []gitwire.Command) {
			r.SetRef(mainRef, e.fx.Main[3])
		}})
		wantErr(t, "moved before push", commit(FileChange{Path: "x", Content: []byte("x")}), ErrStale)
		e.repo.SetRef(mainRef, e.fx.Head)
		e.remount(fakeforge.GitOptions{AbortAfterApply: true})
		cm, err := e.c.CreateCommit(ctx, e.ref, NewCommit{
			Parents: []OID{head}, Changes: []FileChange{{Path: "x", Content: []byte("x")}},
			Author: testAuthor, Message: "m\n", Ref: mainRef, Old: head,
		})
		if err != nil || e.at(mainRef).String() != string(cm.ID) {
			t.Errorf("abort after apply: %+v, %v", cm, err)
		}
	})
}

func TestCreateCommitLimits(t *testing.T) {
	e := newWriteEnv(t, forgeauth.GitHub, fakeforge.GitHub, "o/r", fakeforge.GitOptions{}, Limits{MaxWriteBytes: 8})
	head := confOID(e.fx.Head)
	_, err := e.c.CreateCommit(context.Background(), e.ref, NewCommit{
		Parents: []OID{head}, Author: testAuthor, Message: "m\n", Ref: mainRef, Old: head,
		Changes: []FileChange{{Path: "a", Content: []byte("12345")}, {Path: "b", Content: []byte("6789")}},
	})
	wantErr(t, "over MaxWriteBytes", err, ErrTooLarge)
	if n := len(e.s.Requests()); n != 0 {
		t.Errorf("%d requests for a commit over the limit", n)
	}
}

func TestCreateCommitTimes(t *testing.T) {
	e := newWriteEnv(t, forgeauth.Forgejo, fakeforge.ForgejoAPI, "o/r", fakeforge.GitOptions{}, Limits{})
	head := confOID(e.fx.Head)
	before := time.Now().Add(-time.Second)
	cm, err := e.c.CreateCommit(context.Background(), e.ref, NewCommit{
		Parents: []OID{head}, Author: Signature{Name: "A", Email: "a@example.com"}, Message: "m\n", Ref: mainRef, Old: head,
	})
	after := time.Now().Add(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if w := cm.Author.When; w.Before(before) || w.After(after) || w.Format("-0700") != "+0000" || cm.Committer != cm.Author {
		t.Errorf("author %+v, committer %+v", cm.Author, cm.Committer)
	}
	cm2, err := e.c.CreateCommit(context.Background(), e.ref, NewCommit{
		Parents: []OID{cm.ID}, Author: testAuthor, Committer: Signature{Name: "C", Email: "c@example.com"},
		Message: "m\n", Ref: mainRef, Old: cm.ID,
	})
	if err != nil || !cm2.Author.When.Equal(testAuthor.When) || cm2.Committer.Name != "C" || cm2.Committer.When.Before(before) {
		t.Errorf("explicit committer: %+v, %v", cm2, err)
	}
}

func TestCreateCommitUnverifiableTree(t *testing.T) {
	ctx := context.Background()
	newCommit := func(e *writeEnv) error {
		head := confOID(e.fx.Head)
		_, err := e.c.CreateCommit(ctx, e.ref, NewCommit{
			Parents: []OID{head}, Author: testAuthor, Message: "m\n", Ref: mainRef, Old: head,
			Changes: []FileChange{{Path: "x", Content: []byte("x")}},
		})
		return err
	}

	// GitHub: a listing with an entry missing does not hash to the tree.
	e := newWriteEnv(t, forgeauth.GitHub, fakeforge.GitHub, "o/r", fakeforge.GitOptions{}, Limits{})
	hc, _ := e.repo.ReadCommit(e.fx.Head)
	readme := e.fx.Blobs[fakeforge.PathReadme]
	e.s.Inject(fakeforge.Fault{Path: "/repos/o/r/git/trees/" + hc.Tree.String(), Status: http.StatusOK,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   `{"sha":"` + hc.Tree.String() + `","tree":[{"path":"README.md","mode":"100644","type":"blob","sha":"` + readme.String() + `","size":1}],"truncated":false}`})
	wantMsg(t, "GitHub short listing", newCommit(e), ErrUnsupported, "cannot rebuild")

	// GitLab: the root listing is checked against the fetched commit.
	e = newWriteEnv(t, forgeauth.GitLab, fakeforge.GitLab, "g/r", fakeforge.GitOptions{}, Limits{})
	e.s.Inject(fakeforge.Fault{Path: "/projects/g%2Fr/repository/tree", Status: http.StatusOK,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: `[]`})
	wantMsg(t, "GitLab empty listing", newCommit(e), ErrUnsupported, "cannot rebuild")

	// GitLab without filter support cannot verify the root at all.
	e = newWriteEnv(t, forgeauth.GitLab, fakeforge.GitLab, "g/r", fakeforge.GitOptions{NoFilter: true}, Limits{})
	wantMsg(t, "GitLab without filter", newCommit(e), ErrUnsupported, "filter")
	if e.pushes() != 0 {
		t.Errorf("unverified commits pushed %d times", e.pushes())
	}
}
