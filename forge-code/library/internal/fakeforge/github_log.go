package fakeforge

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

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

	start, end := Paginate(q.w, q.r, len(ids), Paging{SizeParam: paramPerPage, DefaultSize: 30, MaxSize: 100})
	out := []any{}
	for _, id := range ids[start:end] {
		c, _ := q.repo.ReadCommit(id)
		out = append(out, q.listCommitJSON(id, c))
	}

	WriteJSON(q.w, http.StatusOK, out)
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
	start, end := Paginate(q.w, q.r, len(ahead), Paging{SizeParam: paramPerPage, DefaultSize: 250, MaxSize: 250})
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
