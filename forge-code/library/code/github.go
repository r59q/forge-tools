package code

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// githubBackend maps the GitHub REST API, on github.com and GitHub
// Enterprise Server, to the backend seam. It reads Git data through the
// git/* endpoints by object ID, never through contents, which follows
// symlinks and falls back to the default branch.
type githubBackend struct{}

// githubMaxBlob is the largest blob GitHub's blob API serves.
const githubMaxBlob = 100 << 20

// githubMaxPeel caps how many annotated tags one ref may nest.
const githubMaxPeel = 4

// githubMaxRefsBody caps the unpaged git/matching-refs response.
const githubMaxRefsBody = 32 << 20

// githubRawBlob is the media type for raw blob content.
const githubRawBlob = "application/vnd.github.raw+json"

func (githubBackend) capabilities() Capabilities {
	return Capabilities{TreeIDs: true, MergeBase: true}
}

// githubRepo returns the API path of o's repository.
func githubRepo(o *op) (string, error) {
	p, ok := reposPath(o.repo)
	if !ok {
		return "", fmt.Errorf("%w: GitHub repository %q, want owner/name", ErrInvalid, o.repo)
	}
	return p, nil
}

// githubEmpty reports a 409, which GitHub sends for Git data in an empty
// repository ("Git Repository is empty.").
func githubEmpty(err error) bool {
	var fe *forgehttp.Error
	return errors.As(err, &fe) && fe.StatusCode == http.StatusConflict
}

// githubTooLarge reports GitHub's refusal to serve a large blob.
func githubTooLarge(err error) bool {
	var fe *forgehttp.Error
	return errors.As(err, &fe) && (fe.StatusCode == http.StatusForbidden ||
		fe.StatusCode == http.StatusUnprocessableEntity) &&
		strings.Contains(strings.ToLower(fe.Message), "too large")
}

func (githubBackend) defaultBranch(o *op) (string, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return "", err
	}
	var v struct {
		DefaultBranch string `json:"default_branch"`
	}
	err = forgerest.Send(o.ctx, o.s, http.MethodGet, repo, nil, &v)
	return v.DefaultBranch, err
}

// githubSHA is an object reference in GitHub JSON.
type githubSHA struct {
	SHA string `json:"sha"`
}

// githubPerson is an author or committer in GitHub JSON.
type githubPerson struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}

func (p githubPerson) shared() Signature {
	return Signature{Name: p.Name, Email: p.Email, When: p.Date}
}

// githubGitCommit is a git/commits object, and the "commit" field of a
// /commits list item.
type githubGitCommit struct {
	SHA       string       `json:"sha"`
	Tree      githubSHA    `json:"tree"`
	Parents   []githubSHA  `json:"parents"`
	Author    githubPerson `json:"author"`
	Committer githubPerson `json:"committer"`
	Message   string       `json:"message"`
}

// githubListCommit is a /commits or compare list item.
type githubListCommit struct {
	SHA     string          `json:"sha"`
	Commit  githubGitCommit `json:"commit"`
	Parents []githubSHA     `json:"parents"`
}

// githubCommit converts a commit with its ID and parents.
func githubCommit(sha string, c githubGitCommit, parents []githubSHA) (Commit, error) {
	id, err := parseOID(sha)
	if err != nil {
		return Commit{}, err
	}
	tree, err := parseOID(c.Tree.SHA)
	if err != nil {
		return Commit{}, err
	}
	out := Commit{
		ID: id, Tree: tree, Parents: []OID{},
		Author: c.Author.shared(), Committer: c.Committer.shared(), Message: commitMessage(c.Message),
	}
	for _, p := range parents {
		pid, err := parseOID(p.SHA)
		if err != nil {
			return Commit{}, err
		}
		out.Parents = append(out.Parents, pid)
	}
	return out, nil
}

