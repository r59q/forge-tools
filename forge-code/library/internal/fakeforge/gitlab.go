package fakeforge

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// GitLab is a Front for the GitLab REST API (v4) repository endpoints:
//
//	GET  /projects/:id
//	GET  /projects/:id/repository/commits[/:sha]
//	GET  /projects/:id/repository/branches[/:branch]
//	GET  /projects/:id/repository/tags[/:tag]
//	GET  /projects/:id/repository/tree
//	HEAD /projects/:id/repository/files/:path
//	GET  /projects/:id/repository/files/:path[/raw]
//	GET  /projects/:id/repository/blobs/:sha[/raw]
//	GET  /projects/:id/repository/merge_base?refs[]=a&refs[]=b
//
// It reproduces GitLab's quirks: :id is the numeric ID or the %2F-encoded
// path, and file, branch and tag names need their slashes encoded too,
// or the route does not match. Errors are {"message": "404 X Not Found"},
// and a private project without access is "404 Project Not Found". Trees
// carry no tree IDs or blob sizes and page by keyset with an opaque
// page_token. The commit list never sends totals and links a next page
// whenever a page is full, so a listing can end with an empty page.
// commits/:sha peels a tag ID to its commit. Revisions are full or
// unique short SHAs, HEAD, and branch or tag names, tags first as in git.
// Every GET route also answers HEAD.
func GitLab(f *Forge) http.Handler { return &gitlabFront{f: f} }

// gitlabFront serves GitLab for a Forge.
type gitlabFront struct{ f *Forge }

// gitlabTime is GitLab's timestamp format.
const gitlabTime = "2006-01-02T15:04:05.000-07:00"

func (g *gitlabFront) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		WriteMessage(w, http.StatusMethodNotAllowed, "405 Method Not Allowed")
		return
	}
	segs := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	if len(segs) < 2 || segs[0] != "projects" {
		gitlabNotFound(w, "")
		return
	}
	path, repo, ok := g.project(w, segs[1])
	if !ok {
		return
	}
	rest := segs[2:]
	if len(rest) == 0 {
		g.serveProject(w, path, repo)
		return
	}
	if rest[0] != "repository" || len(rest) < 2 {
		gitlabNotFound(w, "")
		return
	}
	// One escaped segment each: GitLab routes never match a raw slash in
	// a name.
	arg := func(i int) (string, bool) {
		v, err := url.PathUnescape(rest[i])
		return v, err == nil && v != ""
	}
	route, n := rest[1], len(rest)
	switch {
	case route == "commits" && n == 2:
		g.serveLog(w, r, repo)
	case route == "commits" && n == 3:
		if sha, ok := arg(2); ok {
			g.serveCommit(w, repo, sha)
			return
		}
		gitlabNotFound(w, "")
	case (route == "branches" || route == "tags") && n == 2:
		g.serveRefs(w, r, repo, route)
	case (route == "branches" || route == "tags") && n == 3:
		if name, ok := arg(2); ok {
			g.serveRef(w, repo, route, name)
			return
		}
		gitlabNotFound(w, "")
	case route == "tree" && n == 2:
		g.serveTree(w, r, repo)
	case route == "files" && (n == 3 || n == 4 && rest[3] == "raw"):
		if p, ok := arg(2); ok {
			g.serveFile(w, r, repo, p, n == 4)
			return
		}
		gitlabNotFound(w, "")
	case route == "blobs" && (n == 3 || n == 4 && rest[3] == "raw"):
		sha, _ := arg(2)
		g.serveBlob(w, repo, sha, n == 4)
	case route == "merge_base" && n == 2:
		g.serveMergeBase(w, r, repo)
	default:
		gitlabNotFound(w, "")
	}
}

// project finds and authorizes the project for an escaped :id segment.
func (g *gitlabFront) project(w http.ResponseWriter, seg string) (string, *Repo, bool) {
	id, err := url.PathUnescape(seg)
	if err != nil {
		gitlabNotFound(w, "Project")
		return "", nil, false
	}
	path := id
	if n, err := strconv.Atoi(id); err == nil {
		p, _, ok := g.f.RepoByID(n)
		if !ok {
			gitlabNotFound(w, "Project")
			return "", nil, false
		}
		path = p
	}
	repo, status := g.f.Authorize(path, false)
	if status != 0 {
		if status == http.StatusNotFound {
			gitlabNotFound(w, "Project")
		} else {
			WriteMessage(w, status, fmt.Sprintf("%d %s", status, http.StatusText(status)))
		}
		return "", nil, false
	}
	return path, repo, true
}

