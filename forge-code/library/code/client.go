package code

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Read defaults and caps.
const (
	defaultRefLimit   = 10000
	defaultPageSize   = 50
	maxPageSize       = 100
	defaultMaxEntries = 100000
)

// Client reads and writes repository content on one or more sources. It
// is safe for concurrent use. Each call has its own call budget and memo;
// nothing is cached between calls.
type Client struct {
	limits  Limits
	sources []*source
	byID    map[string]*source
}

// source is a Source with its forge adapter.
type source struct {
	Source
	forge backend
}

// New returns a Client for the given sources with the default Limits.
// Each source needs a unique, non-empty ID and an open session of a
// supported kind.
func New(sources ...Source) (*Client, error) {
	return NewWithLimits(Limits{}, sources...)
}

// NewWithLimits is like New with custom limits. Zero fields of l take the
// defaults; negative ones are ErrInvalid.
func NewWithLimits(l Limits, sources ...Source) (*Client, error) {
	return newClient(l, func(k forgeauth.Kind) (backend, bool) {
		b, ok := backends[k]
		return b, ok
	}, sources)
}

// newClient builds a Client, picking each source's adapter with lookup.
func newClient(l Limits, lookup func(forgeauth.Kind) (backend, bool), sources []Source) (*Client, error) {
	l, err := l.withDefaults()
	if err != nil {
		return nil, err
	}
	c := &Client{limits: l, byID: map[string]*source{}}
	for _, s := range sources {
		if strings.TrimSpace(s.ID) == "" {
			return nil, fmt.Errorf("%w: source with empty ID", ErrInvalid)
		}
		if _, dup := c.byID[s.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate source %q", ErrInvalid, s.ID)
		}
		if s.Session == nil || s.Session.Client == nil {
			return nil, fmt.Errorf("%w: source %q has no open session", ErrInvalid, s.ID)
		}
		b, ok := lookup(s.Session.Kind)
		if !ok {
			return nil, fmt.Errorf("%w: source %q has unsupported kind %q", ErrInvalid, s.ID, s.Session.Kind)
		}
		src := &source{Source: s, forge: b}
		c.sources = append(c.sources, src)
		c.byID[s.ID] = src
	}
	return c, nil
}

// Sources returns the configured sources in order.
func (c *Client) Sources() []Source {
	out := make([]Source, len(c.sources))
	for i, s := range c.sources {
		out[i] = s.Source
	}
	return out
}

// Capabilities reports what the source supports.
func (c *Client) Capabilities(source string) (Capabilities, error) {
	s, err := c.one(source)
	if err != nil {
		return Capabilities{}, err
	}
	caps := s.forge.capabilities()
	caps.Writes = s.Git != nil
	return caps, nil
}

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

