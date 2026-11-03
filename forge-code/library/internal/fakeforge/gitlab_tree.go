package fakeforge

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

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

	page, ok := gitlabTreePage(w, r, list, prefix)
	if !ok {
		return
	}

	page = slices.Clone(page)
	slices.SortStableFunc(page, func(a, b PathEntry) int { return gitlabRank(a.Mode) - gitlabRank(b.Mode) })
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

// gitlabTreePage picks the page of a tree listing that r asks for and
// sets its paging headers. It writes the error response for a bad
// page_token.
func gitlabTreePage(w http.ResponseWriter, r *http.Request, list []PathEntry, prefix string) ([]PathEntry, bool) {
	q := r.URL.Query()
	switch {
	case q.Get("pagination") == "keyset":
		return gitlabKeysetPage(w, r, list, prefix)
	case q.Get("pagination") == "none" && q.Get("recursive") == "true":
		return list, true
	}

	start, end := Paginate(w, r, len(list), Paging{SizeParam: paramPerPage, DefaultSize: 20, MaxSize: 100})
	w.Header().Set("X-Total", strconv.Itoa(len(list)))

	return list[start:end], true
}

// gitlabKeysetPage picks a keyset page: the entries after page_token,
// with a next link only if more follow.
func gitlabKeysetPage(w http.ResponseWriter, r *http.Request, list []PathEntry, prefix string) ([]PathEntry, bool) {
	q := r.URL.Query()
	limit := gitlabInt(q.Get(paramPerPage), 20, 100)
	start := 0
	if tok := q.Get("page_token"); tok != "" {
		raw, err := base64.RawURLEncoding.DecodeString(tok)
		i := slices.IndexFunc(list, func(p PathEntry) bool { return prefix+p.Path == string(raw) })
		if err != nil || i < 0 {
			WriteMessage(w, http.StatusBadRequest, "could not find starting OID: "+tok)
			return nil, false
		}

		start = i + 1
	}

	page := list[start:]
	if limit < len(page) {
		page = page[:limit]
		last := prefix + page[len(page)-1].Path
		tok := base64.RawURLEncoding.EncodeToString([]byte(last))
		w.Header().Set("Link", gitlabLink(r, "next", "page_token", tok))
	}

	return page, true
}

// gitlabRank orders tree entries as GitLab does: trees, blobs, then
// submodules.
func gitlabRank(m gitobj.Mode) int {
	switch m {
	case gitobj.ModeTree:
		return 0
	case gitobj.ModeSubmodule:
		return 2
	}

	return 1
}
