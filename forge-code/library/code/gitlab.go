package code

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

// gitlabBackend maps the GitLab REST API (v4) to the backend seam. The
// project ID is the %2F-encoded repository path, and file, branch and tag
// names are encoded whole. GitLab reports no tree IDs and no blob sizes in
// listings, and peels tags itself.
type gitlabBackend struct{}

// gitlabMaxPage is GitLab's largest page size.
const gitlabMaxPage = 100

// gitlabCommit is GitLab's commit entity.
type gitlabCommit struct {
	ID             string    `json:"id"`
	ParentIDs      []string  `json:"parent_ids"`
	AuthorName     string    `json:"author_name"`
	AuthorEmail    string    `json:"author_email"`
	AuthoredDate   time.Time `json:"authored_date"`
	CommitterName  string    `json:"committer_name"`
	CommitterEmail string    `json:"committer_email"`
	CommittedDate  time.Time `json:"committed_date"`
	Message        string    `json:"message"`
}

// commit converts c. GitLab never reports the tree.
func (c gitlabCommit) commit() (Commit, error) {
	id, err := parseOID(c.ID)
	if err != nil {
		return Commit{}, err
	}
	out := Commit{
		ID:        id,
		Parents:   make([]OID, 0, len(c.ParentIDs)),
		Author:    Signature{Name: c.AuthorName, Email: c.AuthorEmail, When: c.AuthoredDate},
		Committer: Signature{Name: c.CommitterName, Email: c.CommitterEmail, When: c.CommittedDate},
		Message:   commitMessage(c.Message),
	}
	for _, p := range c.ParentIDs {
		pid, err := parseOID(p)
		if err != nil {
			return Commit{}, err
		}
		out.Parents = append(out.Parents, pid)
	}
	return out, nil
}

// gitlabRef is GitLab's branch or tag entity. Target is set for tags
// only: the tag object of an annotated tag, else the commit. Commit is
// already peeled, and null if a tag points to something else.
type gitlabRef struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Commit *struct {
		ID string `json:"id"`
	} `json:"commit"`
}

// ref converts r, a ref under prefix.
func (r gitlabRef) ref(prefix string) (Ref, error) {
	out := Ref{Name: prefix + r.Name}
	if r.Commit != nil {
		id, err := parseOID(r.Commit.ID)
		if err != nil {
			return Ref{}, err
		}
		out.Commit = id
	}
	out.Target = out.Commit
	if r.Target != "" {
		id, err := parseOID(r.Target)
		if err != nil {
			return Ref{}, err
		}
		out.Target = id
	}
	if r.Name == "" || out.Target == "" {
		return Ref{}, fmt.Errorf("code: forge sent incomplete ref %q", out.Name)
	}
	return out, nil
}

// gitlabEntry is GitLab's tree entry. Path is from the repository root.
type gitlabEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// entry converts e. GitLab sends no sizes.
func (e gitlabEntry) entry() (Entry, error) {
	mode, err := ParseFileMode(e.Mode)
	if err != nil {
		return Entry{}, fmt.Errorf("code: forge sent mode %q for %q", e.Mode, e.Path)
	}
	id, err := parseOID(e.ID)
	if err != nil {
		return Entry{}, err
	}
	if _, err := cleanFilePath(e.Path); err != nil || ObjectType(e.Type) != typeOf(mode) ||
		e.Path[strings.LastIndexByte(e.Path, '/')+1:] != e.Name {
		return Entry{}, fmt.Errorf("code: forge sent bad tree entry %q", e.Path)
	}
	return Entry{Name: e.Name, Path: e.Path, Mode: mode, Type: typeOf(mode), ID: id, Size: -1}, nil
}

// gitlabHidden reports whether err is GitLab's "404 Project Not Found":
// the project is missing or not visible. It stays the forge's error.
func gitlabHidden(err error) bool {
	var fe *forgehttp.Error
	return errors.As(err, &fe) && fe.StatusCode == http.StatusNotFound &&
		strings.Contains(fe.Message, "Project Not Found")
}

// gitlabMissing is missing, but keeps a hidden project's error.
func gitlabMissing(err, notFound error) error {
	if gitlabHidden(err) {
		return err
	}
	return missing(err, notFound)
}

// project returns the API path of the op's project.
func (gitlabBackend) project(o *op) string { return "/projects/" + url.PathEscape(o.repo) }

func (gitlabBackend) capabilities() Capabilities {
	return Capabilities{TreeIDs: false, MergeBase: true}
}