// History returns one page of commits reachable from opts.Revision, in
// the forge's log order. The first page resolves the revision; Next
// continues from the same tip, so paging never drifts when the branch
// moves. Renames are not followed, except on GitLab before 18.10, which
// ignores follow=false and follows them for a single path unless its
// remove_file_commit_history_following flag is on. Path history on a
// Forgejo whose [git] COMMITS_RANGE_SIZE or [api] MAX_RESPONSE_ITEMS is
// not 50 is ErrUnsupported, as the forge would skip commits.
func (c *Client) History(ctx context.Context, r RepositoryRef, opts HistoryOptions) (HistoryPage, error) {
	if opts.PageSize < 0 || opts.PageSize > maxPageSize {
		return HistoryPage{}, fmt.Errorf("%w: page size %d, want 1 to %d", ErrInvalid, opts.PageSize, maxPageSize)
	}
	var cur historyCursor
	var pr revision
	if opts.Cursor != "" {
		var err error
		if cur, err = decodeCursor(opts.Cursor); err != nil {
			return HistoryPage{}, err
		}
		if opts.PageSize != 0 && opts.PageSize != cur.N {
			return HistoryPage{}, fmt.Errorf("%w: page size differs from the cursor's", ErrInvalid)
		}
		if opts.Path != "" && opts.Path != cur.Path {
			return HistoryPage{}, fmt.Errorf("%w: path differs from the cursor's", ErrInvalid)
		}
	} else {
		path, err := cleanDir(opts.Path)
		if err != nil {
			return HistoryPage{}, err
		}
		if pr, err = parseRevision(opts.Revision); err != nil {
			return HistoryPage{}, err
		}
		cur = historyCursor{V: cursorVersion, Path: path, N: cmp(opts.PageSize, defaultPageSize)}
	}
	s, o, err := c.start(ctx, r)
	if err != nil {
		return HistoryPage{}, err
	}
	if opts.Cursor == "" {
		res, _, err := resolve(o, s.forge, pr)
		if err != nil {
			return HistoryPage{}, s.wrap(err)
		}
		cur.Tip = res.Commit
	}
	pos := historyPos{Page: cur.Page, Offset: cur.Offset}
	commits, next, more, err := s.forge.history(o, cur.Tip, cur.Path, cur.N, pos)
	if err != nil {
		return HistoryPage{}, s.wrap(err)
	}
	page := HistoryPage{Tip: cur.Tip, Commits: append([]Commit{}, commits...)}
	if more {
		if next == pos || !next.valid() {
			return HistoryPage{}, s.wrap(fmt.Errorf("code: history did not advance from page %d offset %d", pos.Page, pos.Offset))
		}
		cur.Page, cur.Offset = next.Page, next.Offset
		page.Next = cur.encode()
	}
	return page, nil
}

// Tree lists a directory at rev. A missing directory, or a path that is
// not a directory, wraps ErrPathNotFound. Listings over MaxEntries or the
// call limit are cut short and report Truncated.
func (c *Client) Tree(ctx context.Context, r RepositoryRef, rev string, opts TreeOptions) (Tree, error) {
	limit := opts.MaxEntries
	switch {
	case limit < 0:
		return Tree{}, fmt.Errorf("%w: negative max entries", ErrInvalid)
	case limit == 0:
		limit = defaultMaxEntries
	}
	path, err := cleanDir(opts.Path)
	if err != nil {
		return Tree{}, err
	}
	pr, err := parseRevision(rev)
	if err != nil {
		return Tree{}, err
	}
	s, o, err := c.start(ctx, r)
	if err != nil {
		return Tree{}, err
	}
	res, _, err := resolve(o, s.forge, pr)
	if err != nil {
		return Tree{}, s.wrap(err)
	}
	t, err := s.forge.tree(o, res.Commit, path, opts.Recursive, limit)
	if err != nil {
		return Tree{}, s.wrap(err)
	}
	t.Commit, t.Path = res.Commit, path
	t.Entries = append([]Entry{}, t.Entries...)
	slices.SortFunc(t.Entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	if len(t.Entries) > limit {
		t.Entries, t.Truncated = t.Entries[:limit], true
	}
	return t, nil
}

// Stat describes the entry at path in rev. It is the one way to see
// symlinks and executable bits on every forge.
func (c *Client) Stat(ctx context.Context, r RepositoryRef, rev, path string) (PathInfo, error) {
	if _, err := cleanFilePath(path); err != nil {
		return PathInfo{}, err
	}
	pr, err := parseRevision(rev)
	if err != nil {
		return PathInfo{}, err
	}
	s, o, err := c.start(ctx, r)
	if err != nil {
		return PathInfo{}, err
	}
	res, _, err := resolve(o, s.forge, pr)
	if err != nil {
		return PathInfo{}, s.wrap(err)
	}
	e, err := s.forge.stat(o, res.Commit, path)
	if err != nil {
		return PathInfo{}, s.wrap(err)
	}
	return PathInfo{Commit: res.Commit, Entry: e}, nil
}

// ReadFile opens the file at path in rev. Symlinks are not followed: a
// symlink's content is its target. A directory or submodule is
// ErrNotFile. The caller must close the Reader.
func (c *Client) ReadFile(ctx context.Context, r RepositoryRef, rev, path string) (*Reader, error) {
	if _, err := cleanFilePath(path); err != nil {
		return nil, err
	}
	pr, err := parseRevision(rev)
	if err != nil {
		return nil, err
	}
	s, o, err := c.start(ctx, r)
	if err != nil {
		return nil, err
	}
	res, _, err := resolve(o, s.forge, pr)
	if err != nil {
		return nil, s.wrap(err)
	}
	rd, err := s.forge.readFile(o, res.Commit, path)
	if err != nil {
		return nil, s.wrap(err)
	}
	rd.Commit, rd.Path = res.Commit, path
	rd, err = finishReader(rd, c.limits.MaxBlobBytes)
	return rd, s.wrap(err)
}

// ReadBlob opens the blob id. A missing blob wraps ErrRevisionNotFound.
// The caller must close the Reader.
func (c *Client) ReadBlob(ctx context.Context, r RepositoryRef, id OID) (*Reader, error) {
	if err := checkOID("blob", id); err != nil {
		return nil, err
	}
	s, o, err := c.start(ctx, r)
	if err != nil {
		return nil, err
	}
	rd, err := s.forge.readBlob(o, id)
	if err != nil {
		return nil, s.wrap(err)
	}
	rd.ID, rd.Commit, rd.Path = id, "", ""
	rd, err = finishReader(rd, c.limits.MaxBlobBytes)
	return rd, s.wrap(err)
}

// start checks r and returns its source and a new op.
func (c *Client) start(ctx context.Context, r RepositoryRef) (*source, *op, error) {
	repo, err := cleanRepo(r.Repository)
	if err != nil {
		return nil, nil, err
	}
	s, err := c.one(r.Source)
	if err != nil {
		return nil, nil, err
	}
	return s, newOp(ctx, s.Session, repo, c.limits), nil
}

// one returns the source with the given ID.
func (c *Client) one(id string) (*source, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: source required", ErrInvalid)
	}
	s, ok := c.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSource, id)
	}
	return s, nil
}

