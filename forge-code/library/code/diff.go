package code

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/r59q/forge-tools/forge-code/library/internal/linediff"
)

// Compare defaults.
const (
	defaultContext       = 3
	defaultMaxChanges    = 3000
	defaultMaxPatchFiles = 100
	defaultMaxPatchBytes = 256 << 10
	// maxPatchFiles and maxPatchBytes are the largest MaxPatchFiles and
	// MaxPatchBytes; larger values are lowered to them.
	maxPatchFiles = 1000
	maxPatchBytes = 1 << 20
)

// Compare engine bounds.
const (
	// binarySniff is how many leading bytes are checked for a NUL, as git
	// does.
	binarySniff = 8000
	// patchInputFactor times MaxPatchBytes, capped by MaxBlobBytes, is the
	// largest blob read for a patch.
	patchInputFactor = 16
	// patchInputBudget caps the blob bytes one Compare reads for patches.
	// Once spent, the remaining changes get PatchOmitted.
	patchInputBudget = 64 << 20
	// minSubtreeList is the least number of entries one recursive listing
	// of an added or deleted directory may return.
	minSubtreeList = 1000
)

// errStop ends a tree walk early; the diff is then Truncated.
var errStop = errors.New("code: compare stopped")

// MergeBase returns the best common ancestor of revisions a and b. Two
// commits without shared history are ErrInvalid. On Forgejo a
// criss-cross history, with several best bases, is ErrUnsupported.
func (c *Client) MergeBase(ctx context.Context, r RepositoryRef, a, b string) (OID, error) {
	ra, err := parseRevision(a)
	if err != nil {
		return "", err
	}
	rb, err := parseRevision(b)
	if err != nil {
		return "", err
	}
	s, o, err := c.start(ctx, r)
	if err != nil {
		return "", err
	}
	ca, cb, err := resolveBoth(o, s.forge, ra, rb)
	if err != nil {
		return "", s.wrap(err)
	}
	if ca == cb {
		return ca, nil
	}
	id, err := s.forge.mergeBase(o, ca, cb)
	return id, s.wrap(err)
}

// Compare lists the changes from base to head, computed by the library
// from trees and blobs; forge compare APIs are never used. In MergeBase
// mode (the default) head is compared with the merge base of base and
// head, like "git diff base...head"; in Direct mode with base itself.
//
// The walk lists a directory only if its tree ID differs between the two
// sides, so the cost grows with the size of the change. GitLab does not
// report root tree IDs, so its two root listings are always read. An
// added or deleted directory costs one recursive listing.
//
// Renames are exact: a deleted and an added file with the same blob ID
// and kind, paired one to one, same base name first, then in path order.
// There is no copy or similarity detection. A change between file,
// symlink and submodule is TypeChanged; a file that becomes a directory,
// or the reverse, is a deletion plus additions. A mode change, such as
// the executable bit, is Modified.
//
// Limits: past MaxChanges, Changes holds MaxChanges of them and the diff
// is Truncated; they are sorted, but not necessarily the first ones by
// path. If the call limit runs out while resolving or finding the merge
// base, Compare fails with ErrTooLarge; during the tree walk it returns
// the changes found so far, Truncated; while reading blobs for patches,
// the remaining patches are PatchOmitted.
//
// With Patches, the blobs of up to MaxPatchFiles changes are read by ID.
// A NUL in the first 8000 bytes of either side marks the change Binary,
// without a patch. Blobs over 16 × MaxPatchBytes (capped by MaxBlobBytes)
// and patches over MaxPatchBytes are PatchOmitted. A patch is the unified
// diff hunks without file headers; a submodule side reads as
// "Subproject commit <id>", as in git.
func (c *Client) Compare(ctx context.Context, r RepositoryRef, base, head string, opts CompareOptions) (Diff, error) {
	rb, err := parseRevision(base)
	if err != nil {
		return Diff{}, err
	}
	rh, err := parseRevision(head)
	if err != nil {
		return Diff{}, err
	}
	if opts, err = opts.withDefaults(); err != nil {
		return Diff{}, err
	}
	s, o, err := c.start(ctx, r)
	if err != nil {
		return Diff{}, err
	}
	cb, ch, err := resolveBoth(o, s.forge, rb, rh)
	if err != nil {
		return Diff{}, s.wrap(err)
	}
	d := Diff{Base: cb, Head: ch, Changes: []Change{}}
	from := cb
	if opts.Mode == MergeBase {
		if cb != ch {
			if from, err = s.forge.mergeBase(o, cb, ch); err != nil {
				return Diff{}, s.wrap(err)
			}
		}
		d.MergeBase = from
	}
	if from == ch {
		return d, nil
	}
	t := &treeDiff{o: o, b: s.forge, opts: opts, from: from, to: ch, rawCap: 2*opts.MaxChanges + 1}
	t.listMax = max(2*t.rawCap, minSubtreeList)
	if err := t.run(); err != nil {
		return Diff{}, s.wrap(err)
	}
	d.Changes, d.Truncated = t.result()
	if opts.Patches {
		if err := t.patches(d.Changes, c.limits.MaxBlobBytes); err != nil {
			return Diff{}, s.wrap(err)
		}
	}
	return d, nil
}

