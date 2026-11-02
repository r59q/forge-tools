package fakeforge

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// ForgejoOptions are Forgejo server settings. Zero fields take Forgejo's
// defaults.
type ForgejoOptions struct {
	// MaxBlobSize is [api] DEFAULT_MAX_BLOB_SIZE: git/blobs sends empty
	// content for larger blobs. Default 10 MiB.
	MaxBlobSize int64
	// TreesPerPage is [api] DEFAULT_GIT_TREES_PER_PAGE, the default and
	// maximum per_page of git/trees. Default 1000.
	TreesPerPage int
	// CommitsRangeSize is [git] COMMITS_RANGE_SIZE: the /commits page size
	// cap, and what a path-filtered /commits skips per page. Default 50.
	CommitsRangeSize int
	// MaxResponseItems is [api] MAX_RESPONSE_ITEMS, the largest page size
	// of any listing. Default 50.
	MaxResponseItems int
	// SingleRefObject makes git/refs/{filter} send one object instead of
	// an array when the only match is exactly "refs/"+filter, as older
	// Gitea versions did. Current Forgejo always sends an array.
	SingleRefObject bool
}

// Forgejo API defaults.
const (
	forgejoMaxBlobSize  = 10 << 20
	forgejoTreesPerPage = 1000
	// forgejoRangeSize is [git] COMMITS_RANGE_SIZE and the /commits
	// page size cap.
	forgejoRangeSize = 50
	forgejoPageSize  = 30
	forgejoMaxItems  = 50
)

// ForgejoAPI is a Forgejo API (v1) front with default settings. See
// ForgejoAPIWith.
func ForgejoAPI(f *Forge) http.Handler { return ForgejoAPIWith(ForgejoOptions{})(f) }

// ForgejoAPIWith returns a Forgejo API front. It serves, below
// /repos/{owner}/{name}: the repository, git/commits/{sha}, git/refs[/{filter}],
// git/tags/{sha}, /tags, git/trees/{sha}, git/blobs/{sha}, raw/{path} and
// /commits, with Forgejo's quirks:
//   - git/refs matches by prefix, is not paged and answers 404 for none;
//   - /tags commit.sha is the tag's target peeled one level;
//   - git/trees, git/blobs and git/tags answer 400 for a missing object,
//     and git/trees sets truncated on every page of a long listing;
//   - git/tags also answers 400 for a tag object no refs/tags/* points
//     to directly, such as the inner tag of a chain; git/commits peels
//     any chain of tags;
//   - git/blobs sends content "" over MaxBlobSize and serves any object
//     type, as cat-file does;
//   - raw answers 404 for directories and submodules and sends the blob ID
//     as ETag;
//   - /commits with path skips (page-1)*CommitsRangeSize commits whatever
//     the limit, and answers 404 if no commit changes the path. path is a
//     git pathspec: ":(literal)" turns glob magic off, and otherwise *, ?
//     and [ match paths in the tip's tree.
func ForgejoAPIWith(opts ForgejoOptions) Front {
	if opts.MaxBlobSize <= 0 {
		opts.MaxBlobSize = forgejoMaxBlobSize
	}
	if opts.TreesPerPage <= 0 {
		opts.TreesPerPage = forgejoTreesPerPage
	}
	if opts.CommitsRangeSize <= 0 {
		opts.CommitsRangeSize = forgejoRangeSize
	}
	if opts.MaxResponseItems <= 0 {
		opts.MaxResponseItems = forgejoMaxItems
	}
	return func(f *Forge) http.Handler { return &forgejoFront{f: f, opts: opts} }
}

type forgejoFront struct {
	f    *Forge
	opts ForgejoOptions
}

