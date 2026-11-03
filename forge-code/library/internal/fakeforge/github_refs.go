package fakeforge

import (
	"net/http"
	"slices"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

func (q *githubReq) refJSON(name string, id gitobj.ID) map[string]any {
	typ := string(gitobj.TypeCommit)
	if o, ok := q.repo.Object(id); ok {
		typ = string(o.Type)
	}

	return map[string]any{
		"ref":     name,
		"url":     q.api + "/git/" + name,
		keyObject: map[string]any{"type": typ, "sha": id.String(), "url": q.api + "/git/" + typ + "s/" + id.String()},
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
		"sha":      id.String(),
		"url":      q.api + "/git/tags/" + id.String(),
		"tag":      t.Name,
		keyMessage: t.Message,
		keyObject: map[string]any{
			"type": string(t.Type),
			"sha":  t.Object.String(),
			"url":  q.api + "/git/" + string(t.Type) + "s/" + t.Object.String(),
		},
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
	start, end := Paginate(q.w, q.r, len(list), Paging{SizeParam: paramPerPage, DefaultSize: 30, MaxSize: 100})
	out := []any{}
	for _, t := range list[start:end] {
		out = append(out, map[string]any{
			"name":        t.name,
			keyCommit:     map[string]any{"sha": t.commit.String(), "url": q.api + "/commits/" + t.commit.String()},
			"zipball_url": q.api + "/zipball/refs/tags/" + t.name,
			"tarball_url": q.api + "/tarball/refs/tags/" + t.name,
		})
	}

	WriteJSON(q.w, http.StatusOK, out)
}
