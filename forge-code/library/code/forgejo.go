package code

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// forgejoBackend maps Forgejo's REST API (v1) to the backend seam. Every
// path is below /repos/{owner}/{name}.
type forgejoBackend struct{}

// Forgejo page sizes and caps.
const (
	// forgejoCommitPage is the /commits page size. It must equal the
	// forge's [git] COMMITS_RANGE_SIZE (50), since with a path filter the
	// forge skips (page-1)*COMMITS_RANGE_SIZE commits whatever the limit.
	// forgejoLog checks the forge's page headers and fails with
	// ErrUnsupported rather than skip commits.
	forgejoCommitPage = 50
	// forgejoTreePage is the git/trees page size, the forge's maximum.
	forgejoTreePage = 1000
	// forgejoTagPage is the /tags page size, the forge's maximum.
	forgejoTagPage = 50
	// forgejoMaxRefsBody caps the unpaged git/refs response.
	forgejoMaxRefsBody = 32 << 20
	// forgejoMaxPeel is how many tag objects ListRefs follows through the
	// /tags listing before it peels the rest with git/commits.
	forgejoMaxPeel = 4
	// forgejoMaxBases caps merge base candidates.
	forgejoMaxBases = 8
)

// forgejoLean turns off the slow parts of Forgejo commit responses.
const forgejoLean = "stat=false&verification=false&files=false"

func (forgejoBackend) capabilities() Capabilities {
	return Capabilities{TreeIDs: true, MergeBase: true}
}

// forgejoRepo returns the API path of o's repository.
func forgejoRepo(o *op) (string, error) {
	p, ok := reposPath(o.repo)
	if !ok {
		return "", fmt.Errorf("%w: repository %q, want owner/name on Forgejo", ErrInvalid, o.repo)
	}
	return p, nil
}

// forgejoGet decodes the JSON response of GET target below the repository.
func forgejoGet(o *op, target string, v any) (http.Header, error) {
	repo, err := forgejoRepo(o)
	if err != nil {
		return nil, err
	}
	return forgerest.Do(o.ctx, o.s, http.MethodGet, repo+target, nil, v)
}

func (forgejoBackend) defaultBranch(o *op) (string, error) {
	var v struct {
		DefaultBranch string `json:"default_branch"`
		Empty         bool   `json:"empty"`
	}
	if _, err := forgejoGet(o, "", &v); err != nil {
		return "", err
	}
	if v.Empty {
		return "", nil
	}
	return v.DefaultBranch, nil
}

// forgejoSig is a commit author or committer.
type forgejoSig struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"`
}

func (s forgejoSig) decode() (Signature, error) {
	t, err := time.Parse(time.RFC3339, s.Date)
	if err != nil {
		return Signature{}, fmt.Errorf("code: forge sent invalid date %q", s.Date)
	}
	return Signature{Name: s.Name, Email: s.Email, When: t}, nil
}

// forgejoSHA is an object reference such as a parent or a tree.
type forgejoSHA struct {
	SHA string `json:"sha"`
}

// forgejoCommit is a commit from git/commits or /commits.
type forgejoCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Author    forgejoSig `json:"author"`
		Committer forgejoSig `json:"committer"`
		Message   string     `json:"message"`
		Tree      forgejoSHA `json:"tree"`
	} `json:"commit"`
	Parents []forgejoSHA `json:"parents"`
}

func (c forgejoCommit) decode() (Commit, error) {
	out := Commit{Parents: make([]OID, 0, len(c.Parents))}
	var err error
	if out.ID, err = parseOID(c.SHA); err != nil {
		return Commit{}, err
	}
	if out.Tree, err = parseOID(c.Commit.Tree.SHA); err != nil {
		return Commit{}, err
	}
	for _, p := range c.Parents {
		id, err := parseOID(p.SHA)
		if err != nil {
			return Commit{}, err
		}
		out.Parents = append(out.Parents, id)
	}
	if out.Author, err = c.Commit.Author.decode(); err != nil {
		return Commit{}, err
	}
	if out.Committer, err = c.Commit.Committer.decode(); err != nil {
		return Commit{}, err
	}
	out.Message = commitMessage(c.Commit.Message)
	return out, nil
}