func (githubBackend) commit(o *op, id OID) (Commit, error) {
	return cached(o, "github:commit:"+string(id), func() (Commit, error) {
		repo, err := githubRepo(o)
		if err != nil {
			return Commit{}, err
		}
		var v githubGitCommit
		err = forgerest.Send(o.ctx, o.s, http.MethodGet, repo+"/git/commits/"+string(id), nil, &v)
		if err != nil {
			if githubEmpty(err) {
				return Commit{}, objectMissing(id)
			}
			return Commit{}, missing(err, objectMissing(id))
		}
		c, err := githubCommit(v.SHA, v, v.Parents)
		if err == nil && c.ID != id {
			err = fmt.Errorf("code: GitHub sent commit %s for %s", c.ID, id)
		}
		return c, err
	})
}

// githubObject is the target of a ref or tag in GitHub JSON.
type githubObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// githubRefJSON is a git/ref or git/matching-refs item.
type githubRefJSON struct {
	Ref    string       `json:"ref"`
	Object githubObject `json:"object"`
}

func (githubBackend) ref(o *op, name string) (Ref, bool, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return Ref{}, false, err
	}
	var v githubRefJSON
	path := repo + "/git/ref/" + escapePath(strings.TrimPrefix(name, "refs/"))
	err = forgerest.Send(o.ctx, o.s, http.MethodGet, path, nil, &v)
	switch {
	case isMissing(err) || githubEmpty(err):
		return Ref{}, false, nil
	case err != nil:
		return Ref{}, false, err
	case v.Ref != name:
		return Ref{}, false, nil
	}
	ref, err := githubPeel(o, repo, name, v.Object)
	return ref, err == nil, err
}

// githubPeel returns the ref name pointing at obj, peeling annotated tags
// through git/tags. A dangling tag leaves Commit empty; a chain deeper
// than githubMaxPeel is ErrUnsupported.
func githubPeel(o *op, repo, name string, obj githubObject) (Ref, error) {
	target, err := parseOID(obj.SHA)
	if err != nil {
		return Ref{}, err
	}
	ref := Ref{Name: name, Target: target}
	typ, id := obj.Type, target
	for depth := 0; typ == string(ObjectTag); depth++ {
		if depth == githubMaxPeel {
			return Ref{}, fmt.Errorf("%w: tag %s nests more than %d tags", ErrUnsupported, name, githubMaxPeel)
		}
		var t struct {
			Object githubObject `json:"object"`
		}
		err := forgerest.Send(o.ctx, o.s, http.MethodGet, repo+"/git/tags/"+string(id), nil, &t)
		if isMissing(err) {
			return ref, nil
		}
		if err != nil {
			return Ref{}, err
		}
		typ = t.Object.Type
		if id, err = parseOID(t.Object.SHA); err != nil {
			return Ref{}, err
		}
	}
	if typ == string(ObjectCommit) {
		ref.Commit = id
	}
	return ref, nil
}