// gitlabNotFound writes GitLab's 404 body, "404 <what> Not Found".
func gitlabNotFound(w http.ResponseWriter, what string) {
	msg := "404 Not Found"
	if what != "" {
		msg = "404 " + what + " Not Found"
	}
	WriteMessage(w, http.StatusNotFound, msg)
}

// gitlabBadRequest writes a Grape validation error.
func gitlabBadRequest(w http.ResponseWriter, msg string) {
	WriteJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
}

func (g *gitlabFront) serveProject(w http.ResponseWriter, path string, repo *Repo) {
	name := path[strings.LastIndexByte(path, '/')+1:]
	vis := "public"
	if repo.Private() {
		vis = "private"
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"id":                  repo.ID(),
		"name":                name,
		"path":                name,
		"path_with_namespace": path,
		"default_branch":      repo.DefaultBranch(),
		"empty_repo":          len(repo.Refs("refs/")) == 0,
		"visibility":          vis,
		"web_url":             "https://gitlab.example/" + path,
	})
}

// gitlabResolve resolves a revision as Gitaly's FindCommit does: HEAD or
// empty, a full or unique short SHA (tags peeled), then refs/tags/X,
// refs/heads/X or a full refs/ name.
func gitlabResolve(repo *Repo, rev string) (gitobj.ID, bool) {
	if rev == "" || rev == "HEAD" {
		rev = "refs/heads/" + repo.DefaultBranch()
	}
	if id, err := gitobj.ParseID(strings.ToLower(rev)); err == nil {
		c, err := repo.PeelCommit(id)
		return c, err == nil
	}
	if len(rev) >= 4 && len(rev) < 40 && gitlabIsHex(rev) {
		var match []gitobj.ID
		for _, id := range repo.IDs() {
			if strings.HasPrefix(id.String(), strings.ToLower(rev)) {
				match = append(match, id)
			}
		}
		if len(match) == 1 {
			c, err := repo.PeelCommit(match[0])
			return c, err == nil
		}
		return gitobj.ZeroID, false
	}
	for _, name := range []string{rev, "refs/tags/" + rev, "refs/heads/" + rev} {
		if !strings.HasPrefix(name, "refs/") {
			continue
		}
		if id, ok := repo.Ref(name); ok {
			c, err := repo.PeelCommit(id)
			return c, err == nil
		}
	}
	return gitobj.ZeroID, false
}