func (forgejoBackend) commit(o *op, id OID) (Commit, error) {
	return cached(o, "commit:"+string(id), func() (Commit, error) {
		var v forgejoCommit
		if _, err := forgejoGet(o, "/git/commits/"+string(id)+"?"+forgejoLean, &v); err != nil {
			return Commit{}, missing(err, objectMissing(id))
		}
		c, err := v.decode()
		if err != nil {
			return Commit{}, err
		}
		if c.ID != id {
			// The forge peels a tag ID to its commit.
			return Commit{}, objectMissing(id)
		}
		return c, nil
	})
}

// forgejoRef is one entry of git/refs.
type forgejoRef struct {
	Ref    string `json:"ref"`
	Object struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"object"`
}

// decode returns the ref with Commit set if it points to a commit.
func (r forgejoRef) decode() (Ref, error) {
	target, err := parseOID(r.Object.SHA)
	if err != nil {
		return Ref{}, err
	}
	ref := Ref{Name: r.Ref, Target: target}
	if r.Object.Type == "commit" {
		ref.Commit = target
	}
	return ref, nil
}

// forgejoRefs reads git/refs/{filter}. The forge matches refs whose names
// start with "refs/"+filter and answers 404 if none do. It sends an array,
// or in some versions one object for an exact match. The listing is not
// paged, so its size is capped.
func forgejoRefs(o *op, filter string) ([]forgejoRef, error) {
	repo, err := forgejoRepo(o)
	if err != nil {
		return nil, err
	}
	resp, err := forgerest.Open(o.ctx, o.s, http.MethodGet, repo+"/git/refs/"+escapePath(filter),
		http.Header{"Accept": {"application/json"}})
	if errors.Is(err, forgehttp.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, forgejoMaxRefsBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > forgejoMaxRefsBody {
		return nil, fmt.Errorf("%w: ref listing over %d bytes", ErrTooLarge, forgejoMaxRefsBody)
	}
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '{' {
		var one forgejoRef
		if err := json.Unmarshal(data, &one); err != nil {
			return nil, fmt.Errorf("code: decode refs: %w", err)
		}
		return []forgejoRef{one}, nil
	}
	var many []forgejoRef
	if err := json.Unmarshal(data, &many); err != nil {
		return nil, fmt.Errorf("code: decode refs: %w", err)
	}
	return many, nil
}

// forgejoPeel returns the commit the annotated tag id points to, through
// any chain of tags. git/commits peels tag objects itself, while git/tags
// only answers for a tag that a ref points to directly, so a nested tag
// could not be followed with it. It returns "" if the chain ends in a tree
// or blob or the tag is missing.
func forgejoPeel(o *op, id OID) (OID, error) {
	return cached(o, "peel:"+string(id), func() (OID, error) {
		var v forgejoCommit
		if _, err := forgejoGet(o, "/git/commits/"+string(id)+"?"+forgejoLean, &v); err != nil {
			if isMissing(err) {
				return "", nil
			}
			return "", err
		}
		c, err := v.decode()
		if err != nil {
			return "", err
		}
		if c.ID == id {
			return "", fmt.Errorf("code: forge sent commit %s for tag %s", c.ID, id)
		}
		// Keep the commit for a later read in the same call.
		_, _ = cached(o, "commit:"+string(c.ID), func() (Commit, error) { return c, nil })
		return c.ID, nil
	})
}

func (forgejoBackend) ref(o *op, name string) (Ref, bool, error) {
	list, err := forgejoRefs(o, strings.TrimPrefix(name, "refs/"))
	if err != nil {
		return Ref{}, false, err
	}
	for _, fr := range list {
		if fr.Ref != name {
			continue // the forge matched a longer name
		}
		ref, err := fr.decode()
		if err != nil {
			return Ref{}, false, err
		}
		if fr.Object.Type == "tag" {
			if ref.Commit, err = forgejoPeel(o, ref.Target); err != nil {
				return Ref{}, false, err
			}
		}
		return ref, true, nil
	}
	return Ref{}, false, nil
}

func (forgejoBackend) refs(o *op, prefix string, limit int) ([]Ref, bool, error) {
	list, err := forgejoRefs(o, strings.TrimPrefix(prefix, "refs/"))
	if err != nil {
		return nil, false, err
	}
	slices.SortFunc(list, func(a, b forgejoRef) int { return strings.Compare(a.Ref, b.Ref) })
	var refs []Ref
	var tagged []int // indexes of annotated tags in refs
	tagObjs := map[OID]bool{}
	for _, fr := range list {
		if fr.Object.Type == "tag" {
			if id, err := parseOID(fr.Object.SHA); err == nil {
				tagObjs[id] = true
			}
		}
	}
	truncated := false
	for _, fr := range list {
		if !strings.HasPrefix(fr.Ref, prefix) {
			continue
		}
		if len(refs) == limit {
			truncated = true
			break
		}
		ref, err := fr.decode()
		if err != nil {
			return nil, false, err
		}
		if fr.Object.Type == "tag" {
			tagged = append(tagged, len(refs))
		}
		refs = append(refs, ref)
	}
	if len(tagged) == 0 {
		return refs, truncated, nil
	}
	done, err := forgejoPeelTags(o, refs, tagged, tagObjs)
	if err != nil {
		return nil, false, err
	}
	if done < len(refs) {
		return refs[:done], true, nil
	}
	return refs, truncated, nil
}

// forgejoPeelTags sets Commit on the annotated tags refs[i] for i in
// tagged, in order. It joins them by name with the paged /tags listing,
// whose commit.sha is the tag's target, peeled one level; a target that is
// itself a listed tag is followed. A target still known to be a tag, from
// tagObjs (every annotated tag object in git/refs) or a long chain, and
// tags missing from /tags, such as ones added since git/refs, are peeled
// with forgejoPeel. If the call budget runs out it returns how many
// leading refs are complete.
//
// /tags cannot tell a commit from an unlisted tag object, so a tag whose
// target is an annotated tag without a ref of its own reports that tag
// object as its commit.
func forgejoPeelTags(o *op, refs []Ref, tagged []int, tagObjs map[OID]bool) (int, error) {
	want := map[string]int{}
	for _, i := range tagged {
		want[strings.TrimPrefix(refs[i].Name, tagsPrefix)] = i
	}
	targets := map[OID]OID{} // annotated tag object -> target
	found := map[int]OID{}
	repo, err := forgejoRepo(o)
	if err != nil {
		return 0, err
	}
	q := url.Values{"limit": {strconv.Itoa(forgejoTagPage)}}
	err = forgerest.Each(o.ctx, o.s, repo+"/tags?"+q.Encode(), func(raw json.RawMessage) (bool, error) {
		var v struct {
			Name   string     `json:"name"`
			ID     string     `json:"id"`
			Commit forgejoSHA `json:"commit"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return false, err
		}
		id, err := parseOID(v.ID)
		if err != nil {
			return false, err
		}
		target, err := parseOID(v.Commit.SHA)
		if err != nil {
			return false, err
		}
		if id != target {
			targets[id] = target
		}
		if i, ok := want[v.Name]; ok && refs[i].Target == id {
			found[i] = target
		}
		return len(found) < len(tagged), nil
	})
	if err != nil && !exhausted(err) {
		return 0, err
	}
	for _, i := range tagged {
		target, ok := found[i]
		if ok {
			for n := 1; n < forgejoMaxPeel && targets[target] != ""; n++ {
				target = targets[target]
			}
		}
		// Still a tag: missing from /tags, a long chain, or an inner tag
		// whose /tags entry came after the walk stopped.
		if !ok || targets[target] != "" || tagObjs[target] {
			if !ok {
				target = refs[i].Target
			}
			c, err := forgejoPeel(o, target)
			if exhausted(err) {
				return i, nil
			}
			if err != nil {
				return 0, err
			}
			target = c
		}
		refs[i].Commit = target
	}
	return len(refs), nil
}

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
		return fmt.Errorf("%w: path history on this Forgejo would skip commits (%s); it needs [git] COMMITS_RANGE_SIZE and [api] MAX_RESPONSE_ITEMS of %d",
			ErrUnsupported, why, limit)
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