// githubMatching reads git/matching-refs for prefix, keeping only names
// that start with it. GitHub does not page this listing and ignores
// per_page, so the whole response is read, capped at githubMaxRefsBody.
func githubMatching(o *op, repo, prefix string) ([]githubRefJSON, error) {
	resp, err := forgerest.Open(o.ctx, o.s, http.MethodGet,
		repo+"/git/matching-refs/"+escapePath(strings.TrimPrefix(prefix, "refs/")),
		http.Header{"Accept": {"application/json"}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, githubMaxRefsBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > githubMaxRefsBody {
		return nil, fmt.Errorf("%w: ref listing over %d bytes", ErrTooLarge, githubMaxRefsBody)
	}
	var all []githubRefJSON
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("code: decode refs: %w", err)
	}
	return slices.DeleteFunc(all, func(v githubRefJSON) bool { return !strings.HasPrefix(v.Ref, prefix) }), nil
}

func (githubBackend) refs(o *op, prefix string, limit int) ([]Ref, bool, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return nil, false, err
	}
	listed, err := githubMatching(o, repo, prefix)
	switch {
	case githubEmpty(err):
		return nil, false, nil
	case exhausted(err):
		return nil, true, nil
	case err != nil:
		return nil, false, err
	}
	slices.SortFunc(listed, func(a, b githubRefJSON) int { return strings.Compare(a.Ref, b.Ref) })
	truncated := false
	if len(listed) > limit {
		listed, truncated = listed[:limit], true
	}

	refs := make([]Ref, len(listed))
	done := make([]bool, len(listed))
	annotated := map[string]int{} // short tag name -> index
	for i, v := range listed {
		target, err := parseOID(v.Object.SHA)
		if err != nil {
			return nil, false, err
		}
		refs[i] = Ref{Name: v.Ref, Target: target}
		switch v.Object.Type {
		case string(ObjectTag):
			annotated[strings.TrimPrefix(v.Ref, tagsPrefix)] = i
			continue
		case string(ObjectCommit):
			refs[i].Commit = target
		}
		done[i] = true
	}
	// Peel annotated tags by joining with /tags, which lists each tag's
	// peeled commit; no call per tag.
	var joined []int
	if len(annotated) > 0 {
		left := len(annotated)
		err := forgerest.Each(o.ctx, o.s, repo+"/tags?per_page=100", func(raw json.RawMessage) (bool, error) {
			var t struct {
				Name   string    `json:"name"`
				Commit githubSHA `json:"commit"`
			}
			if err := json.Unmarshal(raw, &t); err != nil {
				return false, err
			}
			i, ok := annotated[t.Name]
			if !ok || done[i] {
				return true, nil
			}
			c, err := parseOID(t.Commit.SHA)
			if err != nil {
				return false, err
			}
			refs[i].Commit, done[i] = c, true
			joined = append(joined, i)
			left--
			return left > 0, nil
		})
		if err != nil && !exhausted(err) {
			return nil, false, err
		}
	}
	// /tags carries no tag object ID, so a tag moved since the first
	// listing would pair its old object with its new commit. List again:
	// a tag whose object changed is peeled one by one below.
	if len(joined) > 0 {
		again, err := githubMatching(o, repo, prefix)
		if err != nil && !exhausted(err) && !githubEmpty(err) {
			return nil, false, err
		}
		now := map[string]githubObject{}
		for _, v := range again {
			now[v.Ref] = v.Object
		}
		for _, i := range joined {
			obj, ok := now[refs[i].Name]
			if ok && strings.EqualFold(obj.SHA, string(refs[i].Target)) {
				continue
			}
			done[i] = false
			if ok {
				listed[i].Object = obj
			}
		}
	}
	// Tags still missing point to a tree or blob, or moved or went away
	// since the first listing; peel them one by one.
	for i := range refs {
		if done[i] {
			continue
		}
		obj := githubObject{Type: listed[i].Object.Type, SHA: listed[i].Object.SHA}
		ref, err := githubPeel(o, repo, refs[i].Name, obj)
		switch {
		case err == nil:
			refs[i], done[i] = ref, true
		case errors.Is(err, ErrUnsupported):
			done[i] = true
		case exhausted(err):
		default:
			return nil, false, err
		}
	}
	// Out of calls: keep the complete refs before the first incomplete one.
	if i := slices.Index(done, false); i >= 0 {
		return refs[:i], true, nil
	}
	return refs, truncated, nil
}

func (githubBackend) history(o *op, tip OID, path string, n int, pos historyPos) ([]Commit, historyPos, bool, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return nil, pos, false, err
	}
	q := url.Values{
		"sha":      {string(tip)},
		"per_page": {strconv.Itoa(n)},
		"page":     {strconv.Itoa(pos.Page + 1)},
	}
	if path != "" {
		q.Set("path", path)
	}
	var items []githubListCommit
	h, err := forgerest.Do(o.ctx, o.s, http.MethodGet, repo+"/commits?"+q.Encode(), nil, &items)
	if err != nil {
		// GitHub answers 404 for an unknown sha (422 on older versions), and
		// 409 in an empty repository. A 404 may also mean the repository is
		// gone, so check it before blaming the tip.
		if isMissing(err) {
			if _, rerr := (githubBackend{}).defaultBranch(o); rerr != nil {
				return nil, pos, false, rerr
			}
		}
		if githubEmpty(err) || isMissing(err) {
			return nil, pos, false, objectMissing(tip)
		}
		return nil, pos, false, err
	}
	out := make([]Commit, 0, len(items))
	for _, it := range items {
		c, err := githubCommit(it.SHA, it.Commit, it.Parents)
		if err != nil {
			return nil, pos, false, err
		}
		out = append(out, c)
	}
	link, ok := forgehttp.NextPage(h)
	return out, historyPos{Page: pos.Page + 1}, ok && link != "" && len(items) > 0, nil
}

