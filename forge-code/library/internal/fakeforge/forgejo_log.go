package fakeforge

import (
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

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

func (h *forgejoFront) commits(w http.ResponseWriter, r *http.Request, repo *Repo, base string) {
	q := r.URL.Query()
	tip, ok := h.logTip(w, repo, q.Get("sha"))
	if !ok {
		return
	}

	not, ok := forgejoNot(w, repo, q["not"])
	if !ok {
		return
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

	pg := h.commitPage(q, path != "", len(ids))
	Paginate(w, r, len(ids), Paging{SizeParam: "limit", DefaultSize: pg.limit, MaxSize: pg.limit})
	pg.setHeaders(w.Header(), len(ids))
	out := []map[string]any{}
	for _, id := range ids[pg.start:pg.end] {
		c, _ := repo.ReadCommit(id)
		out = append(out, forgejoCommitJSON(base, id, c))
	}

	WriteJSON(w, http.StatusOK, out)
}

// logTip resolves the sha of /commits, the default branch if empty, to a
// commit. It writes the error response if there is none.
func (h *forgejoFront) logTip(w http.ResponseWriter, repo *Repo, sha string) (gitobj.ID, bool) {
	if sha == "" {
		if _, ok := repo.Head(); !ok {
			WriteMessage(w, http.StatusConflict, "repository is empty")
			return gitobj.ID{}, false
		}

		sha = "refs/heads/" + repo.DefaultBranch()
	}

	tip, ok := forgejoResolve(repo, sha)
	if ok {
		tip, ok = forgejoPeelCommit(repo, tip)
	}
	if !ok {
		WriteMessage(w, http.StatusNotFound, "sha not found")
	}

	return tip, ok
}

// forgejoNot resolves the not parameters of /commits. It writes the
// error response if one is unknown.
func forgejoNot(w http.ResponseWriter, repo *Repo, names []string) ([]gitobj.ID, bool) {
	var not []gitobj.ID
	for _, n := range names {
		id, ok := forgejoResolve(repo, n)
		if !ok {
			WriteMessage(w, http.StatusNotFound, "not found: "+n)
			return nil, false
		}

		not = append(not, id)
	}

	return not, true
}

// forgejoPage is one page of a /commits listing.
type forgejoPage struct {
	page, limit, pages int
	start, end         int
}

// commitPage picks the page of total commits that q asks for. With a
// path it skips as Forgejo does.
func (h *forgejoFront) commitPage(q url.Values, hasPath bool, total int) forgejoPage {
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = forgejoPageSize
	}

	limit = min(limit, h.opts.MaxResponseItems, h.opts.CommitsRangeSize)
	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	skip := (page - 1) * limit
	if hasPath {
		skip = (page - 1) * h.opts.CommitsRangeSize // Forgejo's path-skip bug
	}

	start := min(skip, total)

	return forgejoPage{
		page:  page,
		limit: limit,
		pages: (total + limit - 1) / limit,
		start: start,
		end:   min(start+limit, total),
	}
}

// setHeaders sets Forgejo's paging headers.
func (p forgejoPage) setHeaders(hd http.Header, total int) {
	hd.Set("X-Total", strconv.Itoa(total))
	hd.Set("X-Total-Count", strconv.Itoa(total))
	hd.Set("X-PageCount", strconv.Itoa(p.pages))
	hd.Set("X-Page", strconv.Itoa(p.page))
	hd.Set("X-PerPage", strconv.Itoa(p.limit))
	hd.Set("X-HasMore", strconv.FormatBool(p.page < p.pages))
}