// wrap prefixes err with the source ID.
func (s *source) wrap(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("code: source %s: %w", s.ID, err)
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

// cmp returns v, or def if v is zero.
func cmp(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// History cursor bounds.
const (
	cursorVersion   = 1
	maxCursorBytes  = 8192
	maxCursorPage   = 1000000
	maxCursorOffset = 10000
)

// historyCursor is the decoded form of HistoryPage.Next.
type historyCursor struct {
	V      int    `json:"v"`
	Tip    OID    `json:"tip"`
	Path   string `json:"path"`
	N      int    `json:"n"`
	Page   int    `json:"page"`
	Offset int    `json:"off"`
}

// encode returns the cursor as unpadded URL-safe base64 JSON.
func (c historyCursor) encode() string {
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}

// decodeCursor decodes and checks a cursor. Only the exact encoding the
// library makes is accepted, so a tampered cursor is ErrInvalid.
func decodeCursor(s string) (historyCursor, error) {
	bad := fmt.Errorf("%w: history cursor", ErrInvalid)
	if len(s) > maxCursorBytes {
		return historyCursor{}, bad
	}
	data, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return historyCursor{}, bad
	}
	var c historyCursor
	if json.Unmarshal(data, &c) != nil || c.encode() != s {
		return historyCursor{}, bad
	}
	pos := historyPos{Page: c.Page, Offset: c.Offset}
	if c.V != cursorVersion || !c.Tip.Valid() || c.N < 1 || c.N > maxPageSize || !pos.valid() {
		return historyCursor{}, bad
	}
	if _, err := cleanDir(c.Path); err != nil {
		return historyCursor{}, bad
	}
	return c, nil
}

// valid reports whether p is within the cursor bounds.
func (p historyPos) valid() bool {
	return p.Page >= 0 && p.Page <= maxCursorPage && p.Offset >= 0 && p.Offset <= maxCursorOffset
}