func (g gitlabBackend) defaultBranch(o *op) (string, error) {
	var p struct {
		DefaultBranch *string `json:"default_branch"`
		EmptyRepo     bool    `json:"empty_repo"`
	}
	if err := forgerest.Send(o.ctx, o.s, http.MethodGet, g.project(o), nil, &p); err != nil {
		return "", err
	}
	// GitLab names a default branch even before the first push.
	if p.DefaultBranch == nil || p.EmptyRepo {
		return "", nil
	}
	return *p.DefaultBranch, nil
}

func (g gitlabBackend) commit(o *op, id OID) (Commit, error) {
	var c gitlabCommit
	err := forgerest.Send(o.ctx, o.s, http.MethodGet, g.project(o)+"/repository/commits/"+string(id)+"?stats=false", nil, &c)
	if err != nil {
		return Commit{}, gitlabMissing(err, objectMissing(id))
	}
	got, err := c.commit()
	if err != nil {
		return Commit{}, err
	}
	if got.ID != id {
		// GitLab peels a tag ID to its commit; id is not a commit.
		return Commit{}, objectMissing(id)
	}
	return got, nil
}

func (g gitlabBackend) ref(o *op, name string) (Ref, bool, error) {
	route, prefix := "/repository/branches/", headsPrefix
	if strings.HasPrefix(name, tagsPrefix) {
		route, prefix = "/repository/tags/", tagsPrefix
	}
	short := strings.TrimPrefix(name, prefix)
	var r gitlabRef
	err := forgerest.Send(o.ctx, o.s, http.MethodGet, g.project(o)+route+url.PathEscape(short), nil, &r)
	if err != nil {
		if !gitlabHidden(err) && isMissing(err) {
			return Ref{}, false, nil
		}
		return Ref{}, false, err
	}
	ref, err := r.ref(prefix)
	if err != nil {
		return Ref{}, false, err
	}
	if ref.Name != name {
		return Ref{}, false, fmt.Errorf("code: forge sent ref %q for %q", ref.Name, name)
	}
	return ref, true, nil
}