// treeDiff is the state of one Compare: a walk from the trees of commit
// from to those of commit to.
type treeDiff struct {
	o        *op
	b        backend
	opts     CompareOptions
	from, to OID
	// raw holds additions, deletions, modifications and type changes;
	// result pairs renames.
	raw []Change
	// rawCap stops the walk: that many raw changes pair into more than
	// MaxChanges changes.
	rawCap int
	// listMax caps one recursive listing.
	listMax   int
	truncated bool
}

// run walks both trees from the root.
func (t *treeDiff) run() error {
	err := t.walkRoot()
	switch {
	case errors.Is(err, errStop):
		return nil
	case exhausted(err):
		t.truncated = true
		return nil
	}
	return err
}

// walkRoot compares the root trees, skipping them if their IDs match.
func (t *treeDiff) walkRoot() error {
	if t.b.capabilities().TreeIDs {
		a, err := t.b.commit(t.o, t.from)
		if err != nil {
			return err
		}
		b, err := t.b.commit(t.o, t.to)
		if err != nil {
			return err
		}
		if a.Tree != "" && a.Tree == b.Tree {
			return nil
		}
	}
	return t.walk("", true, true)
}

// list returns the entries of directory dir in commit, by name. A
// listing cut short stops the walk.
func (t *treeDiff) list(commit OID, dir string) (map[string]Entry, error) {
	tr, err := t.b.tree(t.o, commit, dir, false, defaultMaxEntries)
	if err != nil {
		// Git may store empty subtrees, which some forges call missing.
		if dir != "" && errors.Is(err, ErrPathNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if tr.Truncated {
		t.truncated = true
		return nil, errStop
	}
	m := make(map[string]Entry, len(tr.Entries))
	for _, e := range tr.Entries {
		m[e.Name] = e
	}
	return m, nil
}

// walk compares directory dir, which exists in from if inFrom and in to
// if inTo, and descends into subdirectories whose IDs differ.
func (t *treeDiff) walk(dir string, inFrom, inTo bool) error {
	var old, cur map[string]Entry
	var err error
	if inFrom {
		if old, err = t.list(t.from, dir); err != nil {
			return err
		}
	}
	if inTo {
		if cur, err = t.list(t.to, dir); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(old)+len(cur))
	for n := range old {
		names = append(names, n)
	}
	for n := range cur {
		if _, ok := old[n]; !ok {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	for _, n := range names {
		p := n
		if dir != "" {
			p = dir + "/" + n
		}
		sel := t.selected(p)
		if !sel && !t.ancestor(p) {
			continue
		}
		a, inA := old[n]
		b, inB := cur[n]
		if inA && inB && a.ID == b.ID && a.Mode == b.Mode {
			continue
		}
		aDir, bDir := inA && a.Mode == ModeTree, inB && b.Mode == ModeTree
		if aDir && bDir {
			if err := t.walk(p, true, true); err != nil {
				return err
			}
			continue
		}
		if aDir {
			if err := t.oneSide(p, true); err != nil {
				return err
			}
			inA = false
		}
		if bDir {
			if err := t.oneSide(p, false); err != nil {
				return err
			}
			inB = false
		}
		if !sel || (!inA && !inB) {
			continue
		}
		var c Change
		switch {
		case !inA:
			c = Change{Status: Added, Path: p, Mode: b.Mode, ID: b.ID}
		case !inB:
			c = Change{Status: Deleted, OldPath: p, OldMode: a.Mode, OldID: a.ID}
		default:
			c = Change{Status: Modified, OldPath: p, Path: p, OldMode: a.Mode, Mode: b.Mode, OldID: a.ID, ID: b.ID}
			if kindOf(a.Mode) != kindOf(b.Mode) {
				c.Status = TypeChanged
			}
		}
		if err := t.add(c); err != nil {
			return err
		}
	}
	return nil
}

// oneSide handles directory p that exists only in from (deleted) or only
// in to. A selected directory is listed recursively; one that only leads
// to a selected path is walked.
func (t *treeDiff) oneSide(p string, deleted bool) error {
	switch {
	case t.selected(p):
		return t.subtree(p, deleted)
	case t.ancestor(p):
		return t.walk(p, deleted, !deleted)
	}
	return nil
}

// subtree adds every file below directory p as deleted or added.
func (t *treeDiff) subtree(p string, deleted bool) error {
	commit := t.to
	if deleted {
		commit = t.from
	}
	tr, err := t.b.tree(t.o, commit, p, true, t.listMax)
	if err != nil {
		if errors.Is(err, ErrPathNotFound) {
			return nil // an empty subtree
		}
		return err
	}
	entries := slices.Clone(tr.Entries)
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	for _, e := range entries {
		if e.Mode == ModeTree || !strings.HasPrefix(e.Path, p+"/") {
			continue
		}
		c := Change{Status: Added, Path: e.Path, Mode: e.Mode, ID: e.ID}
		if deleted {
			c = Change{Status: Deleted, OldPath: e.Path, OldMode: e.Mode, OldID: e.ID}
		}
		if err := t.add(c); err != nil {
			return err
		}
	}
	if tr.Truncated {
		t.truncated = true
		return errStop
	}
	return nil
}

// add records a raw change and stops the walk once rawCap is reached.
func (t *treeDiff) add(c Change) error {
	t.raw = append(t.raw, c)
	if len(t.raw) >= t.rawCap {
		t.truncated = true
		return errStop
	}
	return nil
}

// selected reports whether p is at or below one of the Paths filters.
func (t *treeDiff) selected(p string) bool {
	if len(t.opts.Paths) == 0 {
		return true
	}
	for _, f := range t.opts.Paths {
		if p == f || strings.HasPrefix(p, f+"/") {
			return true
		}
	}
	return false
}

// ancestor reports whether p is a directory above one of the Paths
// filters.
func (t *treeDiff) ancestor(p string) bool {
	for _, f := range t.opts.Paths {
		if strings.HasPrefix(f, p+"/") {
			return true
		}
	}
	return false
}

// kindOf groups modes: regular and executable files are one kind, so a
// change between them is Modified.
func kindOf(m FileMode) FileMode {
	if m == ModeExec {
		return ModeFile
	}
	return m
}

// changeKey is the sort key of a change: Path, or OldPath if deleted.
func changeKey(c Change) string {
	if c.Path != "" {
		return c.Path
	}
	return c.OldPath
}

// result pairs renames, sorts the changes and cuts them to MaxChanges.
func (t *treeDiff) result() ([]Change, bool) {
	out := pairRenames(t.raw)
	slices.SortFunc(out, func(a, b Change) int {
		if c := strings.Compare(changeKey(a), changeKey(b)); c != 0 {
			return c
		}
		return strings.Compare(a.OldPath, b.OldPath)
	})
	truncated := t.truncated
	if len(out) > t.opts.MaxChanges {
		out, truncated = out[:t.opts.MaxChanges], true
	}
	return out, truncated
}

// pairRenames turns deletions and additions of the same blob ID and kind
// into renames, one to one. Pairs with the same base name go first; the
// rest pair in path order. Submodules are never paired.
func pairRenames(raw []Change) []Change {
	type key struct {
		id   OID
		kind FileMode
	}
	dels, adds := map[key][]int{}, map[key][]int{}
	for i, c := range raw {
		switch {
		case c.Status == Deleted && c.OldMode != ModeSubmodule:
			k := key{c.OldID, kindOf(c.OldMode)}
			dels[k] = append(dels[k], i)
		case c.Status == Added && c.Mode != ModeSubmodule:
			k := key{c.ID, kindOf(c.Mode)}
			adds[k] = append(adds[k], i)
		}
	}
	used := make([]bool, len(raw))
	var renames []Change
	pair := func(d, a int) {
		used[d], used[a] = true, true
		renames = append(renames, Change{
			Status: Renamed, OldPath: raw[d].OldPath, Path: raw[a].Path,
			OldMode: raw[d].OldMode, Mode: raw[a].Mode, OldID: raw[d].OldID, ID: raw[a].ID,
		})
	}
	for k, ds := range dels {
		as := adds[k]
		if len(as) == 0 {
			continue
		}
		byPath := func(path func(Change) string) func(i, j int) int {
			return func(i, j int) int { return strings.Compare(path(raw[i]), path(raw[j])) }
		}
		slices.SortFunc(ds, byPath(func(c Change) string { return c.OldPath }))
		slices.SortFunc(as, byPath(func(c Change) string { return c.Path }))
		for _, sameName := range []bool{true, false} {
			for _, d := range ds {
				if used[d] {
					continue
				}
				for _, a := range as {
					if !used[a] && (!sameName || path.Base(raw[d].OldPath) == path.Base(raw[a].Path)) {
						pair(d, a)
						break
					}
				}
			}
		}
	}
	out := append([]Change{}, renames...)
	for i, c := range raw {
		if !used[i] {
			out = append(out, c)
		}
	}
	return out
}

// side is one side's content for a patch.
type side struct {
	data []byte
	// full is false if the blob was over the read cap; data then holds
	// only its start.
	full bool
}

// patches fills the patch fields of changes in order, reading blobs by ID.
// Only the current change's content is held, and all reads together stop
// at patchInputBudget bytes.
func (t *treeDiff) patches(changes []Change, maxBlob int64) error {
	readCap := min(int64(patchInputFactor)*int64(t.opts.MaxPatchBytes), maxBlob)
	spent := int64(0) // blob bytes read
	read := func(id OID, mode FileMode) (side, error) {
		if id == "" {
			return side{full: true}, nil
		}
		if mode == ModeSubmodule {
			return side{data: []byte("Subproject commit " + string(id) + "\n"), full: true}, nil
		}
		s, err := t.readBlob(id, readCap)
		spent += int64(len(s.data))
		return s, err
	}
	done, out := 0, false // out: the call budget ran out
	for i := range changes {
		c := &changes[i]
		if c.OldID == c.ID {
			continue // a pure rename or mode change has no hunks
		}
		if out || done >= t.opts.MaxPatchFiles || spent >= patchInputBudget {
			c.PatchOmitted = true
			continue
		}
		done++
		a, err := read(c.OldID, c.OldMode)
		var b side
		if err == nil {
			b, err = read(c.ID, c.Mode)
		}
		switch {
		case exhausted(err):
			out, c.PatchOmitted = true, true
			continue
		case errors.Is(err, ErrTooLarge):
			c.PatchOmitted = true
			continue
		case err != nil:
			return err
		}
		if binary(a.data) || binary(b.data) {
			c.Binary = true
			continue
		}
		if !a.full || !b.full {
			c.PatchOmitted = true
			continue
		}
		patch, adds, dels := linediff.Hunks(a.data, b.data, t.opts.Context)
		c.Additions, c.Deletions = adds, dels
		if len(patch) > t.opts.MaxPatchBytes {
			c.PatchOmitted = true
			continue
		}
		c.Patch = patch
	}
	return nil
}

// readBlob reads blob id, at most readCap bytes, or its start if larger.
func (t *treeDiff) readBlob(id OID, readCap int64) (side, error) {
	rd, err := t.b.readBlob(t.o, id)
	if err != nil {
		return side{}, err
	}
	defer rd.Close()
	n := readCap + 1
	if rd.Size > readCap {
		n = binarySniff
	}
	data, err := io.ReadAll(io.LimitReader(rd, n))
	if err != nil {
		return side{}, err
	}
	if int64(len(data)) > readCap || rd.Size > readCap {
		// Copy the start so the readCap buffer is not kept.
		return side{data: bytes.Clone(data[:min(len(data), binarySniff)])}, nil
	}
	return side{data: data, full: true}, nil
}

// binary reports whether data has a NUL in its first 8000 bytes.
func binary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), binarySniff)], 0) >= 0
}

