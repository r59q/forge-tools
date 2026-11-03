package code

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// forgejoTreeEntry is one entry of git/trees.
type forgejoTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

// decode returns the entry with dir prepended to its path.
func (te forgejoTreeEntry) decode(dir string) (Entry, error) {
	mode, err := ParseFileMode(te.Mode)
	if err != nil {
		return Entry{}, fmt.Errorf("code: forge sent invalid mode %q", te.Mode)
	}

	id, err := parseOID(te.SHA)
	if err != nil {
		return Entry{}, err
	}
	if _, err := cleanFilePath(te.Path); err != nil {
		return Entry{}, fmt.Errorf("code: forge sent invalid tree path %q", te.Path)
	}

	p := te.Path
	if dir != "" {
		p = dir + "/" + p
	}

	e := Entry{
		Name: path.Base(te.Path),
		Path: p,
		Mode: mode,
		Type: typeOf(mode),
		ID:   id,
		Size: -1,
	}
	if e.Type == ObjectBlob {
		e.Size = te.Size
	}

	return e, nil
}

// forgejoList lists tree id, recursively if asked, with dir prepended to
// every path. It stops after max entries, or when the call budget runs
// out, and then reports truncated. The forge's truncated flag is set on
// every page of a long listing, so paging stops at total_count instead.
func forgejoList(o *op, id OID, dir string, recursive bool, max int) ([]Entry, bool, error) {
	var out []Entry
	for page := 1; ; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, paramPerPage: {strconv.Itoa(forgejoTreePage)}}
		if recursive {
			q.Set("recursive", "true")
		}

		var v struct {
			Tree       []forgejoTreeEntry `json:"tree"`
			TotalCount int                `json:"total_count"`
		}
		if _, err := forgejoGet(o, "/git/trees/"+string(id)+"?"+q.Encode(), &v); err != nil {
			if exhausted(err) {
				return out, true, nil
			}

			return nil, false, err
		}

		for _, te := range v.Tree {
			if len(out) == max {
				return out, true, nil
			}

			e, err := te.decode(dir)
			if err != nil {
				return nil, false, err
			}

			out = append(out, e)
		}

		if len(v.Tree) == 0 || len(out) >= v.TotalCount {
			return out, false, nil
		}
		if len(out) == max {
			return out, true, nil
		}
	}
}

// forgejoDir returns the complete flat listing of tree id, memoised.
// Entry paths are bare names.
func forgejoDir(o *op, id OID) ([]Entry, error) {
	return cached(o, "tree:"+string(id), func() ([]Entry, error) {
		entries, truncated, err := forgejoList(o, id, "", false, math.MaxInt)
		if err != nil {
			return nil, err
		}
		if truncated {
			return nil, errBudget
		}

		return entries, nil
	})
}

// forgejoWalk returns the tree ID of directory dir ("" is the root) in
// commit, reading one listing per path component.
func forgejoWalk(o *op, commit OID, dir string) (OID, error) {
	c, err := forgejoBackend{}.commit(o, commit)
	if err != nil {
		return "", err
	}

	id := c.Tree
	if dir == "" {
		return id, nil
	}

	for _, name := range strings.Split(dir, "/") {
		entries, err := forgejoDir(o, id)
		if err != nil {
			return "", missing(err, pathMissing(dir))
		}

		i := slices.IndexFunc(entries, func(e Entry) bool { return e.Name == name })
		if i < 0 || entries[i].Mode != ModeTree {
			return "", pathMissing(dir)
		}

		id = entries[i].ID
	}

	return id, nil
}

func (forgejoBackend) tree(o *op, commit OID, path string, recursive bool, max int) (Tree, error) {
	id, err := forgejoWalk(o, commit, path)
	if err != nil {
		return Tree{}, err
	}

	key := "tree:" + string(id)
	if !recursive {
		// Reuse a listing forgejoWalk or forgejoDir already read.
		o.mu.Lock()
		v, ok := o.memo[key]
		o.mu.Unlock()
		if ok {
			names := v.([]Entry)
			entries := make([]Entry, min(len(names), max))
			for i := range entries {
				entries[i] = names[i]
				if path != "" {
					entries[i].Path = path + "/" + names[i].Name
				}
			}

			return Tree{ID: id, Entries: entries, Truncated: len(names) > max}, nil
		}
	}

	entries, truncated, err := forgejoList(o, id, path, recursive, max)
	if err != nil {
		return Tree{}, missing(err, pathMissing(path))
	}
	if !recursive && !truncated {
		// Keep the complete listing, with bare names, for later walks.
		names := slices.Clone(entries)
		for i := range names {
			names[i].Path = names[i].Name
		}

		o.mu.Lock()
		o.memo[key] = names
		o.mu.Unlock()
	}

	return Tree{ID: id, Entries: entries, Truncated: truncated}, nil
}

