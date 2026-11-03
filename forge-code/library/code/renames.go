package code

import (
	"path"
	"slices"
	"strings"
)

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
	dels, adds := renameCandidates(raw)
	r := &renamer{raw: raw, used: make([]bool, len(raw))}
	for k, ds := range dels {
		if as := adds[k]; len(as) > 0 {
			r.pairGroup(ds, as)
		}
	}

	out := append([]Change{}, r.renames...)
	for i, c := range raw {
		if !r.used[i] {
			out = append(out, c)
		}
	}

	return out
}

// renameKey groups changes that may pair into a rename.
type renameKey struct {
	id   OID
	kind FileMode
}

// renameCandidates returns the indexes of deletions and additions in raw,
// by blob ID and kind.
func renameCandidates(raw []Change) (dels, adds map[renameKey][]int) {
	dels, adds = map[renameKey][]int{}, map[renameKey][]int{}
	for i, c := range raw {
		switch {
		case c.Status == Deleted && c.OldMode != ModeSubmodule:
			k := renameKey{c.OldID, kindOf(c.OldMode)}
			dels[k] = append(dels[k], i)
		case c.Status == Added && c.Mode != ModeSubmodule:
			k := renameKey{c.ID, kindOf(c.Mode)}
			adds[k] = append(adds[k], i)
		}
	}

	return dels, adds
}

// renamer pairs deletions in raw with additions.
type renamer struct {
	raw     []Change
	used    []bool
	renames []Change
}

// pairGroup pairs the deletions ds with the additions as of one key:
// same base names first, then the rest in path order.
func (r *renamer) pairGroup(ds, as []int) {
	byPath := func(path func(Change) string) func(i, j int) int {
		return func(i, j int) int { return strings.Compare(path(r.raw[i]), path(r.raw[j])) }
	}
	slices.SortFunc(ds, byPath(func(c Change) string { return c.OldPath }))
	slices.SortFunc(as, byPath(func(c Change) string { return c.Path }))
	for _, sameName := range []bool{true, false} {
		for _, d := range ds {
			if r.used[d] {
				continue
			}
			if a, ok := r.match(d, as, sameName); ok {
				r.pair(d, a)
			}
		}
	}
}

// match returns the first unused addition in as for deletion d, with the
// same base name if sameName.
func (r *renamer) match(d int, as []int, sameName bool) (int, bool) {
	for _, a := range as {
		if !r.used[a] && (!sameName || path.Base(r.raw[d].OldPath) == path.Base(r.raw[a].Path)) {
			return a, true
		}
	}

	return 0, false
}

// pair records the rename of deletion d to addition a.
func (r *renamer) pair(d, a int) {
	r.used[d], r.used[a] = true, true
	r.renames = append(r.renames, Change{
		Status:  Renamed,
		OldPath: r.raw[d].OldPath,
		Path:    r.raw[a].Path,
		OldMode: r.raw[d].OldMode,
		Mode:    r.raw[a].Mode,
		OldID:   r.raw[d].OldID,
		ID:      r.raw[a].ID,
	})
}
