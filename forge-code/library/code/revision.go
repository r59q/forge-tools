package code

import (
	"fmt"
	"strconv"
	"strings"
)

// maxStep caps N in one ~N or ^N suffix.
const maxStep = 100

// maxRevBytes caps the length of a revision.
const maxRevBytes = maxRefBytes + 256

// revision is a parsed revision. Exactly one of oid, head, ref and bare
// is set.
type revision struct {
	text  string // the revision as given, for errors
	oid   OID    // a full object ID
	head  bool   // HEAD: the default branch
	ref   string // a full name: refs/heads/X or refs/tags/X
	bare  string // a bare name X: refs/tags/X or refs/heads/X
	steps []step // suffixes, in order
}

// step is one suffix: ~n (first parent n times) or ^n (n-th parent; ^0 is
// the commit itself).
type step struct {
	parent bool // ^n if true, ~n otherwise
	n      int
}

// parseRevision parses the revision grammar shared by every forge: a
// full object ID, HEAD, refs/heads/X, refs/tags/X or a bare name, followed
// by any number of ~N or ^N suffixes with N <= 100 (a bare ~ or ^ means
// 1). An empty revision is HEAD. Anything else, including short object
// IDs, ranges, reflog forms and other ref namespaces, wraps ErrInvalid.
func parseRevision(text string) (revision, error) {
	rev := revision{text: text}
	if text == "" {
		rev.head = true
		return rev, nil
	}
	bad := func(why string) (revision, error) {
		return revision{}, fmt.Errorf("%w: revision %q: %s", ErrInvalid, text, why)
	}
	if len(text) > maxRevBytes {
		return bad("too long")
	}
	base, suffix := text, ""
	if i := strings.IndexAny(text, "~^"); i >= 0 {
		base, suffix = text[:i], text[i:]
	}
	steps, err := parseSteps(suffix)
	if err != nil {
		return bad(err.Error())
	}
	rev.steps = steps
	switch {
	case base == "HEAD":
		rev.head = true
	case isHex(base) && (len(base) == 40 || len(base) == 64):
		rev.oid = OID(strings.ToLower(base))
	case isHex(base) && len(base) >= 4:
		return bad("short object IDs are not supported; use the full ID or refs/heads/X")
	case strings.HasPrefix(base, "refs/"):
		if err := checkRefName(base); err != nil {
			return revision{}, err
		}
		rev.ref = base
	default:
		if err := checkRefName(headsPrefix + base); err != nil {
			return bad("not a full object ID, HEAD, refs/heads/X, refs/tags/X or valid name")
		}
		rev.bare = base
	}
	return rev, nil
}

// parseSteps parses a run of ~N and ^N suffixes.
func parseSteps(s string) ([]step, error) {
	var steps []step
	for s != "" {
		st := step{parent: s[0] == '^', n: 1}
		if s[0] != '~' && s[0] != '^' {
			return nil, fmt.Errorf("unexpected %q", s)
		}
		s = s[1:]
		end := 0
		for end < len(s) && s[end] >= '0' && s[end] <= '9' {
			end++
		}
		if end > 0 {
			n, err := strconv.Atoi(s[:end])
			if err != nil || n > maxStep || end > 3 {
				return nil, fmt.Errorf("step %q over %d", s[:end], maxStep)
			}
			st.n = n
		}
		s = s[end:]
		steps = append(steps, st)
	}
	return steps, nil
}

// isHex reports whether s is non-empty and only hex digits, in any case.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i] | 0x20 // lowercase letters; digits keep their value
		if (s[i] < '0' || s[i] > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