func (h *forgejoFront) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		WriteMessage(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	segs, ok := forgejoSegments(r.URL.EscapedPath())
	if !ok {
		WriteMessage(w, http.StatusBadRequest, "bad path")
		return
	}
	if len(segs) < 3 || segs[0] != "repos" {
		WriteMessage(w, http.StatusNotFound, "Not Found")
		return
	}
	repo, status := h.f.Authorize(segs[1]+"/"+segs[2], false)
	if status != 0 {
		WriteMessage(w, status, http.StatusText(status))
		return
	}
	base := "/api/v1/repos/" + url.PathEscape(segs[1]) + "/" + url.PathEscape(segs[2])
	rest := segs[3:]
	switch {
	case len(rest) == 0:
		h.repo(w, repo, segs[1], segs[2])
		return
	case len(rest) == 1 && rest[0] == "commits":
		h.commits(w, r, repo, base)
		return
	case len(rest) == 1 && rest[0] == "tags":
		h.tags(w, r, repo, base)
		return
	case len(rest) >= 2 && rest[0] == "raw":
		h.raw(w, r, repo, strings.Join(rest[1:], "/"))
		return
	case len(rest) >= 2 && rest[0] == "git" && rest[1] == "refs":
		h.refs(w, repo, base, strings.Join(rest[2:], "/"))
		return
	case len(rest) == 3 && rest[0] == "git":
		switch rest[1] {
		case "commits":
			h.commit(w, repo, base, rest[2])
			return
		case "tags":
			h.tag(w, repo, base, rest[2])
			return
		case "trees":
			h.trees(w, r, repo, base, rest[2])
			return
		case "blobs":
			h.blob(w, repo, base, rest[2])
			return
		}
	}
	WriteMessage(w, http.StatusNotFound, "Not Found")
}

// forgejoSegments splits an escaped path into unescaped segments, without
// the leading empty one. A trailing slash gives a trailing "".
func forgejoSegments(p string) ([]string, bool) {
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range segs {
		u, err := url.PathUnescape(s)
		if err != nil {
			return nil, false
		}
		segs[i] = u
	}
	return segs, true
}

func (h *forgejoFront) repo(w http.ResponseWriter, repo *Repo, owner, name string) {
	WriteJSON(w, http.StatusOK, map[string]any{
		"id":             repo.ID(),
		"name":           name,
		"full_name":      owner + "/" + name,
		"owner":          map[string]any{"login": owner},
		"private":        repo.Private(),
		"empty":          len(repo.Refs("refs/")) == 0,
		"default_branch": repo.DefaultBranch(),
	})
}

// forgejoResolve resolves a full object ID or a ref name, in Git's DWIM
// order, to an object ID.
func forgejoResolve(repo *Repo, rev string) (gitobj.ID, bool) {
	if id, err := gitobj.ParseID(strings.ToLower(rev)); err == nil {
		_, ok := repo.Object(id)
		return id, ok
	}
	for _, name := range []string{rev, "refs/" + rev, "refs/tags/" + rev, "refs/heads/" + rev} {
		if strings.HasPrefix(name, "refs/") {
			if id, ok := repo.Ref(name); ok {
				return id, true
			}
		}
	}
	return gitobj.ZeroID, false
}

// forgejoDate formats a time as Forgejo does.
func forgejoDate(t time.Time) string { return t.Format(time.RFC3339) }

func forgejoSig(s gitobj.Signature) map[string]any {
	return map[string]any{"name": s.Name, "email": s.Email, "date": forgejoDate(s.When)}
}

// commitJSON is a Forgejo Commit with stat, verification and files off.
func forgejoCommitJSON(base string, id gitobj.ID, c gitobj.Commit) map[string]any {
	parents := []map[string]any{}
	for _, p := range c.Parents {
		parents = append(parents, map[string]any{
			"url": base + "/git/commits/" + p.String(), "sha": p.String(), "created": forgejoDate(c.Committer.When),
		})
	}
	return map[string]any{
		"url":      base + "/git/commits/" + id.String(),
		"sha":      id.String(),
		"created":  forgejoDate(c.Committer.When),
		"html_url": base + "/commit/" + id.String(),
		"commit": map[string]any{
			"url":       base + "/git/commits/" + id.String(),
			"author":    forgejoSig(c.Author),
			"committer": forgejoSig(c.Committer),
			"message":   c.Message,
			"tree": map[string]any{
				"url": base + "/git/trees/" + c.Tree.String(), "sha": c.Tree.String(), "created": forgejoDate(c.Committer.When),
			},
		},
		"author":    nil,
		"committer": nil,
		"parents":   parents,
	}
}