// forgejoTreeEntry is one entry of git/trees.
type forgejoTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

// decode returns the entry with dir prepended to its path.
func (te forgejoTreeEntry) decode(dir string) (Entry, error) {
	mode, err := ParseFileMode(te.Mode)
	if err != nil {
		return Entry{}, fmt.Errorf("code: forge sent invalid mode %q", te.Mode)
	}
	id, err := parseOID(te.SHA)
	if err != nil {
		return Entry{}, err
	}
	if _, err := cleanFilePath(te.Path); err != nil {
		return Entry{}, fmt.Errorf("code: forge sent invalid tree path %q", te.Path)
	}
	p := te.Path
	if dir != "" {
		p = dir + "/" + p
	}
	e := Entry{Name: path.Base(te.Path), Path: p, Mode: mode, Type: typeOf(mode), ID: id, Size: -1}
	if e.Type == ObjectBlob {
		e.Size = te.Size
	}
	return e, nil
}

// forgejoList lists tree id, recursively if asked, with dir prepended to
// every path. It stops after max entries, or when the call budget runs
// out, and then reports truncated. The forge's truncated flag is set on
// every page of a long listing, so paging stops at total_count instead.
func forgejoList(o *op, id OID, dir string, recursive bool, max int) ([]Entry, bool, error) {
	var out []Entry
	for page := 1; ; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(forgejoTreePage)}}
		if recursive {
			q.Set("recursive", "true")
		}
		var v struct {
			Tree       []forgejoTreeEntry `json:"tree"`
			TotalCount int                `json:"total_count"`
		}
		if _, err := forgejoGet(o, "/git/trees/"+string(id)+"?"+q.Encode(), &v); err != nil {
			if exhausted(err) {
				return out, true, nil
			}
			return nil, false, err
		}
		for _, te := range v.Tree {
			if len(out) == max {
				return out, true, nil
			}
			e, err := te.decode(dir)
			if err != nil {
				return nil, false, err
			}
			out = append(out, e)
		}
		if len(v.Tree) == 0 || len(out) >= v.TotalCount {
			return out, false, nil
		}
		if len(out) == max {
			return out, true, nil
		}
	}
}

