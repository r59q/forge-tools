package fakeforge

import (
	"net/http"
	"net/url"
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
	case len(rest) == 1 && rest[0] == segCommits:
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
		case segCommits:
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

	return gitobj.ID{}, false
}

// forgejoDate formats a time as Forgejo does.
func forgejoDate(t time.Time) string { return t.Format(time.RFC3339) }

func forgejoSig(s gitobj.Signature) map[string]any {
	return map[string]any{"name": s.Name, "email": s.Email, "date": forgejoDate(s.When)}
}

// forgejoMeta is a Forgejo CommitMeta: an object's URL, SHA and date.
func forgejoMeta(url string, id gitobj.ID, when time.Time) map[string]any {
	return map[string]any{"url": url, "sha": id.String(), "created": forgejoDate(when)}
}

// forgejoCommitJSON is a Forgejo Commit with stat, verification and files
// off.
func forgejoCommitJSON(base string, id gitobj.ID, c gitobj.Commit) map[string]any {
	when := c.Committer.When
	parents := []map[string]any{}
	for _, p := range c.Parents {
		parents = append(parents, forgejoMeta(base+"/git/commits/"+p.String(), p, when))
	}

	v := forgejoMeta(base+"/git/commits/"+id.String(), id, when)
	v["html_url"] = base + "/commit/" + id.String()
	v[keyCommit] = map[string]any{
		"url":       base + "/git/commits/" + id.String(),
		"author":    forgejoSig(c.Author),
		"committer": forgejoSig(c.Committer),
		keyMessage:  c.Message,
		"tree":      forgejoMeta(base+"/git/trees/"+c.Tree.String(), c.Tree, when),
	}
	v["author"] = nil
	v["committer"] = nil
	v["parents"] = parents

	return v
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

func forgejoPeelCommit(repo *Repo, id gitobj.ID) (gitobj.ID, bool) {
	c, err := repo.PeelCommit(id)
	return c, err == nil
}
