package code

import (
	"context"
	"errors"
	"fmt"
	"sync"
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

	t := &treeDiff{
		o:      o,
		b:      s.forge,
		opts:   opts,
		from:   from,
		to:     ch,
		rawCap: 2*opts.MaxChanges + 1,
	}
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
