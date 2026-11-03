package fakeforge

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

func (h *forgejoFront) trees(w http.ResponseWriter, r *http.Request, repo *Repo, base, sha string) {
	q := r.URL.Query()
	root, ok := forgejoResolve(repo, sha)
	var entries []PathEntry
	var rootEntry gitobj.TreeEntry
	var err error
	if ok {
		if rootEntry, err = repo.Lookup(root, ""); err == nil {
			recursive, _ := strconv.ParseBool(q.Get("recursive"))
			entries, err = repo.ListTree(root, recursive)
		}
	}
	if !ok || err != nil {
		WriteMessage(w, http.StatusBadRequest, "sha not found: "+sha)
		return
	}

	perPage, _ := strconv.Atoi(q.Get(paramPerPage))
	if perPage <= 0 || perPage > h.opts.TreesPerPage {
		perPage = h.opts.TreesPerPage
	}

	page, _ := strconv.Atoi(q.Get("page"))
	page = max(page, 1)
	resp := map[string]any{
		"sha":         rootEntry.ID.String(),
		"url":         base + "/git/trees/" + rootEntry.ID.String(),
		"tree":        nil,
		"truncated":   false,
		"page":        page,
		"total_count": len(entries),
	}
	start := perPage * (page - 1)
	if start < len(entries) {
		// Forgejo's bug: truncated is set on every page, the last too.
		resp["truncated"] = len(entries) > perPage
		end := min(start+perPage, len(entries))
		var list []map[string]any
		for _, e := range entries[start:end] {
			typ := e.Mode.Type()
			u := base + "/git/blobs/" + e.ID.String()
			switch typ {
			case gitobj.TypeTree:
				u = base + "/git/trees/" + e.ID.String()
			case gitobj.TypeCommit:
				u = ""
			}

			list = append(list, map[string]any{
				"path": e.Path,
				"mode": forgejoMode(e.Mode),
				"type": string(typ),
				"size": e.Size,
				"sha":  e.ID.String(),
				"url":  u,
			})
		}

		resp["tree"] = list
	}

	WriteJSON(w, http.StatusOK, resp)
}

// forgejoMode formats a mode as %06o.
func forgejoMode(m gitobj.Mode) string {
	s := m.String()
	return strings.Repeat("0", max(0, 6-len(s))) + s
}

func (h *forgejoFront) blob(w http.ResponseWriter, repo *Repo, base, sha string) {
	id, err := gitobj.ParseID(strings.ToLower(sha))
	o, ok := repo.Object(id)
	if err != nil || !ok {
		WriteMessage(w, http.StatusBadRequest, "object does not exist [id: "+sha+"]")
		return
	}

	content := ""
	if int64(len(o.Data)) <= h.opts.MaxBlobSize {
		content = base64.StdEncoding.EncodeToString(o.Data)
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"content":   content,
		keyEncoding: encBase64,
		"url":       base + "/git/blobs/" + id.String(),
		"sha":       id.String(),
		"size":      len(o.Data),
	})
}

func (h *forgejoFront) raw(w http.ResponseWriter, r *http.Request, repo *Repo, p string) {
	rev := r.URL.Query().Get("ref")
	if rev == "" {
		rev = "refs/heads/" + repo.DefaultBranch()
	}

	id, ok := forgejoResolve(repo, rev)
	if ok {
		id, ok = forgejoPeelCommit(repo, id)
	}
	if !ok {
		WriteMessage(w, http.StatusNotFound, "Not Found")
		return
	}

	e, err := repo.Lookup(id, p)
	if err != nil || p == "" || e.Mode == gitobj.ModeTree || e.Mode == gitobj.ModeSubmodule {
		WriteMessage(w, http.StatusNotFound, "Not Found")
		return
	}

	data, err := repo.ReadBlob(e.ID)
	if err != nil {
		WriteMessage(w, http.StatusInternalServerError, err.Error())
		return
	}

	etag := `"` + e.ID.String() + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	kind := "file"
	if e.Mode == gitobj.ModeSymlink {
		kind = "symlink"
	}

	w.Header().Set("X-Forgejo-Object-Type", kind)
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