// withDefaults checks o and fills its defaults. Context becomes the
// number of context lines to use, 0 for none.
func (o CompareOptions) withDefaults() (CompareOptions, error) {
	switch o.Mode {
	case "":
		o.Mode = MergeBase
	case MergeBase, Direct:
	default:
		return o, fmt.Errorf("%w: compare mode %q, want merge_base or direct", ErrInvalid, o.Mode)
	}
	for _, p := range o.Paths {
		if _, err := cleanFilePath(p); err != nil {
			return o, err
		}
	}
	if o.MaxChanges < 0 || o.MaxPatchFiles < 0 || o.MaxPatchBytes < 0 {
		return o, fmt.Errorf("%w: negative compare limit", ErrInvalid)
	}
	switch {
	case o.Context == 0:
		o.Context = defaultContext
	case o.Context < 0:
		o.Context = 0
	}
	o.MaxChanges = cmp(o.MaxChanges, defaultMaxChanges)
	o.MaxPatchFiles = min(cmp(o.MaxPatchFiles, defaultMaxPatchFiles), maxPatchFiles)
	o.MaxPatchBytes = min(cmp(o.MaxPatchBytes, defaultMaxPatchBytes), maxPatchBytes)
	return o, nil
}

// resolveBoth resolves two revisions in parallel.
func resolveBoth(o *op, b backend, x, y revision) (OID, OID, error) {
	var rx, ry Resolved
	var ex, ey error
	var wg sync.WaitGroup
	wg.Go(func() { rx, _, ex = resolve(o, b, x) })
	wg.Go(func() { ry, _, ey = resolve(o, b, y) })
	wg.Wait()
	if ex != nil {
		return "", "", ex
	}
	if ey != nil {
		return "", "", ey
	}
	return rx.Commit, ry.Commit, nil
}