// gitlabIsHex reports whether s is only hex digits, in any case.
func gitlabIsHex(s string) bool {
	for _, c := range strings.ToLower(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// gitlabCommit is GitLab's commit entity.
func gitlabCommit(id gitobj.ID, c gitobj.Commit) map[string]any {
	parents := make([]string, len(c.Parents))
	for i, p := range c.Parents {
		parents[i] = p.String()
	}
	title, _, _ := strings.Cut(c.Message, "\n")
	return map[string]any{
		"id":                id.String(),
		"short_id":          id.String()[:8],
		"created_at":        c.Committer.When.Format(gitlabTime),
		"parent_ids":        parents,
		"title":             title,
		"message":           strings.ToValidUTF8(c.Message, "�"),
		"author_name":       c.Author.Name,
		"author_email":      c.Author.Email,
		"authored_date":     c.Author.When.Format(gitlabTime),
		"committer_name":    c.Committer.Name,
		"committer_email":   c.Committer.Email,
		"committed_date":    c.Committer.When.Format(gitlabTime),
		"trailers":          map[string]string{},
		"extended_trailers": map[string]string{},
		"web_url":           "https://gitlab.example/-/commit/" + id.String(),
	}
}

func (g *gitlabFront) serveCommit(w http.ResponseWriter, repo *Repo, sha string) {
	id, ok := gitlabResolve(repo, sha)
	if !ok {
		gitlabNotFound(w, "Commit")
		return
	}
	c, err := repo.ReadCommit(id)
	if err != nil {
		gitlabNotFound(w, "Commit")
		return
	}
	body := gitlabCommit(id, c)
	body["stats"] = map[string]int{"additions": 0, "deletions": 0, "total": 0}
	WriteJSON(w, http.StatusOK, body)
}

// serveLog lists commits with offset paging. Like GitLab it sends no
// totals and links a next page whenever the page is full.
func (g *gitlabFront) serveLog(w http.ResponseWriter, r *http.Request, repo *Repo) {
	q := r.URL.Query()
	commits := []map[string]any{}
	limit := gitlabInt(q.Get("per_page"), 20, 100)
	page := gitlabInt(q.Get("page"), 1, 0)
	offset := (page - 1) * limit
	if tip, ok := gitlabResolve(repo, q.Get("ref_name")); ok {
		ids, err := repo.Log(tip, LogOptions{Path: strings.Trim(q.Get("path"), "/")})
		if err != nil {
			WriteMessage(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, id := range ids[min(offset, len(ids)):min(offset+limit, len(ids))] {
			c, _ := repo.ReadCommit(id)
			commits = append(commits, gitlabCommit(id, c))
		}
	}
	var links []string
	if page > 1 {
		links = append(links, gitlabLink(r, "prev", "page", strconv.Itoa(page-1)), gitlabLink(r, "first", "page", "1"))
	}
	if len(commits) == limit {
		links = append(links, gitlabLink(r, "next", "page", strconv.Itoa(page+1)))
	}
	if len(links) > 0 {
		w.Header().Set("Link", strings.Join(links, ", "))
	}
	w.Header().Set("X-Page", strconv.Itoa(page))
	w.Header().Set("X-Per-Page", strconv.Itoa(limit))
	WriteJSON(w, http.StatusOK, commits)
}

// gitlabInt parses a positive query value, or returns def; a max over 0
// caps it.
func gitlabInt(s string, def, max int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		n = def
	}
	if max > 0 && n > max {
		n = max
	}
	return n
}

// gitlabLink returns a Link entry for r's URL with one query value set.
func gitlabLink(r *http.Request, rel, key, value string) string {
	u, err := url.Parse(r.RequestURI)
	if err != nil {
		u = &url.URL{Path: r.URL.Path}
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return fmt.Sprintf(`<http://%s%s>; rel="%s"`, r.Host, u.RequestURI(), rel)
}

// gitlabRef is GitLab's branch or tag entity for a ref.
func gitlabRef(repo *Repo, route string, ref NamedRef) map[string]any {
	short := strings.TrimPrefix(strings.TrimPrefix(ref.Name, "refs/heads/"), "refs/tags/")
	var commit any
	if id, err := repo.PeelCommit(ref.ID); err == nil {
		if c, err := repo.ReadCommit(id); err == nil {
			commit = gitlabCommit(id, c)
		}
	}
	if route == "branches" {
		return map[string]any{
			"name":      short,
			"commit":    commit,
			"merged":    false,
			"protected": false,
			"default":   short == repo.DefaultBranch(),
			"can_push":  true,
			"web_url":   "https://gitlab.example/-/tree/" + short,
		}
	}
	tag := map[string]any{
		"name":       short,
		"target":     ref.ID.String(),
		"commit":     commit,
		"message":    "",
		"release":    nil,
		"protected":  false,
		"created_at": nil,
	}
	if t, err := repo.ReadTag(ref.ID); err == nil {
		tag["message"] = t.Message
		if t.Tagger != nil {
			tag["created_at"] = t.Tagger.When.Format(gitlabTime)
		}
	}
	return tag
}

// gitlabRefPrefix maps a route to its ref namespace.
func gitlabRefPrefix(route string) string {
	if route == "branches" {
		return "refs/heads/"
	}
	return "refs/tags/"
}

func (g *gitlabFront) serveRef(w http.ResponseWriter, repo *Repo, route, name string) {
	full := gitlabRefPrefix(route) + name
	id, ok := repo.Ref(full)
	if !ok {
		what := "Branch"
		if route == "tags" {
			what = "Tag"
		}
		gitlabNotFound(w, what)
		return
	}
	WriteJSON(w, http.StatusOK, gitlabRef(repo, route, NamedRef{Name: full, ID: id}))
}

// serveRefs lists branches by name, or tags by order_by (name or updated,
// the default) and sort, with offset paging.
func (g *gitlabFront) serveRefs(w http.ResponseWriter, r *http.Request, repo *Repo, route string) {
	refs := repo.Refs(gitlabRefPrefix(route))
	if route == "tags" {
		q := r.URL.Query()
		if q.Get("order_by") != "name" {
			when := func(ref NamedRef) time.Time {
				id, err := repo.PeelCommit(ref.ID)
				if err != nil {
					return time.Time{}
				}
				c, _ := repo.ReadCommit(id)
				return c.Committer.When
			}
			slices.SortStableFunc(refs, func(a, b NamedRef) int { return when(a).Compare(when(b)) })
		}
		if q.Get("sort") != "asc" {
			slices.Reverse(refs)
		}
	}
	start, end := Paginate(w, r, len(refs), Paging{SizeParam: "per_page", DefaultSize: 20, MaxSize: 100})
	w.Header().Set("X-Total", strconv.Itoa(len(refs)))
	out := []map[string]any{}
	for _, ref := range refs[start:end] {
		out = append(out, gitlabRef(repo, route, ref))
	}
	WriteJSON(w, http.StatusOK, out)
}

// serveTree lists a directory. pagination=keyset pages with an opaque
// page_token and links a next page only if more entries follow;
// pagination=none with recursive=true sends everything; otherwise offset
// paging applies. Within a page, trees come first, then blobs, then
// submodules, as GitLab sorts them.
func (g *gitlabFront) serveTree(w http.ResponseWriter, r *http.Request, repo *Repo) {
	q := r.URL.Query()
	commit, ok := gitlabResolve(repo, q.Get("ref"))
	if !ok {
		gitlabNotFound(w, "Tree")
		return
	}
	dir := strings.Trim(q.Get("path"), "/")
	e, err := repo.Lookup(commit, dir)
	if err != nil || e.Mode != gitobj.ModeTree {
		gitlabNotFound(w, "Tree")
		return
	}
	list, err := repo.ListTree(e.ID, q.Get("recursive") == "true")
	if err != nil {
		WriteMessage(w, http.StatusInternalServerError, err.Error())
		return
	}
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	var page []PathEntry
	switch {
	case q.Get("pagination") == "keyset":
		limit := gitlabInt(q.Get("per_page"), 20, 100)
		start := 0
		if tok := q.Get("page_token"); tok != "" {
			raw, err := base64.RawURLEncoding.DecodeString(tok)
			i := slices.IndexFunc(list, func(p PathEntry) bool { return prefix+p.Path == string(raw) })
			if err != nil || i < 0 {
				WriteMessage(w, http.StatusBadRequest, "could not find starting OID: "+tok)
				return
			}
			start = i + 1
		}
		page = list[start:]
		if limit < len(page) {
			page = page[:limit]
			last := prefix + page[len(page)-1].Path
			tok := base64.RawURLEncoding.EncodeToString([]byte(last))
			w.Header().Set("Link", gitlabLink(r, "next", "page_token", tok))
		}
	case q.Get("pagination") == "none" && q.Get("recursive") == "true":
		page = list
	default:
		start, end := Paginate(w, r, len(list), Paging{SizeParam: "per_page", DefaultSize: 20, MaxSize: 100})
		w.Header().Set("X-Total", strconv.Itoa(len(list)))
		page = list[start:end]
	}
	rank := func(m gitobj.Mode) int {
		switch m {
		case gitobj.ModeTree:
			return 0
		case gitobj.ModeSubmodule:
			return 2
		}
		return 1
	}
	page = slices.Clone(page)
	slices.SortStableFunc(page, func(a, b PathEntry) int { return rank(a.Mode) - rank(b.Mode) })
	out := []map[string]any{}
	for _, p := range page {
		out = append(out, map[string]any{
			"id":   p.ID.String(),
			"name": p.Name,
			"type": string(p.Mode.Type()),
			"path": prefix + p.Path,
			"mode": fmt.Sprintf("%06o", uint32(p.Mode)),
		})
	}
	WriteJSON(w, http.StatusOK, out)
}

// serveFile serves a file's metadata as X-Gitlab-* headers (HEAD), as
// JSON with base64 content (GET), or raw. ref is required. Directories
// are "404 File Not Found". A submodule is an empty file whose blob_id is
// the submodule's commit, as GitLab sends it.
func (g *gitlabFront) serveFile(w http.ResponseWriter, r *http.Request, repo *Repo, path string, raw bool) {
	q := r.URL.Query()
	ref := q.Get("ref")
	if ref == "" && !raw {
		gitlabBadRequest(w, "ref is missing")
		return
	}
	commit, ok := gitlabResolve(repo, ref)
	if !ok {
		gitlabNotFound(w, "Commit")
		return
	}
	e, err := repo.Lookup(commit, path)
	if err != nil || e.Mode.Type() == gitobj.TypeTree {
		gitlabNotFound(w, "File")
		return
	}
	var data []byte
	if e.Mode != gitobj.ModeSubmodule {
		if data, err = repo.ReadBlob(e.ID); err != nil {
			gitlabNotFound(w, "File")
			return
		}
	}
	last := ""
	if ids, err := repo.Log(commit, LogOptions{Path: path}); err == nil && len(ids) > 0 {
		last = ids[0].String()
	}
	sum := sha256.Sum256(data)
	meta := map[string]any{
		"file_name":        path[strings.LastIndexByte(path, '/')+1:],
		"file_path":        path,
		"size":             len(data),
		"encoding":         "base64",
		"content_sha256":   hex.EncodeToString(sum[:]),
		"ref":              ref,
		"blob_id":          e.ID.String(),
		"commit_id":        commit.String(),
		"last_commit_id":   last,
		"execute_filemode": e.Mode == gitobj.ModeExec,
	}
	h := w.Header()
	for k, v := range meta {
		h.Set("X-Gitlab-"+gitlabDasherize(k), fmt.Sprint(v))
	}
	switch {
	case raw && q.Get("lfs") == "true":
		gitlabNotFound(w, "")
	case raw:
		gitlabWriteRaw(w, data)
	case r.Method == http.MethodHead:
		w.WriteHeader(http.StatusOK)
	default:
		meta["content"] = base64.StdEncoding.EncodeToString(data)
		WriteJSON(w, http.StatusOK, meta)
	}
}

// gitlabDasherize turns "blob_id" into "Blob-Id", as Rails names headers.
func gitlabDasherize(k string) string {
	parts := strings.Split(k, "_")
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "-")
}

// gitlabWriteRaw writes blob content as Workhorse does.
func gitlabWriteRaw(w http.ResponseWriter, data []byte) {
	ct := "text/plain; charset=utf-8"
	if bytes.IndexByte(data, 0) >= 0 {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// serveBlob serves a blob by full ID, as JSON or raw. Other objects are
// "404 Blob Not Found".
func (g *gitlabFront) serveBlob(w http.ResponseWriter, repo *Repo, sha string, raw bool) {
	id, err := gitobj.ParseID(sha)
	var data []byte
	if err == nil {
		data, err = repo.ReadBlob(id)
	}
	if err != nil {
		gitlabNotFound(w, "Blob")
		return
	}
	if raw {
		gitlabWriteRaw(w, data)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"size":     len(data),
		"encoding": "base64",
		"content":  base64.StdEncoding.EncodeToString(data),
		"sha":      id.String(),
	})
}

// serveMergeBase answers merge_base: 400 for fewer than two refs or
// unknown ones, 404 "Merge Base Not Found" for unrelated histories.
func (g *gitlabFront) serveMergeBase(w http.ResponseWriter, r *http.Request, repo *Repo) {
	refs := r.URL.Query()["refs[]"]
	if len(refs) < 2 {
		WriteMessage(w, http.StatusBadRequest, "Provide at least 2 refs")
		return
	}
	var ids []gitobj.ID
	var unknown []string
	for _, ref := range refs {
		id, ok := gitlabResolve(repo, ref)
		if !ok {
			unknown = append(unknown, ref)
		}
		ids = append(ids, id)
	}
	if len(unknown) > 0 {
		noun := "ref"
		if len(unknown) > 1 {
			noun = "refs"
		}
		WriteMessage(w, http.StatusBadRequest, fmt.Sprintf("Could not find %s: %s", noun, strings.Join(unknown, ", ")))
		return
	}
	base := ids[0]
	for _, id := range ids[1:] {
		b, ok, err := repo.MergeBase(base, id)
		if err != nil && !errors.Is(err, ErrNotFound) {
			WriteMessage(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			gitlabNotFound(w, "Merge Base")
			return
		}
		base = b
	}
	c, _ := repo.ReadCommit(base)
	WriteJSON(w, http.StatusOK, gitlabCommit(base, c))
}
