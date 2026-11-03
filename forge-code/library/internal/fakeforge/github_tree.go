package fakeforge

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

func (g *githubFront) tree(q *githubReq, sha string) {
	id, ok := q.resolve(sha)
	if !ok {
		githubError(q.w, http.StatusNotFound, "Not Found")
		return
	}

	recursive := q.r.URL.Query().Has("recursive")
	list, err := q.repo.ListTree(id, recursive)
	if err != nil {
		githubError(q.w, http.StatusNotFound, "Not Found")
		return
	}

	truncated := false
	if recursive && len(list) > g.opts.TreeLimit {
		list, truncated = list[:g.opts.TreeLimit], true
	}

	echo := id
	if p, typ, err := q.repo.Peel(id); err == nil && typ == gitobj.TypeTree {
		echo = p
	}

	entries := []any{}
	for _, e := range list {
		typ := string(e.Mode.Type())
		v := map[string]any{
			"path": e.Path,
			"mode": fmt.Sprintf("%06o", uint32(e.Mode)),
			"type": typ,
			"sha":  e.ID.String(),
		}
		if typ == string(gitobj.TypeBlob) {
			v["size"] = e.Size
			v["url"] = q.api + "/git/blobs/" + e.ID.String()
		} else if typ == string(gitobj.TypeTree) {
			v["url"] = q.api + "/git/trees/" + e.ID.String()
		}

		entries = append(entries, v)
	}

	WriteJSON(q.w, http.StatusOK, map[string]any{
		"sha":       echo.String(),
		"url":       q.api + "/git/trees/" + echo.String(),
		"tree":      entries,
		"truncated": truncated,
	})
}

func (g *githubFront) blob(q *githubReq, sha string) {
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

	if o.Type != gitobj.TypeBlob {
		githubError(q.w, http.StatusUnprocessableEntity, "Object is not a blob")
		return
	}

	if int64(len(o.Data)) > g.opts.MaxBlobBytes {
		WriteJSON(q.w, http.StatusForbidden, map[string]any{
			keyMessage:          "This API returns blobs up to 100 MB in size. The requested blob is too large to fetch via the API.",
			"errors":            []any{map[string]any{"resource": "Blob", "field": "data", "code": "too_large"}},
			"documentation_url": "https://docs.github.com/rest/git/blobs#get-a-blob",
			"status":            "403",
		})
		return
	}

	if strings.Contains(q.r.Header.Get("Accept"), "application/vnd.github.raw") {
		q.w.Header().Set("Content-Type", "application/vnd.github.raw")
		q.w.Header().Set("Content-Length", fmt.Sprint(len(o.Data)))
		q.w.WriteHeader(http.StatusOK)
		if q.r.Method != http.MethodHead {
			_, _ = q.w.Write(o.Data)
		}

		return
	}

	WriteJSON(q.w, http.StatusOK, map[string]any{
		"sha":       id.String(),
		"url":       q.api + "/git/blobs/" + id.String(),
		"size":      len(o.Data),
		"content":   githubWrap(base64.StdEncoding.EncodeToString(o.Data)),
		keyEncoding: encBase64,
	})
}

// githubWrap breaks base64 into 60-character lines, as GitHub does.
func githubWrap(s string) string {
	var b strings.Builder
	for len(s) > 60 {
		b.WriteString(s[:60])
		b.WriteByte('\n')
		s = s[60:]
	}

	b.WriteString(s)
	b.WriteByte('\n')

	return b.String()
}
