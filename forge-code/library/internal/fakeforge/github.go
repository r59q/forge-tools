package fakeforge

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// GitHubOptions tunes the GitHub front.
type GitHubOptions struct {
	// TreeLimit is how many entries a recursive git/trees listing returns
	// before it sets "truncated". 0 means 100000, as on GitHub.
	TreeLimit int
	// MaxBlobBytes is the largest blob git/blobs serves; larger ones get
	// a 403 "too_large". 0 means 100 MiB, as on GitHub.
	MaxBlobBytes int64
}

// GitHub is the GitHub REST front with default options.
func GitHub(f *Forge) http.Handler { return GitHubFront(GitHubOptions{})(f) }

// GitHubFront returns a front for the GitHub REST endpoints forge-code
// reads, with GitHub's response shapes and quirks:
//
//   - GET /repos/{o}/{r}: default_branch.
//   - git/commits/{sha}: 404 if missing, 422 if not a commit. Commit
//     messages here and in lists lose their trailing newline.
//   - git/ref/{ref}: exact match only, 404 if missing.
//   - git/matching-refs/{prefix}: every match in one response; per_page
//     and page are ignored, as on GitHub.
//   - git/tags/{sha}: one annotated tag.
//   - /tags: paged, peeled to commits, sorted by name descending.
//   - /commits?sha=&path=: paged git log; 404 for an unknown sha.
//   - git/trees/{sha}[?recursive=1]: modes as "040000"; takes a tree,
//     commit or ref name and echoes the commit SHA for a commit-ish;
//     recursive listings over TreeLimit are cut and set "truncated".
//   - git/blobs/{sha}: base64 JSON, or raw bytes for Accept
//     application/vnd.github.raw(+json); 403 too_large over MaxBlobBytes.
//   - compare/{base}...{head}: merge_base_commit and paged commits; 404
//     for no common ancestor or two dots. files is always empty.
//
// A repository with no refs is empty: git endpoints and /commits answer
// 409 "Git Repository is empty.".
func GitHubFront(opts GitHubOptions) Front {
	if opts.TreeLimit <= 0 {
		opts.TreeLimit = 100000
	}
	if opts.MaxBlobBytes <= 0 {
		opts.MaxBlobBytes = 100 << 20
	}
	return func(f *Forge) http.Handler {
		g := &githubFront{f: f, opts: opts}
		return http.HandlerFunc(g.serve)
	}
}

type githubFront struct {
	f    *Forge
	opts GitHubOptions
}

// githubReq is one request to a repository endpoint.
type githubReq struct {
	w    http.ResponseWriter
	r    *http.Request
	repo *Repo
	path string // owner/name
	api  string // absolute API URL of the repository
}

func (g *githubFront) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", "4999")
	w.Header().Set("X-RateLimit-Resource", "core")
	segs, ok := githubSegments(r.URL.EscapedPath())
	if !ok || (r.Method != http.MethodGet && r.Method != http.MethodHead) || len(segs) < 3 || segs[0] != "repos" {
		githubError(w, http.StatusNotFound, "Not Found")
		return
	}
	path := segs[1] + "/" + segs[2]
	repo, status := g.f.Authorize(path, false)
	if status != 0 {
		githubError(w, status, http.StatusText(status))
		return
	}
	q := &githubReq{w: w, r: r, repo: repo, path: path,
		api: "http://" + r.Host + APIPrefix(forgeauth.GitHub) + "/repos/" + path}
	rest := segs[3:]
	if len(rest) == 0 {
		g.repository(q)
		return
	}
	empty := len(repo.Refs("refs/")) == 0
	gitEndpoint := rest[0] == "git" || rest[0] == "commits" || rest[0] == "compare"
	if empty && gitEndpoint {
		githubError(w, http.StatusConflict, "Git Repository is empty.")
		return
	}
	tail := func(from int) string { return strings.Join(rest[from:], "/") }
	switch {
	case rest[0] == "git" && len(rest) == 3 && rest[1] == "commits":
		g.gitCommit(q, rest[2])
	case rest[0] == "git" && len(rest) >= 3 && rest[1] == "ref":
		g.ref(q, tail(2))
	case rest[0] == "git" && len(rest) >= 3 && rest[1] == "matching-refs":
		g.matchingRefs(q, tail(2))
	case rest[0] == "git" && len(rest) == 3 && rest[1] == "tags":
		g.tag(q, rest[2])
	case rest[0] == "git" && len(rest) == 3 && rest[1] == "trees":
		g.tree(q, rest[2])
	case rest[0] == "git" && len(rest) == 3 && rest[1] == "blobs":
		g.blob(q, rest[2])
	case rest[0] == "tags" && len(rest) == 1:
		g.tags(q)
	case rest[0] == "commits" && len(rest) == 1:
		g.commits(q)
	case rest[0] == "compare" && len(rest) >= 2:
		g.compare(q, tail(1))
	default:
		githubError(w, http.StatusNotFound, "Not Found")
	}
}

