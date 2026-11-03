package fakeforge

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// gitlabRef is GitLab's branch or tag entity for a ref.
func gitlabRef(repo *Repo, route string, ref NamedRef) map[string]any {
	short := strings.TrimPrefix(strings.TrimPrefix(ref.Name, "refs/heads/"), "refs/tags/")
	var commit any
	if id, err := repo.PeelCommit(ref.ID); err == nil {
		if c, err := repo.ReadCommit(id); err == nil {
			commit = gitlabCommit(id, c)
		}
	}
	if route == gitlabBranches {
		return map[string]any{
			"name":      short,
			keyCommit:   commit,
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
		keyCommit:    commit,
		keyMessage:   "",
		"release":    nil,
		"protected":  false,
		"created_at": nil,
	}
	if t, err := repo.ReadTag(ref.ID); err == nil {
		tag[keyMessage] = t.Message
		if t.Tagger != nil {
			tag["created_at"] = t.Tagger.When.Format(gitlabTime)
		}
	}

	return tag
}

// gitlabRefPrefix maps a route to its ref namespace.
func gitlabRefPrefix(route string) string {
	if route == gitlabBranches {
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

	start, end := Paginate(w, r, len(refs), Paging{SizeParam: paramPerPage, DefaultSize: 20, MaxSize: 100})
	w.Header().Set("X-Total", strconv.Itoa(len(refs)))
	out := []map[string]any{}
	for _, ref := range refs[start:end] {
		out = append(out, gitlabRef(repo, route, ref))
	}

	WriteJSON(w, http.StatusOK, out)
}
