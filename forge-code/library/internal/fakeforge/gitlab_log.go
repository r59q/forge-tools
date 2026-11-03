package fakeforge

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// serveLog lists commits with offset paging. Like GitLab it sends no
// totals and links a next page whenever the page is full.
func (g *gitlabFront) serveLog(w http.ResponseWriter, r *http.Request, repo *Repo) {
	q := r.URL.Query()
	commits := []map[string]any{}
	limit := gitlabInt(q.Get(paramPerPage), 20, 100)
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
