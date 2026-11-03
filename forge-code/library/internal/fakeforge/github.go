package fakeforge

import (
	"fmt"
	"net/http"
	"net/url"
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

	q := &githubReq{
		w:    w,
		r:    r,
		repo: repo,
		path: path,
		api:  "http://" + r.Host + APIPrefix(forgeauth.GitHub) + "/repos/" + path,
	}
	rest := segs[3:]
	if len(rest) == 0 {
		g.repository(q)
		return
	}

	empty := len(repo.Refs("refs/")) == 0
	gitEndpoint := rest[0] == "git" || rest[0] == segCommits || rest[0] == "compare"
	if empty && gitEndpoint {
		githubError(w, http.StatusConflict, "Git Repository is empty.")
		return
	}

	tail := func(from int) string { return strings.Join(rest[from:], "/") }
	switch {
	case rest[0] == "git" && len(rest) == 3 && rest[1] == segCommits:
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
	case rest[0] == segCommits && len(rest) == 1:
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
		keyMessage:          msg,
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

	return gitobj.ID{}, false
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
		"sha":       id.String(),
		"url":       q.api + "/git/commits/" + id.String(),
		"author":    githubPerson(c.Author),
		"committer": githubPerson(c.Committer),
		keyMessage:  strings.TrimSuffix(c.Message, "\n"),
		"tree":      map[string]any{"sha": c.Tree.String(), "url": q.api + "/git/trees/" + c.Tree.String()},
		"parents":   q.parents(c),
		"verification": map[string]any{
			"verified":  false,
			"reason":    "unsigned",
			"signature": nil,
			"payload":   nil,
		},
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
		keyCommit: inner,
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
