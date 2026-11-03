package code

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Resolve returns the commit rev names. See the package doc for the
// revision grammar; an empty rev is HEAD. A bare name that is both a tag
// and a branch is ErrAmbiguous. A missing revision wraps
// ErrRevisionNotFound and matches forgehttp.ErrNotFound. HEAD is
// ErrUnsupported if the default branch has a name the library refuses,
// such as one with "%".
func (c *Client) Resolve(ctx context.Context, r RepositoryRef, rev string) (Resolved, error) {
	pr, err := parseRevision(rev)
	if err != nil {
		return Resolved{}, err
	}

	s, o, err := c.start(ctx, r)
	if err != nil {
		return Resolved{}, err
	}

	res, _, err := resolve(o, s.forge, pr)

	return res, s.wrap(err)
}

// ListRefs lists branches and tags, sorted by name, with the default
// branch. Branches come first when both are listed and Limit cuts them
// off. The listing is not one atomic snapshot on GitHub and GitLab.
// Default is set even if the library refuses its name (see Resolve).
func (c *Client) ListRefs(ctx context.Context, r RepositoryRef, opts RefsOptions) (RefList, error) {
	limit := opts.Limit
	switch {
	case limit < 0:
		return RefList{}, fmt.Errorf("%w: negative limit", ErrInvalid)
	case limit == 0:
		limit = defaultRefLimit
	}

	prefixes := []string{headsPrefix, tagsPrefix}
	switch opts.Prefix {
	case "":
	case headsPrefix, tagsPrefix:
		prefixes = []string{opts.Prefix}
	default:
		return RefList{}, fmt.Errorf("%w: prefix %q, want refs/heads/, refs/tags/ or empty", ErrInvalid, opts.Prefix)
	}

	s, o, err := c.start(ctx, r)
	if err != nil {
		return RefList{}, err
	}

	list := RefList{Refs: []Ref{}}
	def, err := defaultRef(o, s.forge)
	if err != nil && (def == "" || !errors.Is(err, ErrUnsupported)) {
		return RefList{}, s.wrap(err)
	}

	list.Default = def
	for _, prefix := range prefixes {
		rem := limit - len(list.Refs)
		if list.Truncated {
			break
		}

		// With no room left, ask for one ref to learn if any exist.
		refs, truncated, err := s.forge.refs(o, prefix, max(rem, 1))
		if err != nil {
			return RefList{}, s.wrap(err)
		}

		for _, ref := range refs {
			if !strings.HasPrefix(ref.Name, prefix) {
				continue
			}
			if len(list.Refs) == limit {
				truncated = true
				break
			}

			list.Refs = append(list.Refs, ref)
		}

		list.Truncated = truncated
	}

	slices.SortFunc(list.Refs, func(a, b Ref) int { return strings.Compare(a.Name, b.Name) })

	return list, nil
}

// Commit returns the commit rev names.
func (c *Client) Commit(ctx context.Context, r RepositoryRef, rev string) (Commit, error) {
	pr, err := parseRevision(rev)
	if err != nil {
		return Commit{}, err
	}

	s, o, err := c.start(ctx, r)
	if err != nil {
		return Commit{}, err
	}

	res, cm, err := resolve(o, s.forge, pr)
	if err != nil {
		return Commit{}, s.wrap(err)
	}
	if cm != nil {
		return *cm, nil
	}

	got, err := s.forge.commit(o, res.Commit)

	return got, s.wrap(err)
}

// resolve resolves rev to a commit. It returns the commit object too when
// it had to read it, so callers can skip a second read.
func resolve(o *op, b backend, rev revision) (Resolved, *Commit, error) {
	var res Resolved
	switch {
	case rev.oid != "":
		res.Commit = rev.oid
	case rev.head:
		name, err := defaultRef(o, b)
		if err != nil {
			return Resolved{}, nil, err
		}
		if name == "" {
			return Resolved{}, nil, revisionMissing(rev.text)
		}
		if res, err = lookupRef(o, b, name, rev.text); err != nil {
			return Resolved{}, nil, err
		}
	case rev.ref != "":
		var err error
		if res, err = lookupRef(o, b, rev.ref, rev.text); err != nil {
			return Resolved{}, nil, err
		}
	default:
		var err error
		if res, err = lookupBare(o, b, rev); err != nil {
			return Resolved{}, nil, err
		}
	}

	var last *Commit // the commit object of res.Commit, once read
	read := func() (*Commit, error) {
		if last == nil {
			cm, err := b.commit(o, res.Commit)
			if err != nil {
				return nil, err
			}

			last = &cm
		}

		return last, nil
	}
	if rev.oid != "" {
		// Check that the object ID names a commit.
		if _, err := read(); err != nil {
			return Resolved{}, nil, err
		}
	}

	for _, st := range rev.steps {
		hops, k := st.n, 0 // ~n follows the first parent n times
		if st.parent {
			hops, k = min(st.n, 1), st.n-1 // ^n takes the n-th parent once
		}

		for range hops {
			cm, err := read()
			if err != nil {
				return Resolved{}, nil, err
			}
			if k >= len(cm.Parents) {
				return Resolved{}, nil, revisionMissing(rev.text)
			}

			res.Commit, last = cm.Parents[k], nil
		}
	}

	return res, last, nil
}

// defaultRef returns the default branch's full name, or "". A name Git
// allows but checkRefName refuses, such as one with "%", comes back with
// an error wrapping ErrUnsupported.
func defaultRef(o *op, b backend) (string, error) {
	name, err := b.defaultBranch(o)
	if err != nil || name == "" {
		return "", err
	}

	full := headsPrefix + name
	if checkRefNameGit(full) != nil {
		return "", fmt.Errorf("code: forge sent invalid default branch %q", name)
	}
	if checkRefName(full) != nil {
		return full, fmt.Errorf("%w: default branch %q has a name this library refuses (%% or a leading -); use a commit ID",
			ErrUnsupported, name)
	}

	return full, nil
}

// lookupRef resolves one full ref name to the commit it points to.
func lookupRef(o *op, b backend, name, text string) (Resolved, error) {
	ref, ok, err := b.ref(o, name)
	if err != nil {
		return Resolved{}, err
	}

	return refCommit(ref, ok, name, text)
}

// refCommit returns the commit a found ref points to.
func refCommit(ref Ref, ok bool, name, text string) (Resolved, error) {
	if !ok || !ref.Commit.Valid() {
		return Resolved{}, revisionMissing(text)
	}

	return Resolved{Commit: ref.Commit, Ref: name}, nil
}

// lookupBare looks up a bare name as a tag and as a branch in parallel.
func lookupBare(o *op, b backend, rev revision) (Resolved, error) {
	names := [2]string{tagsPrefix + rev.bare, headsPrefix + rev.bare}
	var refs [2]Ref
	var found [2]bool
	var errs [2]error
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() { refs[i], found[i], errs[i] = b.ref(o, name) })
	}

	wg.Wait()
	if err := errors.Join(errs[:]...); err != nil {
		return Resolved{}, err
	}
	if found[0] && found[1] {
		return Resolved{}, fmt.Errorf("%w: %q is a tag and a branch; use %s or %s",
			ErrAmbiguous, rev.bare, names[0], names[1])
	}

	i := 0
	if found[1] {
		i = 1
	}

	return refCommit(refs[i], found[i], names[i], rev.text)
}
