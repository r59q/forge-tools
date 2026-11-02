package code

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-code/library/internal/gitwire"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// maxParents caps the parents of one commit.
const maxParents = 100

// CreateCommit makes a commit from n and pushes it, moving n.Ref from
// n.Old to the new commit in the same push. Changes apply to the tree of
// n.Parents[0], or to an empty tree for a root commit. If n.Ref is not at
// n.Old, nothing changes and the error wraps ErrStale. Deleting a missing
// path wraps ErrPathNotFound. A forge refusal, such as branch protection,
// wraps ErrRejected. Trees the library cannot rebuild exactly, and
// SHA-256 repositories, give ErrUnsupported, as does a source without a
// Git remote. If the connection fails after the push is sent and the
// result cannot be told, the error wraps neither ErrStale nor
// ErrRejected; read n.Ref before retrying. Commits are unsigned.
func (c *Client) CreateCommit(ctx context.Context, r RepositoryRef, n NewCommit) (Commit, error) {
	if err := c.checkCommit(n); err != nil {
		return Commit{}, err
	}
	s, o, err := c.start(ctx, r)
	if err != nil {
		return Commit{}, err
	}
	if s.Git == nil {
		return Commit{}, s.wrap(fmt.Errorf("%w: source has no Git remote", ErrUnsupported))
	}
	cm, err := createCommit(o, s, n)
	return cm, s.wrap(err)
}

// UpdateRefs creates, moves and deletes refs in one atomic push: either
// all updates apply or none. If any ref is not at its Old value, nothing
// changes and the error wraps ErrStale. A forge refusal, such as a hook
// or a missing New object, wraps ErrRejected. Several updates need a
// forge that supports atomic pushes, and deletes one that allows them,
// otherwise ErrUnsupported. If the connection fails after the push is
// sent and the result cannot be told, the error wraps neither ErrStale
// nor ErrRejected; read the refs before retrying.
func (c *Client) UpdateRefs(ctx context.Context, r RepositoryRef, u ...RefUpdate) error {
	if err := checkRefUpdates(u); err != nil {
		return err
	}
	s, o, err := c.start(ctx, r)
	if err != nil {
		return err
	}
	if s.Git == nil {
		return s.wrap(fmt.Errorf("%w: source has no Git remote", ErrUnsupported))
	}
	cmds := make([]gitwire.Command, len(u))
	for i, up := range u {
		old, err := sha1ID(up.Old)
		if err != nil {
			return s.wrap(err)
		}
		nw, err := sha1ID(up.New)
		if err != nil {
			return s.wrap(err)
		}
		cmds[i] = gitwire.Command{Name: up.Name, Old: old, New: nw}
	}
	url, err := s.Git.URL(o.repo)
	if err != nil {
		return s.wrap(err)
	}
	return s.wrap(push(ctx, s.Git, url, cmds, nil))
}

// sha1ID converts a checked OID. SHA-256 IDs give ErrUnsupported.
func sha1ID(id OID) (gitobj.ID, error) {
	if len(id) != 40 {
		return gitobj.ID{}, fmt.Errorf("%w: SHA-256 object ID %s", ErrUnsupported, id)
	}
	return gitobj.ParseID(string(id))
}