// forgejoDir returns the complete flat listing of tree id, memoised.
// Entry paths are bare names.
func forgejoDir(o *op, id OID) ([]Entry, error) {
	return cached(o, "tree:"+string(id), func() ([]Entry, error) {
		entries, truncated, err := forgejoList(o, id, "", false, math.MaxInt)
		if err != nil {
			return nil, err
		}
		if truncated {
			return nil, errBudget
		}
		return entries, nil
	})
}

// forgejoWalk returns the tree ID of directory dir ("" is the root) in
// commit, reading one listing per path component.
func forgejoWalk(o *op, commit OID, dir string) (OID, error) {
	c, err := forgejoBackend{}.commit(o, commit)
	if err != nil {
		return "", err
	}
	id := c.Tree
	if dir == "" {
		return id, nil
	}
	for _, name := range strings.Split(dir, "/") {
		entries, err := forgejoDir(o, id)
		if err != nil {
			return "", missing(err, pathMissing(dir))
		}
		i := slices.IndexFunc(entries, func(e Entry) bool { return e.Name == name })
		if i < 0 || entries[i].Mode != ModeTree {
			return "", pathMissing(dir)
		}
		id = entries[i].ID
	}
	return id, nil
}

func (forgejoBackend) tree(o *op, commit OID, path string, recursive bool, max int) (Tree, error) {
	id, err := forgejoWalk(o, commit, path)
	if err != nil {
		return Tree{}, err
	}
	key := "tree:" + string(id)
	if !recursive {
		// Reuse a listing forgejoWalk or forgejoDir already read.
		o.mu.Lock()
		v, ok := o.memo[key]
		o.mu.Unlock()
		if ok {
			names := v.([]Entry)
			entries := make([]Entry, min(len(names), max))
			for i := range entries {
				entries[i] = names[i]
				if path != "" {
					entries[i].Path = path + "/" + names[i].Name
				}
			}
			return Tree{ID: id, Entries: entries, Truncated: len(names) > max}, nil
		}
	}
	entries, truncated, err := forgejoList(o, id, path, recursive, max)
	if err != nil {
		return Tree{}, missing(err, pathMissing(path))
	}
	if !recursive && !truncated {
		// Keep the complete listing, with bare names, for later walks.
		names := slices.Clone(entries)
		for i := range names {
			names[i].Path = names[i].Name
		}
		o.mu.Lock()
		o.memo[key] = names
		o.mu.Unlock()
	}
	return Tree{ID: id, Entries: entries, Truncated: truncated}, nil
}