func (forgejoBackend) stat(o *op, commit OID, p string) (Entry, error) {
	dir, name := "", p
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		dir, name = p[:i], p[i+1:]
	}

	id, err := forgejoWalk(o, commit, dir)
	if errors.Is(err, ErrPathNotFound) {
		return Entry{}, pathMissing(p)
	}
	if err != nil {
		return Entry{}, err
	}

	entries, err := forgejoDir(o, id)
	if err != nil {
		return Entry{}, missing(err, pathMissing(p))
	}

	i := slices.IndexFunc(entries, func(e Entry) bool { return e.Name == name })
	if i < 0 {
		return Entry{}, pathMissing(p)
	}

	e := entries[i]
	e.Path = p

	return e, nil
}

// readFile streams raw/{path}?ref={commit}. The ETag header is the blob
// ID. The forge answers 404 for a directory or submodule too, so a 404 is
// told apart with stat.
func (b forgejoBackend) readFile(o *op, commit OID, p string) (*Reader, error) {
	repo, err := forgejoRepo(o)
	if err != nil {
		return nil, err
	}

	resp, err := forgerest.Open(o.ctx, o.s, http.MethodGet, repo+"/raw/"+escapePath(p)+"?ref="+string(commit), nil)
	if errors.Is(err, forgehttp.ErrNotFound) {
		e, serr := b.stat(o, commit, p)
		if serr != nil {
			return nil, serr
		}
		if e.Type != ObjectBlob {
			return nil, fmt.Errorf("%w: %q is a %s", ErrNotFile, p, e.Type)
		}

		return nil, pathMissing(p)
	}

	if err != nil {
		return nil, err
	}

	id, ok := forgejoETag(resp.Header.Get("ETag"))
	if !ok {
		e, err := b.stat(o, commit, p)
		if err != nil {
			_ = resp.Body.Close() // the stat error is the one to report
			return nil, err
		}

		id = e.ID
	}

	if resp.ContentLength > o.limits.MaxBlobBytes {
		_ = resp.Body.Close() // the size error is the one to report
		return nil, fmt.Errorf("%w: blob of %d bytes, limit %d", ErrTooLarge, resp.ContentLength, o.limits.MaxBlobBytes)
	}

	return bodyReader(id, resp), nil
}

// forgejoETag parses an ETag holding an object ID, such as "\"abc...\"".
func forgejoETag(v string) (OID, bool) {
	v = strings.Trim(strings.TrimPrefix(strings.TrimSpace(v), "W/"), `"`)
	id, err := parseOID(v)

	return id, err == nil
}

// forgejoBlobSlack is room in a git/blobs response for the fields around
// the base64 content.
const forgejoBlobSlack = 64 << 10

// readBlob reads git/blobs/{id}, base64 in JSON. The forge sends empty
// content for blobs over its [api] DEFAULT_MAX_BLOB_SIZE (10 MiB), which
// admins may raise, so the body is decoded here with a cap that follows
// MaxBlobBytes instead of forgerest's 32 MiB.
func (forgejoBackend) readBlob(o *op, id OID) (*Reader, error) {
	repo, err := forgejoRepo(o)
	if err != nil {
		return nil, err
	}

	resp, err := forgerest.Open(o.ctx, o.s, http.MethodGet, repo+"/git/blobs/"+string(id),
		acceptJSON())
	if err != nil {
		return nil, missing(err, objectMissing(id))
	}

	defer resp.Body.Close()
	var v struct {
		SHA      string `json:"sha"`
		Size     int64  `json:"size"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	limit := int64(base64.StdEncoding.EncodedLen(int(min(o.limits.MaxBlobBytes, math.MaxInt32)))) + forgejoBlobSlack
	body := &io.LimitedReader{R: resp.Body, N: limit + 1}
	if err := json.NewDecoder(body).Decode(&v); err != nil {
		if body.N <= 0 {
			return nil, fmt.Errorf("%w: blob response over %d bytes, limit %d", ErrTooLarge, limit, o.limits.MaxBlobBytes)
		}

		return nil, fmt.Errorf("code: decode blob %s: %w", id, err)
	}

	if got, err := parseOID(v.SHA); err != nil || got != id {
		return nil, fmt.Errorf("code: forge sent blob %q for %s", v.SHA, id)
	}

	switch {
	case v.Size > o.limits.MaxBlobBytes:
		return nil, fmt.Errorf("%w: blob of %d bytes, limit %d", ErrTooLarge, v.Size, o.limits.MaxBlobBytes)
	case v.Size > 0 && v.Content == "":
		return nil, fmt.Errorf("%w: blob of %d bytes is over the forge's API limit; use ReadFile", ErrTooLarge, v.Size)
	case v.Encoding != "base64":
		return nil, fmt.Errorf("code: forge sent blob encoding %q", v.Encoding)
	}

	dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(v.Content))

	return &Reader{Blob: Blob{ID: id, Size: v.Size}, ReadCloser: io.NopCloser(dec)}, nil
}
