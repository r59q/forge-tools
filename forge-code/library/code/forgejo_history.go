package code

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// forgejoLog reads one page of commits reachable from sha and not from
// not, changing path if set. Its page size is limit; with a path it must
// be forgejoCommitPage. A 404 with a path means no commit changes it.
func forgejoLog(o *op, sha OID, path string, not OID, page, limit int) ([]Commit, bool, error) {
	q := url.Values{
		"sha":   {string(sha)},
		"page":  {strconv.Itoa(page)},
		"limit": {strconv.Itoa(limit)},
	}
	if path != "" {
		// The forge hands path to git as a pathspec; match it literally.
		q.Set("path", ":(literal)"+path)
	}
	if not != "" {
		q.Set("not", string(not))
	}

	var list []forgejoCommit
	h, err := forgejoGet(o, "/commits?"+q.Encode()+"&"+forgejoLean, &list)
	if err != nil {
		if path != "" && errors.Is(err, forgehttp.ErrNotFound) {
			return nil, false, nil
		}

		return nil, false, missing(err, objectMissing(sha))
	}

	out := make([]Commit, 0, len(list))
	for _, fc := range list {
		c, err := fc.decode()
		if err != nil {
			return nil, false, err
		}

		out = append(out, c)
	}

	more := len(list) == limit
	if v := h.Get("X-HasMore"); v != "" {
		more = v == "true" && len(list) > 0
	}
	if path != "" {
		if err := forgejoCheckPathPage(h, page, limit, len(list), more); err != nil {
			return nil, false, err
		}
	}

	return out, more, nil
}

// forgejoCheckPathPage checks a path-filtered /commits page against the
// forge's headers. With a path the forge skips (page-1) times [git]
// COMMITS_RANGE_SIZE commits but returns at most [api] MAX_RESPONSE_ITEMS,
// so unless both are limit, pages leave gaps. A page size other than
// limit, a short page before the last, or a last page that ends before
// X-Total shows such a gap.
func forgejoCheckPathPage(h http.Header, page, limit, n int, more bool) error {
	gap := func(why string) error {
		return fmt.Errorf("%w: path history on this Forgejo would skip commits (%s); "+
			"it needs [git] COMMITS_RANGE_SIZE and [api] MAX_RESPONSE_ITEMS of %d", ErrUnsupported, why, limit)
	}
	if v := h.Get("X-PerPage"); v != "" && v != strconv.Itoa(limit) {
		return gap("page size " + v)
	}
	if more && n < limit {
		return gap(fmt.Sprintf("page %d has %d commits", page, n))
	}

	total, err := strconv.Atoi(h.Get("X-Total"))
	if err == nil && !more && (page-1)*limit+n < total {
		return gap(fmt.Sprintf("page %d ends before commit %d of %d", page, (page-1)*limit+n, total))
	}

	return nil
}

// history uses pos.Page as the forge page (1-based; 0 is the first) and
// pos.Offset as the number of commits of that page already returned.
func (forgejoBackend) history(o *op, tip OID, path string, n int, pos historyPos) ([]Commit, historyPos, bool, error) {
	page, off := max(pos.Page, 1), pos.Offset
	var out []Commit
	for {
		list, more, err := forgejoLog(o, tip, path, "", page, forgejoCommitPage)
		if err != nil {
			return nil, pos, false, err
		}

		off = min(off, len(list))
		k := min(n-len(out), len(list)-off)
		out = append(out, list[off:off+k]...)
		off += k
		if off < len(list) {
			return out, historyPos{Page: page, Offset: off}, true, nil
		}
		if !more {
			return out, historyPos{Page: page, Offset: off}, false, nil
		}

		page, off = page+1, 0
		if len(out) == n {
			return out, historyPos{Page: page}, true, nil
		}
	}
}

// mergeBase lists S, the commits reachable from b but not from a. If S is
// empty, b is an ancestor of a. Otherwise every best common ancestor is a
// parent of S outside S; those candidates are reduced by ancestry checks,
// and more than one left (a criss-cross merge) is ErrUnsupported.
func (forgejoBackend) mergeBase(o *op, a, b OID) (OID, error) {
	parents, err := forgejoOnlyIn(o, b, a)
	if err != nil {
		return "", err
	}
	if len(parents) == 0 {
		return b, nil
	}

	cands := forgejoCandidates(parents)
	switch {
	case len(cands) == 0:
		return "", errNoMergeBase
	case len(cands) > forgejoMaxBases:
		return "", fmt.Errorf("%w: %d merge base candidates on Forgejo", ErrUnsupported, len(cands))
	}

	var best []OID
	for _, x := range cands {
		redundant, err := forgejoBelowAny(o, x, cands)
		if err != nil {
			return "", err
		}
		if !redundant {
			best = append(best, x)
		}
	}

	if len(best) != 1 {
		return "", fmt.Errorf("%w: %d best merge bases on Forgejo", ErrUnsupported, len(best))
	}

	return best[0], nil
}

// forgejoOnlyIn maps the commits reachable from b but not from a to
// their parents.
func forgejoOnlyIn(o *op, b, a OID) (map[OID][]OID, error) {
	parents := map[OID][]OID{}
	for page := 1; ; page++ {
		list, more, err := forgejoLog(o, b, "", a, page, forgejoCommitPage)
		if err != nil {
			return nil, err
		}

		for _, c := range list {
			parents[c.ID] = c.Parents
		}

		if !more {
			return parents, nil
		}
	}
}

// forgejoCandidates returns the parents of commits in parents that are not
// in it themselves, sorted.
func forgejoCandidates(parents map[OID][]OID) []OID {
	var cands []OID
	for _, ps := range parents {
		for _, p := range ps {
			if _, in := parents[p]; !in && !slices.Contains(cands, p) {
				cands = append(cands, p)
			}
		}
	}

	slices.Sort(cands)

	return cands
}

// forgejoBelowAny reports whether x is an ancestor of another of cands:
// no commit is reachable from x but not from it.
func forgejoBelowAny(o *op, x OID, cands []OID) (bool, error) {
	for _, y := range cands {
		if x == y {
			continue
		}

		list, _, err := forgejoLog(o, x, "", y, 1, 1)
		if err != nil {
			return false, err
		}
		if len(list) == 0 {
			return true, nil
		}
	}

	return false, nil
}