func (forgejoBackend) stat(o *op, commit OID, p string) (Entry, error) {
	dir, name := "", p
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		dir, name = p[:i], p[i+1:]
	}
	id, err := forgejoWalk(o, commit, dir)
	if errors.Is(err, ErrPathNotFound) {
		return Entry{}, pathMissing(p)
	}
	if err != nil {
		return Entry{}, err
	}
	entries, err := forgejoDir(o, id)
	if err != nil {
		return Entry{}, missing(err, pathMissing(p))
	}
	i := slices.IndexFunc(entries, func(e Entry) bool { return e.Name == name })
	if i < 0 {
		return Entry{}, pathMissing(p)
	}
	e := entries[i]
	e.Path = p
	return e, nil
}

// readFile streams raw/{path}?ref={commit}. The ETag header is the blob
// ID. The forge answers 404 for a directory or submodule too, so a 404 is
// told apart with stat.
func (b forgejoBackend) readFile(o *op, commit OID, p string) (*Reader, error) {
	repo, err := forgejoRepo(o)
	if err != nil {
		return nil, err
	}
	resp, err := forgerest.Open(o.ctx, o.s, http.MethodGet, repo+"/raw/"+escapePath(p)+"?ref="+string(commit), nil)
	if errors.Is(err, forgehttp.ErrNotFound) {
		e, serr := b.stat(o, commit, p)
		if serr != nil {
			return nil, serr
		}
		if e.Type != ObjectBlob {
			return nil, fmt.Errorf("%w: %q is a %s", ErrNotFile, p, e.Type)
		}
		return nil, pathMissing(p)
	}
	if err != nil {
		return nil, err
	}
	id, ok := forgejoETag(resp.Header.Get("ETag"))
	if !ok {
		e, err := b.stat(o, commit, p)
		if err != nil {
			resp.Body.Close()
			return nil, err
		}
		id = e.ID
	}
	if resp.ContentLength > o.limits.MaxBlobBytes {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: blob of %d bytes, limit %d", ErrTooLarge, resp.ContentLength, o.limits.MaxBlobBytes)
	}
	return &Reader{Blob: Blob{ID: id, Size: resp.ContentLength}, ReadCloser: resp.Body}, nil
}

// forgejoETag parses an ETag holding an object ID, such as "\"abc...\"".
func forgejoETag(v string) (OID, bool) {
	v = strings.Trim(strings.TrimPrefix(strings.TrimSpace(v), "W/"), `"`)
	id, err := parseOID(v)
	return id, err == nil
}

// forgejoBlobSlack is room in a git/blobs response for the fields around
// the base64 content.
const forgejoBlobSlack = 64 << 10

