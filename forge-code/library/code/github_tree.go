package code

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// githubTreeItem is a git/trees entry.
type githubTreeItem struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
	Size *int64 `json:"size"`
}

// githubListing is one git/trees response, with Paths relative to the
// listed tree.
type githubListing struct {
	Entries   []Entry
	Truncated bool
}

// githubTree reads git/trees/{id}, flat or recursive.
func githubTree(o *op, repo string, id OID, recursive bool) (githubListing, error) {
	path := repo + "/git/trees/" + string(id)
	if recursive {
		path += "?recursive=1"
	}

	var v struct {
		Tree      []githubTreeItem `json:"tree"`
		Truncated bool             `json:"truncated"`
	}
	if err := forgerest.Send(o.ctx, o.s, http.MethodGet, path, nil, &v); err != nil {
		return githubListing{}, err
	}

	l := githubListing{Entries: make([]Entry, 0, len(v.Tree)), Truncated: v.Truncated}
	for _, it := range v.Tree {
		mode, err := ParseFileMode(it.Mode)
		if err != nil {
			return githubListing{}, fmt.Errorf("code: GitHub sent mode %q for %q", it.Mode, it.Path)
		}

		eid, err := parseOID(it.SHA)
		if err != nil {
			return githubListing{}, err
		}

		e := Entry{
			Name: it.Path,
			Path: it.Path,
			Mode: mode,
			Type: typeOf(mode),
			ID:   eid,
			Size: -1,
		}
		if i := strings.LastIndexByte(it.Path, '/'); i >= 0 {
			e.Name = it.Path[i+1:]
		}
		if e.Type == ObjectBlob && it.Size != nil {
			e.Size = *it.Size
		}

		l.Entries = append(l.Entries, e)
	}

	return l, nil
}

// githubFlat returns the flat listing of tree id, memoised per call.
func githubFlat(o *op, repo string, id OID) (githubListing, error) {
	return cached(o, "github:tree:"+string(id), func() (githubListing, error) {
		return githubTree(o, repo, id, false)
	})
}

// githubDir returns the tree ID of directory dir ("" is the root) in
// commit, walking one listing per path segment.
func (b githubBackend) githubDir(o *op, repo string, commit OID, dir string) (OID, error) {
	c, err := b.commit(o, commit)
	if err != nil {
		return "", err
	}

	id := c.Tree
	if dir == "" {
		return id, nil
	}

	for _, name := range strings.Split(dir, "/") {
		e, err := githubFind(o, repo, id, name)
		if err != nil {
			if errors.Is(err, ErrPathNotFound) {
				return "", pathMissing(dir)
			}

			return "", err
		}

		if e.Mode != ModeTree {
			return "", pathMissing(dir)
		}

		id = e.ID
	}

	return id, nil
}

// githubFind returns the entry name in tree id.
func githubFind(o *op, repo string, id OID, name string) (Entry, error) {
	l, err := githubFlat(o, repo, id)
	if err != nil {
		return Entry{}, err
	}

	for _, e := range l.Entries {
		if e.Name == name {
			return e, nil
		}
	}

	if l.Truncated {
		return Entry{}, fmt.Errorf("%w: GitHub truncated the listing of tree %s", ErrTooLarge, id)
	}

	return Entry{}, pathMissing(name)
}

// githubUnder returns entries with dir prefixed to their Paths.
func githubUnder(dir string, entries []Entry) []Entry {
	out := make([]Entry, len(entries))
	for i, e := range entries {
		if dir != "" {
			e.Path = dir + "/" + e.Path
		}

		out[i] = e
	}

	return out
}

func (b githubBackend) tree(o *op, commit OID, path string, recursive bool, max int) (Tree, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return Tree{}, err
	}

	id, err := b.githubDir(o, repo, commit, path)
	if err != nil {
		return Tree{}, err
	}

	t := Tree{ID: id}
	if !recursive {
		l, err := githubFlat(o, repo, id)
		if err != nil {
			return Tree{}, err
		}

		t.Entries, t.Truncated = githubUnder(path, l.Entries), l.Truncated

		return t, nil
	}

	l, err := githubTree(o, repo, id, true)
	switch {
	case exhausted(err):
		t.Truncated = true
		return t, nil
	case err != nil:
		return Tree{}, err
	case !l.Truncated:
		t.Entries = githubUnder(path, l.Entries)
		return t, nil
	}

	// GitHub cut the recursive listing: walk the subtrees one at a time.
	type dir struct {
		id   OID
		path string
	}
	queue := []dir{{id, path}}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		l, err := githubFlat(o, repo, d.id)
		if exhausted(err) {
			t.Truncated = true
			break
		}

		if err != nil {
			return Tree{}, err
		}

		t.Truncated = t.Truncated || l.Truncated
		for _, e := range githubUnder(d.path, l.Entries) {
			if len(t.Entries) == max {
				t.Truncated = true
				return t, nil
			}

			t.Entries = append(t.Entries, e)
			if e.Mode == ModeTree {
				queue = append(queue, dir{e.ID, e.Path})
			}
		}
	}

	return t, nil
}

func (b githubBackend) stat(o *op, commit OID, path string) (Entry, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return Entry{}, err
	}

	dir, name := "", path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		dir, name = path[:i], path[i+1:]
	}

	id, err := b.githubDir(o, repo, commit, dir)
	if err == nil {
		var e Entry
		if e, err = githubFind(o, repo, id, name); err == nil {
			e.Path = path
			return e, nil
		}
	}

	if errors.Is(err, ErrPathNotFound) {
		return Entry{}, pathMissing(path)
	}

	return Entry{}, err
}

func (b githubBackend) readFile(o *op, commit OID, path string) (*Reader, error) {
	e, err := b.stat(o, commit, path)
	if err != nil {
		return nil, err
	}
	if e.Mode == ModeTree || e.Mode == ModeSubmodule {
		return nil, fmt.Errorf("%w: %q is a %s", ErrNotFile, path, e.Type)
	}
	if e.Size > o.limits.MaxBlobBytes || e.Size > githubMaxBlob {
		return nil, fmt.Errorf("%w: %q has %d bytes", ErrTooLarge, path, e.Size)
	}

	r, err := githubOpenBlob(o, e.ID)
	if err != nil {
		return nil, missing(err, pathMissing(path))
	}
	if e.Size >= 0 {
		r.Size = e.Size
	}

	return r, nil
}

func (githubBackend) readBlob(o *op, id OID) (*Reader, error) {
	r, err := githubOpenBlob(o, id)
	if err != nil {
		return nil, missing(err, objectMissing(id))
	}

	return r, nil
}

// githubOpenBlob streams blob id with the raw media type.
func githubOpenBlob(o *op, id OID) (*Reader, error) {
	repo, err := githubRepo(o)
	if err != nil {
		return nil, err
	}

	resp, err := forgerest.Open(o.ctx, o.s, http.MethodGet, repo+"/git/blobs/"+string(id),
		http.Header{"Accept": {githubRawBlob}})
	if err != nil {
		if githubTooLarge(err) {
			// Not wrapped: the forge's 422 would then read as a missing blob.
			return nil, fmt.Errorf("%w: GitHub serves blobs up to 100 MB: %s", ErrTooLarge, err.Error())
		}

		return nil, err
	}

	// A JSON body would be base64 content, not the blob.
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "application/json" {
		_ = resp.Body.Close() // nothing useful is left to read
		return nil, errors.New("code: GitHub ignored the raw media type for a blob")
	}

	return bodyReader(id, resp), nil
}