func (g gitlabBackend) refs(o *op, prefix string, limit int) ([]Ref, bool, error) {
	q := url.Values{"per_page": {strconv.Itoa(min(limit+1, gitlabMaxPage))}}
	route := "/repository/branches?"
	if prefix == tagsPrefix {
		route = "/repository/tags?"
		q.Set("order_by", "name")
		q.Set("sort", "asc")
	}
	var out []Ref
	truncated := false
	err := forgerest.Each(o.ctx, o.s, g.project(o)+route+q.Encode(), func(raw json.RawMessage) (bool, error) {
		if len(out) == limit {
			truncated = true
			return false, nil
		}
		var r gitlabRef
		if err := json.Unmarshal(raw, &r); err != nil {
			return false, err
		}
		ref, err := r.ref(prefix)
		if err != nil {
			return false, err
		}
		out = append(out, ref)
		return true, nil
	})
	if exhausted(err) {
		return out, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return out, truncated, nil
}

// history pages with GitLab's page numbers: pos.Page is the next page to
// fetch (0 means 1). GitLab links a next page whenever a page is full, so
// the last page may be empty.
func (g gitlabBackend) history(o *op, tip OID, path string, n int, pos historyPos) ([]Commit, historyPos, bool, error) {
	page := max(pos.Page, 1)
	q := url.Values{
		"ref_name": {string(tip)},
		"per_page": {strconv.Itoa(n)},
		"page":     {strconv.Itoa(page)},
	}
	if path != "" {
		q.Set("path", path)
		q.Set("follow", "false")
	}
	var raw []gitlabCommit
	if err := forgerest.Send(o.ctx, o.s, http.MethodGet, g.project(o)+"/repository/commits?"+q.Encode(), nil, &raw); err != nil {
		return nil, historyPos{}, false, err
	}
	if len(raw) > n {
		return nil, historyPos{}, false, fmt.Errorf("code: forge sent %d commits for a page of %d", len(raw), n)
	}
	commits := make([]Commit, 0, len(raw))
	for _, c := range raw {
		cm, err := c.commit()
		if err != nil {
			return nil, historyPos{}, false, err
		}
		commits = append(commits, cm)
	}
	return commits, historyPos{Page: page + 1}, len(raw) == n, nil
}

// list calls fn for the entries of the directory at dir ("" is the root)
// in commit, page by page, until fn returns false. It returns notFound if
// GitLab reports the directory missing; older GitLab versions list a
// missing directory as empty instead.
func (g gitlabBackend) list(o *op, commit OID, dir string, recursive bool, perPage int, notFound error, fn func(Entry) bool) error {
	q := url.Values{
		"ref":        {string(commit)},
		"pagination": {"keyset"},
		"per_page":   {strconv.Itoa(min(max(perPage, 1), gitlabMaxPage))},
	}
	if dir != "" {
		q.Set("path", dir)
	}
	if recursive {
		q.Set("recursive", "true")
	}
	err := forgerest.Each(o.ctx, o.s, g.project(o)+"/repository/tree?"+q.Encode(), func(raw json.RawMessage) (bool, error) {
		var ge gitlabEntry
		if err := json.Unmarshal(raw, &ge); err != nil {
			return false, err
		}
		e, err := ge.entry()
		if err != nil {
			return false, err
		}
		if dir != "" && !strings.HasPrefix(e.Path, dir+"/") {
			return false, fmt.Errorf("code: forge sent entry %q outside %q", e.Path, dir)
		}
		return fn(e), nil
	})
	if err != nil {
		return gitlabMissing(err, notFound)
	}
	return nil
}

func (g gitlabBackend) tree(o *op, commit OID, path string, recursive bool, max int) (Tree, error) {
	key := fmt.Sprintf("gitlab:tree:%s:%t:%d:%s", commit, recursive, max, path)
	return cached(o, key, func() (Tree, error) {
		var t Tree
		err := g.list(o, commit, path, recursive, max+1, pathMissing(path), func(e Entry) bool {
			if len(t.Entries) == max {
				t.Truncated = true
				return false
			}
			t.Entries = append(t.Entries, e)
			return true
		})
		if exhausted(err) {
			return Tree{Entries: t.Entries, Truncated: true}, nil
		}
		if err != nil {
			return Tree{}, err
		}
		// Git has no empty directories, so an empty listing below the
		// root means the path is missing or not a directory.
		if path != "" && len(t.Entries) == 0 {
			return Tree{}, pathMissing(path)
		}
		return t, nil
	})
}

// stat finds path in its parent directory's listing.
func (g gitlabBackend) stat(o *op, commit OID, path string) (Entry, error) {
	dir := ""
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		dir = path[:i]
	}
	var got *Entry
	err := g.list(o, commit, dir, false, gitlabMaxPage, pathMissing(path), func(e Entry) bool {
		if e.Path == path {
			got = &e
			return false
		}
		return true
	})
	if err != nil {
		return Entry{}, err
	}
	if got == nil {
		return Entry{}, pathMissing(path)
	}
	return *got, nil
}

// readFile finds the blob ID in the parent listing, then streams
// repository/blobs/:id/raw, which never resolves LFS and sends the size.
// repository/files is avoided: even its HEAD makes GitLab load and hash
// the whole blob, and it reports a submodule as an empty file.
func (g gitlabBackend) readFile(o *op, commit OID, path string) (*Reader, error) {
	e, err := g.stat(o, commit, path)
	if err != nil {
		return nil, err
	}
	if e.Type != ObjectBlob {
		return nil, fmt.Errorf("%w: %q is a %s", ErrNotFile, path, e.Type)
	}
	return g.readBlob(o, e.ID)
}

func (g gitlabBackend) readBlob(o *op, id OID) (*Reader, error) {
	resp, err := forgerest.Open(o.ctx, o.s, http.MethodGet, g.project(o)+"/repository/blobs/"+string(id)+"/raw", nil)
	if err != nil {
		return nil, gitlabMissing(err, objectMissing(id))
	}
	return &Reader{Blob: Blob{ID: id, Size: resp.ContentLength}, ReadCloser: resp.Body}, nil
}

func (g gitlabBackend) mergeBase(o *op, a, b OID) (OID, error) {
	q := url.Values{"refs[]": {string(a), string(b)}}
	var c struct {
		ID string `json:"id"`
	}
	err := forgerest.Send(o.ctx, o.s, http.MethodGet, g.project(o)+"/repository/merge_base?"+q.Encode(), nil, &c)
	if err != nil {
		var fe *forgehttp.Error
		if errors.As(err, &fe) && fe.StatusCode == http.StatusNotFound && strings.Contains(fe.Message, "Merge Base") {
			return "", errNoMergeBase
		}
		return "", err
	}
	return parseOID(c.ID)
}
