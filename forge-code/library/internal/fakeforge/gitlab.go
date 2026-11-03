package fakeforge

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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
// or the route does not match. Errors are {keyMessage: "404 X Not Found"},
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

// gitlabBranches is the branches route; the other ref route is "tags".
const gitlabBranches = "branches"

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
	case route == segCommits && n == 2:
		g.serveLog(w, r, repo)
	case route == segCommits && n == 3:
		if sha, ok := arg(2); ok {
			g.serveCommit(w, repo, sha)
			return
		}
		gitlabNotFound(w, "")
	case (route == gitlabBranches || route == "tags") && n == 2:
		g.serveRefs(w, r, repo, route)
	case (route == gitlabBranches || route == "tags") && n == 3:
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

		return gitobj.ID{}, false
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

	return gitobj.ID{}, false
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
		keyMessage:          strings.ToValidUTF8(c.Message, "�"),
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