// createCommit builds n on top of its first parent's tree and pushes it.
func createCommit(o *op, s *source, n NewCommit) (Commit, error) {
	old, err := sha1ID(n.Old)
	if err != nil {
		return Commit{}, err
	}
	parents := make([]gitobj.ID, len(n.Parents))
	for i, p := range n.Parents {
		if parents[i], err = sha1ID(p); err != nil {
			return Commit{}, err
		}
	}
	url, err := s.Git.URL(o.repo)
	if err != nil {
		return Commit{}, err
	}

	e := &editor{o: o, b: s.forge, objs: map[gitobj.ID]gitobj.Object{}}
	e.root = &editTree{entries: map[string]gitobj.TreeEntry{}, kids: map[string]*editTree{}}
	if len(n.Parents) > 0 {
		e.base = n.Parents[0]
		rootID, err := baseTree(o, s, url, n.Parents)
		if err != nil {
			return Commit{}, err
		}
		if e.root, err = e.load("", rootID); err != nil {
			return Commit{}, err
		}
	}
	if err := e.apply(n.Changes); err != nil {
		return Commit{}, err
	}
	tree, err := e.write(e.root)
	if err != nil {
		return Commit{}, err
	}

	now := time.Now().UTC()
	author, committer := n.Author, n.Committer
	if author.When.IsZero() {
		author.When = now
	}
	if committer == (Signature{}) {
		committer = author
	} else if committer.When.IsZero() {
		committer.When = now
	}
	data, err := gitobj.EncodeCommit(gitobj.Commit{
		Tree:      tree,
		Parents:   parents,
		Author:    gitobj.Signature(author),
		Committer: gitobj.Signature(committer),
		Message:   n.Message,
	})
	if err != nil {
		return Commit{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	commit := gitobj.Object{Type: gitobj.TypeCommit, Data: data}
	id := commit.ID()
	e.objs[id] = commit

	// Blobs first, then trees, then the commit, each sorted by ID, so the
	// pack is the same for the same commit.
	rank := map[gitobj.Type]int{gitobj.TypeBlob: 0, gitobj.TypeTree: 1, gitobj.TypeCommit: 2}
	keys := slices.SortedFunc(maps.Keys(e.objs), func(a, b gitobj.ID) int {
		if d := rank[e.objs[a].Type] - rank[e.objs[b].Type]; d != 0 {
			return d
		}
		return bytes.Compare(a[:], b[:])
	})
	objs := make([]gitobj.Object, len(keys))
	for i, k := range keys {
		objs[i] = e.objs[k]
	}
	cmd := gitwire.Command{Name: n.Ref, Old: old, New: id}
	if err := push(o.ctx, s.Git, url, []gitwire.Command{cmd}, objs); err != nil {
		return Commit{}, err
	}

	dc, err := gitobj.DecodeCommit(data)
	if err != nil {
		return Commit{}, err
	}
	out := Commit{
		ID:        OID(id.String()),
		Tree:      OID(tree.String()),
		Parents:   slices.Clone(n.Parents),
		Author:    Signature(dc.Author),
		Committer: Signature(dc.Committer),
		Message:   commitMessage(dc.Message),
	}
	if out.Parents == nil {
		out.Parents = []OID{}
	}
	return out, nil
}

// baseTree checks that every parent exists and returns the root tree ID
// of the first one. On forges that do not report tree IDs it fetches the
// commit object over Git.
func baseTree(o *op, s *source, url string, parents []OID) (gitobj.ID, error) {
	first, err := s.forge.commit(o, parents[0])
	if err != nil {
		return gitobj.ID{}, err
	}
	for _, p := range parents[1:] {
		if _, err := s.forge.commit(o, p); err != nil {
			return gitobj.ID{}, err
		}
	}
	if first.Tree != "" {
		return sha1ID(first.Tree)
	}
	pid, _ := sha1ID(parents[0])
	data, err := gitwire.FetchCommit(o.ctx, s.Git.Client, url, pid)
	if err != nil {
		return gitobj.ID{}, fmt.Errorf("fetch parent %s for its tree ID: %w", pid, gitError(err))
	}
	pc, err := gitobj.DecodeCommit(data)
	if err != nil {
		return gitobj.ID{}, fmt.Errorf("%w: parent %s: %w", ErrUnsupported, pid, err)
	}
	return pc.Tree, nil
}

// editor applies file changes to a tree read from the forge. It loads
// only the directories along changed paths, checks each listing against
// its tree ID, and collects the new objects.
type editor struct {
	o    *op
	b    backend
	base OID // the commit the trees are read from
	root *editTree
	objs map[gitobj.ID]gitobj.Object
}

// editTree is one directory being edited.
type editTree struct {
	path    string
	orig    gitobj.ID // the tree ID it was read from; zero if new
	entries map[string]gitobj.TreeEntry
	kids    map[string]*editTree // subdirectories being edited, by name
}

// load lists the directory path of the base commit and checks that the
// listing hashes to want.
func (e *editor) load(path string, want gitobj.ID) (*editTree, error) {
	t, err := e.b.tree(e.o, e.base, path, false, defaultMaxEntries)
	if err != nil {
		return nil, err
	}
	if t.Truncated {
		return nil, fmt.Errorf("%w: directory %q has too many entries to edit", ErrTooLarge, path)
	}
	et := &editTree{path: path, orig: want, entries: map[string]gitobj.TreeEntry{}, kids: map[string]*editTree{}}
	list := make([]gitobj.TreeEntry, 0, len(t.Entries))
	for _, en := range t.Entries {
		id, err := sha1ID(en.ID)
		if err != nil {
			return nil, err
		}
		te := gitobj.TreeEntry{Name: en.Name, Mode: gitobj.Mode(en.Mode), ID: id}
		list = append(list, te)
		et.entries[te.Name] = te
	}
	data, err := gitobj.EncodeTree(list)
	if err != nil || gitobj.Hash(gitobj.TypeTree, data) != want {
		return nil, fmt.Errorf("%w: cannot rebuild directory %q exactly from the forge's listing", ErrUnsupported, path)
	}
	return et, nil
}

// apply applies changes: deletes first, then writes, each in path order.
func (e *editor) apply(changes []FileChange) error {
	sorted := slices.Clone(changes)
	slices.SortFunc(sorted, func(a, b FileChange) int {
		if a.Delete != b.Delete {
			if a.Delete {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	for _, ch := range sorted {
		if err := e.change(ch); err != nil {
			return err
		}
	}
	return nil
}

// change applies one file change.
func (e *editor) change(ch FileChange) error {
	segs := strings.Split(ch.Path, "/")
	dir := e.root
	for i, name := range segs[:len(segs)-1] {
		if kid, ok := dir.kids[name]; ok {
			dir = kid
			continue
		}
		sub := strings.Join(segs[:i+1], "/")
		en, ok := dir.entries[name]
		var kid *editTree
		switch {
		case !ok && ch.Delete:
			return pathMissing(ch.Path)
		case !ok:
			kid = &editTree{path: sub, entries: map[string]gitobj.TreeEntry{}, kids: map[string]*editTree{}}
		case en.Mode == gitobj.ModeTree:
			var err error
			if kid, err = e.load(sub, en.ID); err != nil {
				return err
			}
		case ch.Delete:
			return pathMissing(ch.Path)
		default:
			return fmt.Errorf("%w: change to %q: %q is not a directory", ErrInvalid, ch.Path, sub)
		}
		dir.kids[name] = kid
		dir = kid
	}

	name := segs[len(segs)-1]
	en, exists := dir.entries[name]
	if kid, ok := dir.kids[name]; ok && !ch.Delete && emptied(kid) {
		// Earlier deletes emptied the directory, so a file can take its place.
		delete(dir.kids, name)
		delete(dir.entries, name)
		exists = false
	}
	if _, isDir := dir.kids[name]; isDir || (exists && en.Mode == gitobj.ModeTree) {
		if ch.Delete {
			return fmt.Errorf("%w: cannot delete directory %q", ErrNotFile, ch.Path)
		}
		return fmt.Errorf("%w: change to %q: path is a directory", ErrInvalid, ch.Path)
	}
	if ch.Delete {
		if !exists {
			return pathMissing(ch.Path)
		}
		delete(dir.entries, name)
		return nil
	}
	mode := gitobj.Mode(ch.Mode)
	if mode == 0 {
		mode = gitobj.ModeFile
		if exists && en.Mode != gitobj.ModeSubmodule {
			mode = en.Mode
		}
	}
	var id gitobj.ID
	if mode == gitobj.ModeSubmodule {
		var err error
		if id, err = sha1ID(ch.Submodule); err != nil {
			return err
		}
	} else {
		blob := gitobj.Object{Type: gitobj.TypeBlob, Data: ch.Content}
		if blob.Data == nil {
			blob.Data = []byte{}
		}
		id = blob.ID()
		e.objs[id] = blob
	}
	dir.entries[name] = gitobj.TreeEntry{Name: name, Mode: mode, ID: id}
	return nil
}

// emptied reports whether t holds no files once its edits apply: every
// entry is an edited subdirectory that is itself emptied.
func emptied(t *editTree) bool {
	for name := range t.entries {
		if k, ok := t.kids[name]; !ok || !emptied(k) {
			return false
		}
	}
	for _, k := range t.kids {
		if !emptied(k) {
			return false
		}
	}
	return true
}

// write encodes t and its edited subdirectories bottom up, adds the new
// trees to e.objs and returns t's ID. Emptied subdirectories are removed.
func (e *editor) write(t *editTree) (gitobj.ID, error) {
	for name, kid := range t.kids {
		id, err := e.write(kid)
		if err != nil {
			return gitobj.ID{}, err
		}
		if len(kid.entries) == 0 {
			delete(t.entries, name)
		} else {
			t.entries[name] = gitobj.TreeEntry{Name: name, Mode: gitobj.ModeTree, ID: id}
		}
	}
	list := make([]gitobj.TreeEntry, 0, len(t.entries))
	for _, en := range t.entries {
		list = append(list, en)
	}
	data, err := gitobj.EncodeTree(list)
	if err != nil {
		return gitobj.ID{}, fmt.Errorf("%w: directory %q: %w", ErrInvalid, t.path, err)
	}
	tree := gitobj.Object{Type: gitobj.TypeTree, Data: data}
	id := tree.ID()
	if id != t.orig {
		e.objs[id] = tree
	}
	return id, nil
}

// push sends cmds and a pack of objs to the receive-pack endpoint at url.
// It checks every Old against the advertisement first, sends nothing on
// a mismatch, and classifies failures as ErrStale, ErrRejected or
// ErrUnsupported.
func push(ctx context.Context, git *forgeauth.GitRemote, url string, cmds []gitwire.Command, objs []gitobj.Object) error {
	adv, err := gitwire.Advertise(ctx, git.Client, url)
	if err != nil {
		return gitError(err)
	}
	for _, c := range cmds {
		if cur := adv.Refs[c.Name]; cur != c.Old {
			return fmt.Errorf("%w: %s is at %s, want %s", ErrStale, c.Name, refValue(cur), refValue(c.Old))
		}
	}
	caps := []string{"report-status"}
	if len(cmds) > 1 {
		if !adv.Caps.Has("atomic") {
			return fmt.Errorf("%w: the forge does not support atomic pushes of several refs", ErrUnsupported)
		}
		caps = append(caps, "atomic")
	}
	if slices.ContainsFunc(cmds, func(c gitwire.Command) bool { return c.New.IsZero() }) {
		if !adv.Caps.Has("delete-refs") {
			return fmt.Errorf("%w: the forge does not allow deleting refs by push", ErrUnsupported)
		}
		caps = append(caps, "delete-refs")
	}
	for _, opt := range []string{"side-band-64k", "quiet"} {
		if adv.Caps.Has(opt) {
			caps = append(caps, opt)
		}
	}
	if adv.Caps.Has("object-format") {
		caps = append(caps, "object-format=sha1")
	}
	var pack io.Reader
	if len(objs) > 0 {
		var buf bytes.Buffer
		if err := gitwire.WritePack(&buf, objs); err != nil {
			return err
		}
		pack = &buf
	}
	rep, err := gitwire.Push(ctx, git.Client, url, cmds, caps, pack)
	if err != nil {
		return recoverPush(ctx, git, url, cmds, err)
	}
	var reasons []string
	if !rep.UnpackOK {
		reasons = append(reasons, "unpack: "+rep.Unpack)
	}
	var failed []gitwire.Command
	for _, c := range cmds {
		st, ok := rep.Refs[c.Name]
		switch {
		case !ok:
			reasons = append(reasons, c.Name+": no status")
		case !st.OK:
			reasons = append(reasons, c.Name+": "+st.Reason)
		default:
			continue
		}
		failed = append(failed, c)
	}
	if len(reasons) == 0 {
		return nil
	}
	if len(failed) == 0 {
		failed = cmds
	}
	why := strings.Join(reasons, "; ")
	// Tell a lost race from a refusal by looking at the refs again. A ref
	// at New looks applied, as OIDs are deterministic. When in doubt it is
	// a refusal, never a false ErrStale.
	now, err := gitwire.Advertise(ctx, git.Client, url)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrRejected, why)
	}
	atNew, moved := true, ""
	for _, c := range failed {
		cur := now.Refs[c.Name]
		if cur == c.New {
			continue
		}
		atNew = false
		if cur != c.Old && moved == "" {
			moved = c.Name
		}
	}
	switch {
	case moved != "":
		return fmt.Errorf("%w: %s moved during the push (%s)", ErrStale, moved, why)
	case atNew && rep.UnpackOK:
		return nil
	}
	return fmt.Errorf("%w: %s", ErrRejected, why)
}

// recoverPush finds out what a push that failed in transit did. If every
// ref is at its New value the push applied. Otherwise the outcome is
// unknown and it returns err: a ref still at Old may yet move if the
// forge applies the push late, and a ref elsewhere may hold our push with
// another on top, so it is never ErrStale.
func recoverPush(ctx context.Context, git *forgeauth.GitRemote, url string, cmds []gitwire.Command, err error) error {
	var fe *forgehttp.Error
	if ctx.Err() != nil || (errors.As(err, &fe) && fe.StatusCode < http.StatusInternalServerError) {
		return gitError(err)
	}
	now, aerr := gitwire.Advertise(ctx, git.Client, url)
	if aerr != nil {
		return fmt.Errorf("code: push outcome unknown: %w", err)
	}
	atNew, atOld := true, true
	for _, c := range cmds {
		cur := now.Refs[c.Name]
		atNew = atNew && cur == c.New
		atOld = atOld && cur == c.Old
	}
	switch {
	case atNew:
		return nil
	case atOld:
		return fmt.Errorf("code: push outcome unknown, refs unchanged: %w", err)
	}
	return fmt.Errorf("code: push outcome unknown, refs moved: %w", err)
}

// gitError maps gitwire sentinels to the package's errors.
func gitError(err error) error {
	switch {
	case errors.Is(err, gitwire.ErrUnsupported):
		return fmt.Errorf("%w: %w", ErrUnsupported, err)
	case errors.Is(err, gitwire.ErrTooLarge):
		return fmt.Errorf("%w: %w", ErrTooLarge, err)
	}
	return err
}

// refValue describes a ref value in errors.
func refValue(id gitobj.ID) string {
	if id.IsZero() {
		return "absent"
	}
	return id.String()
}

// checkCommit checks a NewCommit's arguments.
func (c *Client) checkCommit(n NewCommit) error {
	if err := checkRefName(n.Ref); err != nil {
		return err
	}
	if err := checkOID("old", n.Old); err != nil {
		return err
	}
	if len(n.Parents) > maxParents {
		return fmt.Errorf("%w: more than %d parents", ErrInvalid, maxParents)
	}
	seen := map[OID]bool{}
	for _, p := range n.Parents {
		if err := checkOID("parent", p); err != nil {
			return err
		}
		if p.zero() || seen[p] {
			return fmt.Errorf("%w: parent %s zero or repeated", ErrInvalid, p)
		}
		seen[p] = true
	}
	if strings.TrimSpace(n.Message) == "" || strings.ContainsRune(n.Message, 0) {
		return fmt.Errorf("%w: empty commit message or NUL in it", ErrInvalid)
	}
	if err := checkSignature("author", n.Author); err != nil {
		return err
	}
	if n.Committer != (Signature{}) {
		if err := checkSignature("committer", n.Committer); err != nil {
			return err
		}
	}
	return checkChanges(n.Changes, len(n.Parents) > 0, c.limits.MaxWriteBytes)
}

// checkSignature checks a name and email that go into a commit header.
func checkSignature(what string, s Signature) error {
	for _, v := range []string{s.Name, s.Email} {
		if strings.TrimSpace(v) == "" || strings.ContainsAny(v, "<>\n\r\x00") {
			return fmt.Errorf("%w: %s name and email must be set, without <, > or newlines", ErrInvalid, what)
		}
	}
	return nil
}

// checkChanges checks file changes: valid paths, one change per path, no
// file inside another new file, sane modes, and content within max bytes.
// Without a parent there is nothing to delete or keep a mode of.
func checkChanges(changes []FileChange, hasParent bool, max int64) error {
	seen := map[string]bool{}  // every changed path
	added := map[string]bool{} // paths that are not deleted
	var total int64
	for _, ch := range changes {
		bad := func(why string) error {
			return fmt.Errorf("%w: change to %q: %s", ErrInvalid, ch.Path, why)
		}
		if err := checkWritePath(ch.Path); err != nil {
			return err
		}
		if seen[ch.Path] {
			return bad("repeated path")
		}
		seen[ch.Path] = true
		switch {
		case ch.Delete:
			if ch.Mode != 0 || ch.Content != nil || ch.Submodule != "" {
				return bad("delete with mode, content or submodule")
			}
			if !hasParent {
				return bad("delete in a root commit")
			}
			continue
		case ch.Mode == ModeSubmodule:
			if ch.Content != nil {
				return bad("submodule with content")
			}
			if err := checkOID("submodule", ch.Submodule); err != nil {
				return err
			}
		case ch.Submodule != "":
			return bad("submodule without ModeSubmodule")
		case ch.Mode == ModeTree || (ch.Mode != 0 && !ch.Mode.valid()):
			return bad("mode " + ch.Mode.String())
		}
		added[ch.Path] = true
		total += int64(len(ch.Content))
		if total > max {
			return fmt.Errorf("%w: commit content over %d bytes", ErrTooLarge, max)
		}
	}
	// A new file cannot sit below another new file.
	for p := range added {
		for dir := p; ; {
			i := strings.LastIndexByte(dir, '/')
			if i < 0 {
				break
			}
			if dir = dir[:i]; added[dir] {
				return fmt.Errorf("%w: change to %q is below the file %q", ErrInvalid, p, dir)
			}
		}
	}
	return nil
}

// checkRefUpdates checks UpdateRefs arguments: at least one update, valid
// and distinct names, and full object IDs that differ.
func checkRefUpdates(u []RefUpdate) error {
	if len(u) == 0 {
		return fmt.Errorf("%w: no ref updates", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, up := range u {
		if err := checkRefName(up.Name); err != nil {
			return err
		}
		if seen[up.Name] {
			return fmt.Errorf("%w: ref %q updated twice", ErrInvalid, up.Name)
		}
		seen[up.Name] = true
		if err := checkOID("old", up.Old); err != nil {
			return err
		}
		if err := checkOID("new", up.New); err != nil {
			return err
		}
		if up.Old == up.New || (up.Old.zero() && up.New.zero()) {
			return fmt.Errorf("%w: ref %q: old and new are the same", ErrInvalid, up.Name)
		}
	}
	return nil
}