// githubTreeItem is a git/trees entry.
type githubTreeItem struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
	Size *int64 `json:"size"`
}

// githubListing is one git/trees response, with Paths relative to the
// listed tree.
type githubListing struct {
	Entries   []Entry
	Truncated bool
}

// githubTree reads git/trees/{id}, flat or recursive.
func githubTree(o *op, repo string, id OID, recursive bool) (githubListing, error) {
	path := repo + "/git/trees/" + string(id)
	if recursive {
		path += "?recursive=1"
	}
	var v struct {
		Tree      []githubTreeItem `json:"tree"`
		Truncated bool             `json:"truncated"`
	}
	if err := forgerest.Send(o.ctx, o.s, http.MethodGet, path, nil, &v); err != nil {
		return githubListing{}, err
	}
	l := githubListing{Entries: make([]Entry, 0, len(v.Tree)), Truncated: v.Truncated}
	for _, it := range v.Tree {
		mode, err := ParseFileMode(it.Mode)
		if err != nil {
			return githubListing{}, fmt.Errorf("code: GitHub sent mode %q for %q", it.Mode, it.Path)
		}
		eid, err := parseOID(it.SHA)
		if err != nil {
			return githubListing{}, err
		}
		e := Entry{Name: it.Path, Path: it.Path, Mode: mode, Type: typeOf(mode), ID: eid, Size: -1}
		if i := strings.LastIndexByte(it.Path, '/'); i >= 0 {
			e.Name = it.Path[i+1:]
		}
		if e.Type == ObjectBlob && it.Size != nil {
			e.Size = *it.Size
		}
		l.Entries = append(l.Entries, e)
	}
	return l, nil
}

// githubFlat returns the flat listing of tree id, memoised per call.
func githubFlat(o *op, repo string, id OID) (githubListing, error) {
	return cached(o, "github:tree:"+string(id), func() (githubListing, error) {
		return githubTree(o, repo, id, false)
	})
}

// githubDir returns the tree ID of directory dir ("" is the root) in
// commit, walking one listing per path segment.
func (b githubBackend) githubDir(o *op, repo string, commit OID, dir string) (OID, error) {
	c, err := b.commit(o, commit)
	if err != nil {
		return "", err
	}
	id := c.Tree
	if dir == "" {
		return id, nil
	}
	for _, name := range strings.Split(dir, "/") {
		e, err := githubFind(o, repo, id, name)
		if err != nil {
			if errors.Is(err, ErrPathNotFound) {
				return "", pathMissing(dir)
			}
			return "", err
		}
		if e.Mode != ModeTree {
			return "", pathMissing(dir)
		}
		id = e.ID
	}
	return id, nil
}

// githubFind returns the entry name in tree id.
func githubFind(o *op, repo string, id OID, name string) (Entry, error) {
	l, err := githubFlat(o, repo, id)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range l.Entries {
		if e.Name == name {
			return e, nil
		}
	}
	if l.Truncated {
		return Entry{}, fmt.Errorf("%w: GitHub truncated the listing of tree %s", ErrTooLarge, id)
	}
	return Entry{}, pathMissing(name)
}

// githubUnder returns entries with dir prefixed to their Paths.
func githubUnder(dir string, entries []Entry) []Entry {
	out := make([]Entry, len(entries))
	for i, e := range entries {
		if dir != "" {
			e.Path = dir + "/" + e.Path
		}
		out[i] = e
	}
	return out
}