func (h *forgejoFront) commit(w http.ResponseWriter, repo *Repo, base, sha string) {
	if strings.ContainsAny(sha, "~^:") || strings.Contains(sha, "@{") || strings.Contains(sha, "..") {
		WriteMessage(w, http.StatusUnprocessableEntity, "no valid ref or sha: "+sha)
		return
	}
	id, ok := forgejoResolve(repo, sha)
	if ok {
		id, ok = forgejoPeelCommit(repo, id)
	}
	if !ok {
		WriteMessage(w, http.StatusNotFound, "object does not exist [id: "+sha+"]")
		return
	}
	c, _ := repo.ReadCommit(id)
	WriteJSON(w, http.StatusOK, forgejoCommitJSON(base, id, c))
}

// forgejoPathLog is Log for a /commits path, read as git reads a
// pathspec. Glob magic is approximated: a pattern matches paths in the
// tip's tree equal to it or matching it with path.Match, as git matches.
func forgejoPathLog(repo *Repo, tip gitobj.ID, p string, not []gitobj.ID) ([]gitobj.ID, error) {
	if lit, ok := strings.CutPrefix(p, ":(literal)"); ok || !strings.ContainsAny(p, "*?[") {
		if ok {
			p = lit
		}
		return repo.Log(tip, LogOptions{Path: p, Not: not})
	}
	entries, err := repo.ListTree(tip, true)
	if err != nil {
		return nil, err
	}
	match := map[gitobj.ID]bool{}
	for _, e := range entries {
		if ok, _ := path.Match(p, e.Path); !ok && e.Path != p {
			continue
		}
		ids, err := repo.Log(tip, LogOptions{Path: e.Path, Not: not})
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			match[id] = true
		}
	}
	all, err := repo.Log(tip, LogOptions{Not: not})
	return slices.DeleteFunc(all, func(id gitobj.ID) bool { return !match[id] }), err
}

func forgejoPeelCommit(repo *Repo, id gitobj.ID) (gitobj.ID, bool) {
	c, err := repo.PeelCommit(id)
	return c, err == nil
}

func (h *forgejoFront) refs(w http.ResponseWriter, repo *Repo, base, filter string) {
	prefix := "refs/"
	if filter != "" {
		prefix += filter
	}
	var out []map[string]any
	var names []string
	for _, ref := range repo.Refs(prefix) {
		if strings.HasPrefix(ref.Name, "refs/remotes/") {
			continue
		}
		o, ok := repo.Object(ref.ID)
		typ := ""
		if ok {
			typ = string(o.Type)
		}
		kind := map[string]string{"tag": "tags", "commit": "commits", "tree": "trees", "blob": "blobs"}[typ]
		out = append(out, map[string]any{
			"ref": ref.Name,
			"url": base + "/git/" + ref.Name,
			"object": map[string]any{
				"type": typ, "sha": ref.ID.String(), "url": base + "/git/" + kind + "/" + ref.ID.String(),
			},
		})
		names = append(names, ref.Name)
	}
	switch {
	case len(out) == 0:
		WriteMessage(w, http.StatusNotFound, "Not Found")
	case h.opts.SingleRefObject && len(out) == 1 && names[0] == "refs/"+filter:
		WriteJSON(w, http.StatusOK, out[0])
	default:
		WriteJSON(w, http.StatusOK, out)
	}
}

func (h *forgejoFront) tag(w http.ResponseWriter, repo *Repo, base, sha string) {
	id, err := gitobj.ParseID(strings.ToLower(sha))
	var t gitobj.Tag
	if err == nil {
		t, err = repo.ReadTag(id)
	}
	if err == nil && !slices.ContainsFunc(repo.Refs("refs/tags/"), func(r NamedRef) bool { return r.ID == id }) {
		err = errNoTagRef
	}
	if err != nil {
		WriteMessage(w, http.StatusBadRequest, "GetAnnotatedTag: object does not exist")
		return
	}
	v := map[string]any{
		"tag":     t.Name,
		"sha":     id.String(),
		"url":     base + "/git/tags/" + id.String(),
		"message": t.Message,
		"object": map[string]any{
			"type": string(t.Type), "sha": t.Object.String(), "url": base + "/git/" + string(t.Type) + "s/" + t.Object.String(),
		},
	}
	if t.Tagger != nil {
		v["tagger"] = forgejoSig(*t.Tagger)
	}
	WriteJSON(w, http.StatusOK, v)
}