// githubSegments splits an escaped path into unescaped segments.
func githubSegments(escaped string) ([]string, bool) {
	segs := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
	for i, s := range segs {
		u, err := url.PathUnescape(s)
		if err != nil {
			return nil, false
		}
		segs[i] = u
	}
	return segs, true
}

// githubError writes GitHub's error shape.
func githubError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]any{
		"message":           msg,
		"documentation_url": "https://docs.github.com/rest",
		"status":            fmt.Sprint(status),
	})
}

func (g *githubFront) repository(q *githubReq) {
	owner, name, _ := strings.Cut(q.path, "/")
	WriteJSON(q.w, http.StatusOK, map[string]any{
		"id":             q.repo.ID(),
		"name":           name,
		"full_name":      q.path,
		"owner":          map[string]any{"login": owner},
		"private":        q.repo.Private(),
		"default_branch": q.repo.DefaultBranch(),
		"url":            q.api,
	})
}

// resolve finds a commit-ish named by a full SHA, a full ref name, a
// "heads/X" or "tags/X" name, or a bare branch or tag name.
func (q *githubReq) resolve(name string) (gitobj.ID, bool) {
	if id, err := gitobj.ParseID(name); err == nil {
		_, ok := q.repo.Object(id)
		return id, ok
	}
	for _, full := range []string{name, "refs/" + name, "refs/heads/" + name, "refs/tags/" + name} {
		if id, ok := q.repo.Ref(full); ok {
			return id, true
		}
	}
	return gitobj.ZeroID, false
}

// githubPerson is a commit author or committer as GitHub sends it, with
// the date in UTC.
func githubPerson(s gitobj.Signature) map[string]any {
	return map[string]any{"name": s.Name, "email": s.Email, "date": s.When.UTC().Format(time.RFC3339)}
}

func (q *githubReq) parents(c gitobj.Commit) []any {
	out := []any{}
	for _, p := range c.Parents {
		out = append(out, map[string]any{"sha": p.String(), "url": q.api + "/git/commits/" + p.String()})
	}
	return out
}

// gitCommitJSON is a git/commits object.
func (q *githubReq) gitCommitJSON(id gitobj.ID, c gitobj.Commit) map[string]any {
	return map[string]any{
		"sha":          id.String(),
		"url":          q.api + "/git/commits/" + id.String(),
		"author":       githubPerson(c.Author),
		"committer":    githubPerson(c.Committer),
		"message":      strings.TrimSuffix(c.Message, "\n"),
		"tree":         map[string]any{"sha": c.Tree.String(), "url": q.api + "/git/trees/" + c.Tree.String()},
		"parents":      q.parents(c),
		"verification": map[string]any{"verified": false, "reason": "unsigned", "signature": nil, "payload": nil},
	}
}

// listCommitJSON is a commit as /commits and compare list it.
func (q *githubReq) listCommitJSON(id gitobj.ID, c gitobj.Commit) map[string]any {
	inner := q.gitCommitJSON(id, c)
	delete(inner, "sha")
	delete(inner, "parents")
	return map[string]any{
		"sha":     id.String(),
		"url":     q.api + "/commits/" + id.String(),
		"commit":  inner,
		"parents": q.parents(c),
	}
}

func (g *githubFront) gitCommit(q *githubReq, sha string) {
	id, err := gitobj.ParseID(sha)
	if err != nil {
		githubError(q.w, http.StatusNotFound, "Not Found")
		return
	}
	o, ok := q.repo.Object(id)
	if !ok {
		githubError(q.w, http.StatusNotFound, "Not Found")
		return
	}
	if o.Type != gitobj.TypeCommit {
		githubError(q.w, http.StatusUnprocessableEntity, "Object is not a commit")
		return
	}
	c, _ := q.repo.ReadCommit(id)
	WriteJSON(q.w, http.StatusOK, q.gitCommitJSON(id, c))
}

func (q *githubReq) refJSON(name string, id gitobj.ID) map[string]any {
	typ := "commit"
	if o, ok := q.repo.Object(id); ok {
		typ = string(o.Type)
	}
	return map[string]any{
		"ref":    name,
		"url":    q.api + "/git/" + name,
		"object": map[string]any{"type": typ, "sha": id.String(), "url": q.api + "/git/" + typ + "s/" + id.String()},
	}
}

