package fakeforge

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
)

// serveFile serves a file's metadata as X-Gitlab-* headers (HEAD), as
// JSON with base64 content (GET), or raw. ref is required. Directories
// are "404 File Not Found". A submodule is an empty file whose blob_id is
// the submodule's commit, as GitLab sends it.
func (g *gitlabFront) serveFile(w http.ResponseWriter, r *http.Request, repo *Repo, path string, raw bool) {
	q := r.URL.Query()
	ref := q.Get("ref")
	if ref == "" && !raw {
		gitlabBadRequest(w, "ref is missing")
		return
	}

	commit, ok := gitlabResolve(repo, ref)
	if !ok {
		gitlabNotFound(w, "Commit")
		return
	}

	e, err := repo.Lookup(commit, path)
	if err != nil || e.Mode.Type() == gitobj.TypeTree {
		gitlabNotFound(w, "File")
		return
	}

	var data []byte
	if e.Mode != gitobj.ModeSubmodule {
		if data, err = repo.ReadBlob(e.ID); err != nil {
			gitlabNotFound(w, "File")
			return
		}
	}

	last := ""
	if ids, err := repo.Log(commit, LogOptions{Path: path}); err == nil && len(ids) > 0 {
		last = ids[0].String()
	}

	sum := sha256.Sum256(data)
	meta := map[string]any{
		"file_name":        path[strings.LastIndexByte(path, '/')+1:],
		"file_path":        path,
		"size":             len(data),
		keyEncoding:        encBase64,
		"content_sha256":   hex.EncodeToString(sum[:]),
		"ref":              ref,
		"blob_id":          e.ID.String(),
		"commit_id":        commit.String(),
		"last_commit_id":   last,
		"execute_filemode": e.Mode == gitobj.ModeExec,
	}
	h := w.Header()
	for k, v := range meta {
		h.Set("X-Gitlab-"+gitlabDasherize(k), fmt.Sprint(v))
	}

	switch {
	case raw && q.Get("lfs") == "true":
		gitlabNotFound(w, "")
	case raw:
		gitlabWriteRaw(w, data)
	case r.Method == http.MethodHead:
		w.WriteHeader(http.StatusOK)
	default:
		meta["content"] = base64.StdEncoding.EncodeToString(data)
		WriteJSON(w, http.StatusOK, meta)
	}
}

// gitlabDasherize turns "blob_id" into "Blob-Id", as Rails names headers.
func gitlabDasherize(k string) string {
	parts := strings.Split(k, "_")
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}

	return strings.Join(parts, "-")
}

// gitlabWriteRaw writes blob content as Workhorse does.
func gitlabWriteRaw(w http.ResponseWriter, data []byte) {
	ct := "text/plain; charset=utf-8"
	if bytes.IndexByte(data, 0) >= 0 {
		ct = "application/octet-stream"
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data) //nolint:gosec // G705: blob bytes sent as text/plain or octet-stream, as GitLab does; never HTML.
}

// serveBlob serves a blob by full ID, as JSON or raw. Other objects are
// "404 Blob Not Found".
func (g *gitlabFront) serveBlob(w http.ResponseWriter, repo *Repo, sha string, raw bool) {
	id, err := gitobj.ParseID(sha)
	var data []byte
	if err == nil {
		data, err = repo.ReadBlob(id)
	}
	if err != nil {
		gitlabNotFound(w, "Blob")
		return
	}

	if raw {
		gitlabWriteRaw(w, data)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"size":      len(data),
		keyEncoding: encBase64,
		"content":   base64.StdEncoding.EncodeToString(data),
		"sha":       id.String(),
	})
}