// errNoTagRef is Forgejo's GetTagNameBySHA failing: no tag ref points to
// the tag object.
var errNoTagRef = errors.New("no tag ref points to the object")

func (h *forgejoFront) tags(w http.ResponseWriter, r *http.Request, repo *Repo, base string) {
	type tagInfo struct {
		v    map[string]any
		when time.Time
	}
	var all []tagInfo
	for _, ref := range repo.Refs("refs/tags/") {
		name := strings.TrimPrefix(ref.Name, "refs/tags/")
		o, ok := repo.Object(ref.ID)
		if !ok {
			continue
		}
		target, msg := ref.ID, ""
		var when time.Time
		switch o.Type {
		case gitobj.TypeTag:
			t, err := repo.ReadTag(ref.ID)
			if err != nil {
				continue
			}
			target, msg = t.Object, strings.TrimSpace(t.Message)
			if t.Tagger != nil {
				when = t.Tagger.When
			}
		case gitobj.TypeCommit:
			c, _ := repo.ReadCommit(ref.ID)
			when = c.Committer.When
		}
		all = append(all, tagInfo{when: when, v: map[string]any{
			"name":    name,
			"message": msg,
			"id":      ref.ID.String(),
			"commit": map[string]any{
				"url": base + "/git/commits/" + ref.ID.String(), "sha": target.String(), "created": forgejoDate(when),
			},
			"zipball_url": base + "/archive/" + name + ".zip",
			"tarball_url": base + "/archive/" + name + ".tar.gz",
		}})
	}
	slices.SortStableFunc(all, func(a, b tagInfo) int { return b.when.Compare(a.when) })
	w.Header().Set("X-Total-Count", strconv.Itoa(len(all)))
	start, end := Paginate(w, r, len(all), Paging{SizeParam: "limit", DefaultSize: forgejoPageSize, MaxSize: forgejoMaxItems})
	out := []map[string]any{}
	for _, t := range all[start:end] {
		out = append(out, t.v)
	}
	WriteJSON(w, http.StatusOK, out)
}

func (h *forgejoFront) trees(w http.ResponseWriter, r *http.Request, repo *Repo, base, sha string) {
	q := r.URL.Query()
	root, ok := forgejoResolve(repo, sha)
	var entries []PathEntry
	var rootEntry gitobj.TreeEntry
	var err error
	if ok {
		if rootEntry, err = repo.Lookup(root, ""); err == nil {
			recursive, _ := strconv.ParseBool(q.Get("recursive"))
			entries, err = repo.ListTree(root, recursive)
		}
	}
	if !ok || err != nil {
		WriteMessage(w, http.StatusBadRequest, "sha not found: "+sha)
		return
	}
	perPage, _ := strconv.Atoi(q.Get("per_page"))
	if perPage <= 0 || perPage > h.opts.TreesPerPage {
		perPage = h.opts.TreesPerPage
	}
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	resp := map[string]any{
		"sha":         rootEntry.ID.String(),
		"url":         base + "/git/trees/" + rootEntry.ID.String(),
		"tree":        nil,
		"truncated":   false,
		"page":        page,
		"total_count": len(entries),
	}
	start := perPage * (page - 1)
	if start < len(entries) {
		// Forgejo's bug: truncated is set on every page, the last too.
		resp["truncated"] = len(entries) > perPage
		end := min(start+perPage, len(entries))
		var list []map[string]any
		for _, e := range entries[start:end] {
			typ := e.Mode.Type()
			u := base + "/git/blobs/" + e.ID.String()
			switch typ {
			case gitobj.TypeTree:
				u = base + "/git/trees/" + e.ID.String()
			case gitobj.TypeCommit:
				u = ""
			}
			list = append(list, map[string]any{
				"path": e.Path, "mode": forgejoMode(e.Mode), "type": string(typ),
				"size": e.Size, "sha": e.ID.String(), "url": u,
			})
		}
		resp["tree"] = list
	}
	WriteJSON(w, http.StatusOK, resp)
}

// forgejoMode formats a mode as %06o.
func forgejoMode(m gitobj.Mode) string {
	s := m.String()
	return strings.Repeat("0", max(0, 6-len(s))) + s
}