func (g *githubFront) ref(q *githubReq, name string) {
	full := "refs/" + name
	id, ok := q.repo.Ref(full)
	if !ok {
		githubError(q.w, http.StatusNotFound, "Not Found")
		return
	}
	WriteJSON(q.w, http.StatusOK, q.refJSON(full, id))
}

func (g *githubFront) matchingRefs(q *githubReq, prefix string) {
	out := []any{}
	for _, ref := range q.repo.Refs("refs/" + prefix) {
		out = append(out, q.refJSON(ref.Name, ref.ID))
	}
	WriteJSON(q.w, http.StatusOK, out)
}

func (g *githubFront) tag(q *githubReq, sha string) {
	id, err := gitobj.ParseID(sha)
	if err != nil {
		githubError(q.w, http.StatusNotFound, "Not Found")
		return
	}
	t, err := q.repo.ReadTag(id)
	if err != nil {
		githubError(q.w, http.StatusNotFound, "Not Found")
		return
	}
	v := map[string]any{
		"sha":     id.String(),
		"url":     q.api + "/git/tags/" + id.String(),
		"tag":     t.Name,
		"message": t.Message,
		"object": map[string]any{"type": string(t.Type), "sha": t.Object.String(),
			"url": q.api + "/git/" + string(t.Type) + "s/" + t.Object.String()},
		"verification": map[string]any{"verified": false, "reason": "unsigned"},
	}
	if t.Tagger != nil {
		v["tagger"] = githubPerson(*t.Tagger)
	}
	WriteJSON(q.w, http.StatusOK, v)
}

func (g *githubFront) tags(q *githubReq) {
	type tag struct {
		name   string
		commit gitobj.ID
	}
	var list []tag
	for _, ref := range q.repo.Refs("refs/tags/") {
		if c, err := q.repo.PeelCommit(ref.ID); err == nil {
			list = append(list, tag{strings.TrimPrefix(ref.Name, "refs/tags/"), c})
		}
	}
	slices.SortFunc(list, func(a, b tag) int { return strings.Compare(b.name, a.name) })
	start, end := Paginate(q.w, q.r, len(list), Paging{SizeParam: "per_page", DefaultSize: 30, MaxSize: 100})
	out := []any{}
	for _, t := range list[start:end] {
		out = append(out, map[string]any{
			"name":        t.name,
			"commit":      map[string]any{"sha": t.commit.String(), "url": q.api + "/commits/" + t.commit.String()},
			"zipball_url": q.api + "/zipball/refs/tags/" + t.name,
			"tarball_url": q.api + "/tarball/refs/tags/" + t.name,
		})
	}
	WriteJSON(q.w, http.StatusOK, out)
}

func (g *githubFront) commits(q *githubReq) {
	sha := q.r.URL.Query().Get("sha")
	if sha == "" {
		sha = "refs/heads/" + q.repo.DefaultBranch()
	}
	tip, ok := q.resolve(sha)
	if ok {
		_, err := q.repo.PeelCommit(tip)
		ok = err == nil
	}
	if !ok {
		githubError(q.w, http.StatusNotFound, "Not Found")
		return
	}
	ids, err := q.repo.Log(tip, LogOptions{Path: q.r.URL.Query().Get("path")})
	if err != nil {
		githubError(q.w, http.StatusInternalServerError, err.Error())
		return
	}
	start, end := Paginate(q.w, q.r, len(ids), Paging{SizeParam: "per_page", DefaultSize: 30, MaxSize: 100})
	out := []any{}
	for _, id := range ids[start:end] {
		c, _ := q.repo.ReadCommit(id)
		out = append(out, q.listCommitJSON(id, c))
	}
	WriteJSON(q.w, http.StatusOK, out)
}

func (g *githubFront) tree(q *githubReq, sha string) {
	id, ok := q.resolve(sha)
	if !ok {
		githubError(q.w, http.StatusNotFound, "Not Found")
		return
	}
	recursive := q.r.URL.Query().Has("recursive")
	list, err := q.repo.ListTree(id, recursive)
	if err != nil {
		githubError(q.w, http.StatusNotFound, "Not Found")
		return
	}
	truncated := false
	if recursive && len(list) > g.opts.TreeLimit {
		list, truncated = list[:g.opts.TreeLimit], true
	}
	echo := id
	if p, typ, err := q.repo.Peel(id); err == nil && typ == gitobj.TypeTree {
		echo = p
	}
	entries := []any{}
	for _, e := range list {
		typ := string(e.Mode.Type())
		v := map[string]any{
			"path": e.Path,
			"mode": fmt.Sprintf("%06o", uint32(e.Mode)),
			"type": typ,
			"sha":  e.ID.String(),
		}
		if typ == string(gitobj.TypeBlob) {
			v["size"] = e.Size
			v["url"] = q.api + "/git/blobs/" + e.ID.String()
		} else if typ == string(gitobj.TypeTree) {
			v["url"] = q.api + "/git/trees/" + e.ID.String()
		}
		entries = append(entries, v)
	}
	WriteJSON(q.w, http.StatusOK, map[string]any{
		"sha":       echo.String(),
		"url":       q.api + "/git/trees/" + echo.String(),
		"tree":      entries,
		"truncated": truncated,
	})
}

