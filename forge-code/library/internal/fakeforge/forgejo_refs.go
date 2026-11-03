package fakeforge

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

func (h *forgejoFront) refs(w http.ResponseWriter, repo *Repo, base, filter string) {
	prefix := "refs/"
	if filter != "" {
		prefix += filter
	}

	var out []map[string]any
	var names []string
	for _, ref := range repo.Refs(prefix) {
		if strings.HasPrefix(ref.Name, "refs/remotes/") {
			continue
		}

		typ, kind := "", ""
		if o, ok := repo.Object(ref.ID); ok {
			typ = string(o.Type)
			kind = typ + "s"
		}

		out = append(out, map[string]any{
			"ref": ref.Name,
			"url": base + "/git/" + ref.Name,
			keyObject: map[string]any{
				"type": typ,
				"sha":  ref.ID.String(),
				"url":  base + "/git/" + kind + "/" + ref.ID.String(),
			},
		})
		names = append(names, ref.Name)
	}

	switch {
	case len(out) == 0:
		WriteMessage(w, http.StatusNotFound, "Not Found")
	case h.opts.SingleRefObject && len(out) == 1 && names[0] == "refs/"+filter:
		WriteJSON(w, http.StatusOK, out[0])
	default:
		WriteJSON(w, http.StatusOK, out)
	}
}

func (h *forgejoFront) tag(w http.ResponseWriter, repo *Repo, base, sha string) {
	id, err := gitobj.ParseID(strings.ToLower(sha))
	var t gitobj.Tag
	if err == nil {
		t, err = repo.ReadTag(id)
	}
	if err == nil && !slices.ContainsFunc(repo.Refs("refs/tags/"), func(r NamedRef) bool { return r.ID == id }) {
		err = errNoTagRef
	}
	if err != nil {
		WriteMessage(w, http.StatusBadRequest, "GetAnnotatedTag: object does not exist")
		return
	}

	v := map[string]any{
		"tag":      t.Name,
		"sha":      id.String(),
		"url":      base + "/git/tags/" + id.String(),
		keyMessage: t.Message,
		keyObject: map[string]any{
			"type": string(t.Type),
			"sha":  t.Object.String(),
			"url":  base + "/git/" + string(t.Type) + "s/" + t.Object.String(),
		},
	}
	if t.Tagger != nil {
		v["tagger"] = forgejoSig(*t.Tagger)
	}

	WriteJSON(w, http.StatusOK, v)
}

// errNoTagRef is Forgejo's GetTagNameBySHA failing: no tag ref points to
// the tag object.
var errNoTagRef = errors.New("no tag ref points to the object")

func (h *forgejoFront) tags(w http.ResponseWriter, r *http.Request, repo *Repo, base string) {
	type tagInfo struct {
		v    map[string]any
		when time.Time
	}
	var all []tagInfo
	for _, ref := range repo.Refs("refs/tags/") {
		name := strings.TrimPrefix(ref.Name, "refs/tags/")
		o, ok := repo.Object(ref.ID)
		if !ok {
			continue
		}

		target, msg := ref.ID, ""
		var when time.Time
		switch o.Type {
		case gitobj.TypeTag:
			t, err := repo.ReadTag(ref.ID)
			if err != nil {
				continue
			}
			target, msg = t.Object, strings.TrimSpace(t.Message)
			if t.Tagger != nil {
				when = t.Tagger.When
			}
		case gitobj.TypeCommit:
			c, _ := repo.ReadCommit(ref.ID)
			when = c.Committer.When
		}

		all = append(all, tagInfo{when: when, v: map[string]any{
			"name":        name,
			keyMessage:    msg,
			"id":          ref.ID.String(),
			keyCommit:     forgejoMeta(base+"/git/commits/"+ref.ID.String(), target, when),
			"zipball_url": base + "/archive/" + name + ".zip",
			"tarball_url": base + "/archive/" + name + ".tar.gz",
		}})
	}

	slices.SortStableFunc(all, func(a, b tagInfo) int { return b.when.Compare(a.when) })
	w.Header().Set("X-Total-Count", strconv.Itoa(len(all)))
	start, end := Paginate(w, r, len(all), Paging{SizeParam: "limit", DefaultSize: forgejoPageSize, MaxSize: forgejoMaxItems})
	out := []map[string]any{}
	for _, t := range all[start:end] {
		out = append(out, t.v)
	}

	WriteJSON(w, http.StatusOK, out)
}