func (h *forgejoFront) blob(w http.ResponseWriter, repo *Repo, base, sha string) {
	id, err := gitobj.ParseID(strings.ToLower(sha))
	o, ok := repo.Object(id)
	if err != nil || !ok {
		WriteMessage(w, http.StatusBadRequest, "object does not exist [id: "+sha+"]")
		return
	}
	content := ""
	if int64(len(o.Data)) <= h.opts.MaxBlobSize {
		content = base64.StdEncoding.EncodeToString(o.Data)
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"content":  content,
		"encoding": "base64",
		"url":      base + "/git/blobs/" + id.String(),
		"sha":      id.String(),
		"size":     len(o.Data),
	})
}

func (h *forgejoFront) raw(w http.ResponseWriter, r *http.Request, repo *Repo, p string) {
	rev := r.URL.Query().Get("ref")
	if rev == "" {
		rev = "refs/heads/" + repo.DefaultBranch()
	}
	id, ok := forgejoResolve(repo, rev)
	if ok {
		id, ok = forgejoPeelCommit(repo, id)
	}
	if !ok {
		WriteMessage(w, http.StatusNotFound, "Not Found")
		return
	}
	e, err := repo.Lookup(id, p)
	if err != nil || p == "" || e.Mode == gitobj.ModeTree || e.Mode == gitobj.ModeSubmodule {
		WriteMessage(w, http.StatusNotFound, "Not Found")
		return
	}
	data, err := repo.ReadBlob(e.ID)
	if err != nil {
		WriteMessage(w, http.StatusInternalServerError, err.Error())
		return
	}
	etag := `"` + e.ID.String() + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	kind := "file"
	if e.Mode == gitobj.ModeSymlink {
		kind = "symlink"
	}
	w.Header().Set("X-Forgejo-Object-Type", kind)
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (h *forgejoFront) commits(w http.ResponseWriter, r *http.Request, repo *Repo, base string) {
	q := r.URL.Query()
	sha := q.Get("sha")
	if sha == "" {
		if _, ok := repo.Head(); !ok {
			WriteMessage(w, http.StatusConflict, "repository is empty")
			return
		}
		sha = "refs/heads/" + repo.DefaultBranch()
	}
	tip, ok := forgejoResolve(repo, sha)
	if ok {
		tip, ok = forgejoPeelCommit(repo, tip)
	}
	if !ok {
		WriteMessage(w, http.StatusNotFound, "sha not found")
		return
	}
	var not []gitobj.ID
	for _, n := range q["not"] {
		id, ok := forgejoResolve(repo, n)
		if !ok {
			WriteMessage(w, http.StatusNotFound, "not found: "+n)
			return
		}
		not = append(not, id)
	}
	path := q.Get("path")
	ids, err := forgejoPathLog(repo, tip, path, not)
	if err != nil {
		WriteMessage(w, http.StatusInternalServerError, err.Error())
		return
	}
	if path != "" && len(ids) == 0 {
		WriteMessage(w, http.StatusNotFound, "no commits for path")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = forgejoPageSize
	}
	limit = min(limit, h.opts.MaxResponseItems, h.opts.CommitsRangeSize)
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	skip := (page - 1) * limit
	if path != "" {
		skip = (page - 1) * h.opts.CommitsRangeSize // Forgejo's path-skip bug
	}
	start := min(skip, len(ids))
	end := min(start+limit, len(ids))
	pages := (len(ids) + limit - 1) / limit

	Paginate(w, r, len(ids), Paging{SizeParam: "limit", DefaultSize: limit, MaxSize: limit})
	hd := w.Header()
	hd.Set("X-Total", strconv.Itoa(len(ids)))
	hd.Set("X-Total-Count", strconv.Itoa(len(ids)))
	hd.Set("X-PageCount", strconv.Itoa(pages))
	hd.Set("X-Page", strconv.Itoa(page))
	hd.Set("X-PerPage", strconv.Itoa(limit))
	hd.Set("X-HasMore", strconv.FormatBool(page < pages))
	out := []map[string]any{}
	for _, id := range ids[start:end] {
		c, _ := repo.ReadCommit(id)
		out = append(out, forgejoCommitJSON(base, id, c))
	}
	WriteJSON(w, http.StatusOK, out)
}