func (g *githubFront) blob(q *githubReq, sha string) {
	id, err := gitobj.ParseID(sha)
	if err != nil {
		githubError(q.w, http.StatusNotFound, "Not Found")
		return
	}
	o, ok := q.repo.Object(id)
	if !ok {
		githubError(q.w, http.StatusNotFound, "Not Found")
		return
	}
	if o.Type != gitobj.TypeBlob {
		githubError(q.w, http.StatusUnprocessableEntity, "Object is not a blob")
		return
	}
	if int64(len(o.Data)) > g.opts.MaxBlobBytes {
		WriteJSON(q.w, http.StatusForbidden, map[string]any{
			"message":           "This API returns blobs up to 100 MB in size. The requested blob is too large to fetch via the API.",
			"errors":            []any{map[string]any{"resource": "Blob", "field": "data", "code": "too_large"}},
			"documentation_url": "https://docs.github.com/rest/git/blobs#get-a-blob",
			"status":            "403",
		})
		return
	}
	if strings.Contains(q.r.Header.Get("Accept"), "application/vnd.github.raw") {
		q.w.Header().Set("Content-Type", "application/vnd.github.raw")
		q.w.Header().Set("Content-Length", fmt.Sprint(len(o.Data)))
		q.w.WriteHeader(http.StatusOK)
		if q.r.Method != http.MethodHead {
			_, _ = q.w.Write(o.Data)
		}
		return
	}
	WriteJSON(q.w, http.StatusOK, map[string]any{
		"sha":      id.String(),
		"url":      q.api + "/git/blobs/" + id.String(),
		"size":     len(o.Data),
		"content":  githubWrap(base64.StdEncoding.EncodeToString(o.Data)),
		"encoding": "base64",
	})
}

// githubWrap breaks base64 into 60-character lines, as GitHub does.
func githubWrap(s string) string {
	var b strings.Builder
	for len(s) > 60 {
		b.WriteString(s[:60])
		b.WriteByte('\n')
		s = s[60:]
	}
	b.WriteString(s)
	b.WriteByte('\n')
	return b.String()
}

func (g *githubFront) compare(q *githubReq, basehead string) {
	baseName, headName, ok := strings.Cut(basehead, "...")
	if !ok || baseName == "" || headName == "" {
		githubError(q.w, http.StatusNotFound, "Not Found")
		return
	}
	var ids [2]gitobj.ID
	for i, name := range []string{baseName, headName} {
		id, ok := q.resolve(name)
		if ok {
			var err error
			id, err = q.repo.PeelCommit(id)
			ok = err == nil
		}
		if !ok {
			githubError(q.w, http.StatusNotFound, "Not Found")
			return
		}
		ids[i] = id
	}
	base, head := ids[0], ids[1]
	mb, ok, err := q.repo.MergeBase(base, head)
	if err != nil || !ok {
		githubError(q.w, http.StatusNotFound, fmt.Sprintf("No common ancestor between %s and %s.", baseName, headName))
		return
	}
	ahead, _ := q.repo.Log(head, LogOptions{Not: []gitobj.ID{base}})
	behind, _ := q.repo.Log(base, LogOptions{Not: []gitobj.ID{head}})
	status := "diverged"
	switch {
	case len(ahead) == 0 && len(behind) == 0:
		status = "identical"
	case len(behind) == 0:
		status = "ahead"
	case len(ahead) == 0:
		status = "behind"
	}
	slices.Reverse(ahead) // oldest first
	start, end := Paginate(q.w, q.r, len(ahead), Paging{SizeParam: "per_page", DefaultSize: 250, MaxSize: 250})
	commits := []any{}
	for _, id := range ahead[start:end] {
		c, _ := q.repo.ReadCommit(id)
		commits = append(commits, q.listCommitJSON(id, c))
	}
	bc, _ := q.repo.ReadCommit(base)
	mc, _ := q.repo.ReadCommit(mb)
	WriteJSON(q.w, http.StatusOK, map[string]any{
		"url":               q.api + "/compare/" + basehead,
		"status":            status,
		"ahead_by":          len(ahead),
		"behind_by":         len(behind),
		"total_commits":     len(ahead),
		"base_commit":       q.listCommitJSON(base, bc),
		"merge_base_commit": q.listCommitJSON(mb, mc),
		"commits":           commits,
		"files":             []any{},
	})
}