// readBlob reads git/blobs/{id}, base64 in JSON. The forge sends empty
// content for blobs over its [api] DEFAULT_MAX_BLOB_SIZE (10 MiB), which
// admins may raise, so the body is decoded here with a cap that follows
// MaxBlobBytes instead of forgerest's 32 MiB.
func (forgejoBackend) readBlob(o *op, id OID) (*Reader, error) {
	repo, err := forgejoRepo(o)
	if err != nil {
		return nil, err
	}
	resp, err := forgerest.Open(o.ctx, o.s, http.MethodGet, repo+"/git/blobs/"+string(id),
		http.Header{"Accept": {"application/json"}})
	if err != nil {
		return nil, missing(err, objectMissing(id))
	}
	defer resp.Body.Close()
	var v struct {
		SHA      string `json:"sha"`
		Size     int64  `json:"size"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	limit := int64(base64.StdEncoding.EncodedLen(int(min(o.limits.MaxBlobBytes, math.MaxInt32)))) + forgejoBlobSlack
	body := &io.LimitedReader{R: resp.Body, N: limit + 1}
	if err := json.NewDecoder(body).Decode(&v); err != nil {
		if body.N <= 0 {
			return nil, fmt.Errorf("%w: blob response over %d bytes, limit %d", ErrTooLarge, limit, o.limits.MaxBlobBytes)
		}
		return nil, fmt.Errorf("code: decode blob %s: %w", id, err)
	}
	if got, err := parseOID(v.SHA); err != nil || got != id {
		return nil, fmt.Errorf("code: forge sent blob %q for %s", v.SHA, id)
	}
	switch {
	case v.Size > o.limits.MaxBlobBytes:
		return nil, fmt.Errorf("%w: blob of %d bytes, limit %d", ErrTooLarge, v.Size, o.limits.MaxBlobBytes)
	case v.Size > 0 && v.Content == "":
		return nil, fmt.Errorf("%w: blob of %d bytes is over the forge's API limit; use ReadFile", ErrTooLarge, v.Size)
	case v.Encoding != "base64":
		return nil, fmt.Errorf("code: forge sent blob encoding %q", v.Encoding)
	}
	dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(v.Content))
	return &Reader{Blob: Blob{ID: id, Size: v.Size}, ReadCloser: io.NopCloser(dec)}, nil
}

// mergeBase lists S, the commits reachable from b but not from a. If S is
// empty, b is an ancestor of a. Otherwise every best common ancestor is a
// parent of S outside S; those candidates are reduced by ancestry checks,
// and more than one left (a criss-cross merge) is ErrUnsupported.
func (forgejoBackend) mergeBase(o *op, a, b OID) (OID, error) {
	parents := map[OID][]OID{}
	for page := 1; ; page++ {
		list, more, err := forgejoLog(o, b, "", a, page, forgejoCommitPage)
		if err != nil {
			return "", err
		}
		for _, c := range list {
			parents[c.ID] = c.Parents
		}
		if !more {
			break
		}
	}
	if len(parents) == 0 {
		return b, nil
	}
	var cands []OID
	for _, ps := range parents {
		for _, p := range ps {
			if _, in := parents[p]; !in && !slices.Contains(cands, p) {
				cands = append(cands, p)
			}
		}
	}
	slices.Sort(cands)
	switch {
	case len(cands) == 0:
		return "", errNoMergeBase
	case len(cands) > forgejoMaxBases:
		return "", fmt.Errorf("%w: %d merge base candidates on Forgejo", ErrUnsupported, len(cands))
	}
	var best []OID
	for _, x := range cands {
		redundant := false
		for _, y := range cands {
			if x == y {
				continue
			}
			// x is an ancestor of y if no commit is reachable from x but
			// not from y.
			list, _, err := forgejoLog(o, x, "", y, 1, 1)
			if err != nil {
				return "", err
			}
			if len(list) == 0 {
				redundant = true
				break
			}
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