func (b githubBackend) tree(o *op, commit OID, path string, recursive bool, max int) (Tree, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return Tree{}, err
	}
	id, err := b.githubDir(o, repo, commit, path)
	if err != nil {
		return Tree{}, err
	}
	t := Tree{ID: id}
	if !recursive {
		l, err := githubFlat(o, repo, id)
		if err != nil {
			return Tree{}, err
		}
		t.Entries, t.Truncated = githubUnder(path, l.Entries), l.Truncated
		return t, nil
	}
	l, err := githubTree(o, repo, id, true)
	switch {
	case exhausted(err):
		t.Truncated = true
		return t, nil
	case err != nil:
		return Tree{}, err
	case !l.Truncated:
		t.Entries = githubUnder(path, l.Entries)
		return t, nil
	}
	// GitHub cut the recursive listing: walk the subtrees one at a time.
	type dir struct {
		id   OID
		path string
	}
	queue := []dir{{id, path}}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		l, err := githubFlat(o, repo, d.id)
		if exhausted(err) {
			t.Truncated = true
			break
		}
		if err != nil {
			return Tree{}, err
		}
		t.Truncated = t.Truncated || l.Truncated
		for _, e := range githubUnder(d.path, l.Entries) {
			if len(t.Entries) == max {
				t.Truncated = true
				return t, nil
			}
			t.Entries = append(t.Entries, e)
			if e.Mode == ModeTree {
				queue = append(queue, dir{e.ID, e.Path})
			}
		}
	}
	return t, nil
}

func (b githubBackend) stat(o *op, commit OID, path string) (Entry, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return Entry{}, err
	}
	dir, name := "", path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		dir, name = path[:i], path[i+1:]
	}
	id, err := b.githubDir(o, repo, commit, dir)
	if err == nil {
		var e Entry
		if e, err = githubFind(o, repo, id, name); err == nil {
			e.Path = path
			return e, nil
		}
	}
	if errors.Is(err, ErrPathNotFound) {
		return Entry{}, pathMissing(path)
	}
	return Entry{}, err
}

func (b githubBackend) readFile(o *op, commit OID, path string) (*Reader, error) {
	e, err := b.stat(o, commit, path)
	if err != nil {
		return nil, err
	}
	if e.Mode == ModeTree || e.Mode == ModeSubmodule {
		return nil, fmt.Errorf("%w: %q is a %s", ErrNotFile, path, e.Type)
	}
	if e.Size > o.limits.MaxBlobBytes || e.Size > githubMaxBlob {
		return nil, fmt.Errorf("%w: %q has %d bytes", ErrTooLarge, path, e.Size)
	}
	r, err := githubOpenBlob(o, e.ID)
	if err != nil {
		return nil, missing(err, pathMissing(path))
	}
	if e.Size >= 0 {
		r.Size = e.Size
	}
	return r, nil
}

func (githubBackend) readBlob(o *op, id OID) (*Reader, error) {
	r, err := githubOpenBlob(o, id)
	if err != nil {
		return nil, missing(err, objectMissing(id))
	}
	return r, nil
}

// githubOpenBlob streams blob id with the raw media type.
func githubOpenBlob(o *op, id OID) (*Reader, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return nil, err
	}
	resp, err := forgerest.Open(o.ctx, o.s, http.MethodGet, repo+"/git/blobs/"+string(id),
		http.Header{"Accept": {githubRawBlob}})
	if err != nil {
		if githubTooLarge(err) {
			return nil, fmt.Errorf("%w: GitHub serves blobs up to 100 MB: %v", ErrTooLarge, err)
		}
		return nil, err
	}
	// A JSON body would be base64 content, not the blob.
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "application/json" {
		resp.Body.Close()
		return nil, errors.New("code: GitHub ignored the raw media type for a blob")
	}
	return &Reader{Blob: Blob{ID: id, Size: resp.ContentLength}, ReadCloser: resp.Body}, nil
}

func (githubBackend) mergeBase(o *op, a, b OID) (OID, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return "", err
	}
	var v struct {
		MergeBase githubSHA `json:"merge_base_commit"`
	}
	path := repo + "/compare/" + string(a) + "..." + string(b) + "?per_page=1"
	if err := forgerest.Send(o.ctx, o.s, http.MethodGet, path, nil, &v); err != nil {
		// Both commits exist, so a 404 means they share no history.
		if isMissing(err) {
			return "", errNoMergeBase
		}
		return "", err
	}
	return parseOID(v.MergeBase.SHA)
}
