package fakeforge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgerest"
)

const gitlabProj = "/projects/group%2Fsub%2Fproj"

func newGitLab(t *testing.T) (*Server, *Repo, Fixture) {
	t.Helper()
	s := NewServer(t, forgeauth.GitLab, GitLab)
	r := s.NewRepo("group/sub/proj")
	return s, r, Seed(r)
}

// gitlabGet sends a request and returns the response with its body read.
func gitlabGet(t *testing.T, s *Server, method, path string) (*http.Response, string) {
	t.Helper()
	req, err := s.Session.NewRequest(context.Background(), method, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.Session.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func TestGitLabProject(t *testing.T) {
	s, r, _ := newGitLab(t)
	var p struct {
		ID            int    `json:"id"`
		DefaultBranch string `json:"default_branch"`
		EmptyRepo     bool   `json:"empty_repo"`
		Path          string `json:"path_with_namespace"`
	}
	for _, id := range []string{gitlabProj, "/projects/" + strconv.Itoa(r.ID())} {
		if err := s.Session.GetJSON(context.Background(), id, &p); err != nil || p.Path != "group/sub/proj" ||
			p.DefaultBranch != "main" || p.EmptyRepo {
			t.Fatalf("%s: %+v, %v", id, p, err)
		}
	}
	s.NewRepo("g/empty")
	if err := s.Session.GetJSON(context.Background(), "/projects/g%2Fempty", &p); err != nil || !p.EmptyRepo {
		t.Fatalf("empty: %+v, %v", p, err)
	}
	// An unencoded path does not match the route.
	resp, _ := gitlabGet(t, s, "GET", "/projects/group/sub/proj")
	if resp.StatusCode != 404 {
		t.Errorf("unencoded: %d", resp.StatusCode)
	}
	r.SetPrivate(true)
	s.SetAccess("group/sub/proj", AccessNone)
	err := s.Session.GetJSON(context.Background(), gitlabProj+"/repository/branches/main", &p)
	var fe *forgehttp.Error
	if !errors.As(err, &fe) || fe.StatusCode != 404 || fe.Message != "404 Project Not Found" {
		t.Errorf("private: %v", err)
	}
	if resp, _ := gitlabGet(t, s, "POST", gitlabProj); resp.StatusCode != 405 {
		t.Errorf("POST: %d", resp.StatusCode)
	}
}

func TestGitLabCommits(t *testing.T) {
	s, r, fx := newGitLab(t)
	var c struct {
		ID        string   `json:"id"`
		ParentIDs []string `json:"parent_ids"`
		Message   string   `json:"message"`
		Date      string   `json:"committed_date"`
	}
	// A tag ID, a tag name and a short SHA all give the peeled commit.
	for _, rev := range []string{fx.Tag.String(), "v1.0", fx.Main[3].String()[:10], "refs%2Ftags%2Fv1.0"} {
		if err := s.Session.GetJSON(context.Background(), gitlabProj+"/repository/commits/"+rev, &c); err != nil || c.ID != fx.Main[3].String() {
			t.Errorf("%s: %+v, %v", rev, c, err)
		}
	}
	if c.Date != "2024-01-01T04:00:00.000+00:00" || len(c.ParentIDs) != 1 || c.Message != "Rename and add unicode name\n" {
		t.Errorf("commit = %+v", c)
	}
	// Tags win over branches, as in git.
	if err := s.Session.GetJSON(context.Background(), gitlabProj+"/repository/commits/clash", &c); err != nil || c.ID != fx.ClashTag.String() {
		t.Errorf("clash: %+v, %v", c, err)
	}
	for _, rev := range []string{"nope", fx.Blobs[PathReadme].String(), "abcd"} {
		err := s.Session.GetJSON(context.Background(), gitlabProj+"/repository/commits/"+rev, &c)
		var fe *forgehttp.Error
		if !errors.As(err, &fe) || fe.Message != "404 Commit Not Found" {
			t.Errorf("%s: %v", rev, err)
		}
	}

	// The list links a next page whenever a page is full.
	log, _ := r.Log(fx.Head, LogOptions{})
	total := 0
	err := forgerest.Each(context.Background(), s.Session, gitlabProj+"/repository/commits?ref_name=main&per_page="+strconv.Itoa(len(log)), func(json.RawMessage) (bool, error) {
		total++
		return true, nil
	})
	if err != nil || total != len(log) {
		t.Fatalf("total %d of %d, %v", total, len(log), err)
	}
	if n := s.Calls("GET", "*/repository/commits"); n != 2 {
		t.Errorf("%d pages, want 2 (the last one empty)", n)
	}
	resp, body := gitlabGet(t, s, "GET", gitlabProj+"/repository/commits?ref_name=nope")
	if resp.StatusCode != 200 || strings.TrimSpace(body) != "[]" || resp.Header.Get("X-Total") != "" {
		t.Errorf("unknown ref: %d %s", resp.StatusCode, body)
	}
}

func TestGitLabRefs(t *testing.T) {
	s, _, fx := newGitLab(t)
	var tag struct {
		Name   string `json:"name"`
		Target string `json:"target"`
		Commit *struct {
			ID string `json:"id"`
		} `json:"commit"`
		Message string `json:"message"`
	}
	if err := s.Session.GetJSON(context.Background(), gitlabProj+"/repository/tags/chain", &tag); err != nil ||
		tag.Target != fx.TagOuter.String() || tag.Commit.ID != fx.Main[1].String() || tag.Message != "Outer\n" {
		t.Fatalf("chain = %+v, %v", tag, err)
	}
	var fe *forgehttp.Error
	err := s.Session.GetJSON(context.Background(), gitlabProj+"/repository/branches/nope", &tag)
	if !errors.As(err, &fe) || fe.Message != "404 Branch Not Found" {
		t.Errorf("branch: %v", err)
	}
	err = s.Session.GetJSON(context.Background(), gitlabProj+"/repository/tags/nope", &tag)
	if !errors.As(err, &fe) || fe.Message != "404 Tag Not Found" {
		t.Errorf("tag: %v", err)
	}

	names := func(path string) []string {
		var refs []struct {
			Name string `json:"name"`
		}
		if err := s.Session.GetJSON(context.Background(), path, &refs); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range refs {
			out = append(out, r.Name)
		}
		return out
	}
	for path, want := range map[string]string{
		"/repository/branches":                               "clash feature main",
		"/repository/tags?order_by=name&sort=asc":            "chain clash light v1.0",
		"/repository/tags?order_by=name":                     "v1.0 light clash chain",
		"/repository/tags":                                   "v1.0 clash chain light", // newest commit first
		"/repository/branches?per_page=2&page=2":             "main",
		"/repository/tags?per_page=1&order_by=name&sort=asc": "chain",
	} {
		if got := strings.Join(names(gitlabProj+path), " "); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}

func TestGitLabTree(t *testing.T) {
	s, r, fx := newGitLab(t)
	type entry struct {
		ID, Name, Type, Path, Mode string
	}
	all, _ := r.ListTree(fx.Head, true)

	// Keyset pages follow opaque tokens and end without an empty page.
	var got []entry
	err := forgerest.Each(context.Background(), s.Session, gitlabProj+"/repository/tree?ref=main&recursive=true&pagination=keyset&per_page=5",
		func(raw json.RawMessage) (bool, error) {
			var e entry
			err := json.Unmarshal(raw, &e)
			got = append(got, e)
			return true, err
		})
	if err != nil || len(got) != len(all) {
		t.Fatalf("got %d of %d entries, %v", len(got), len(all), err)
	}
	if n, want := s.Calls("GET", "*/repository/tree"), (len(all)+4)/5; n != want {
		t.Errorf("%d pages, want %d", n, want)
	}
	modes := map[string]string{}
	for _, e := range got {
		modes[e.Path] = e.Mode + " " + e.Type
	}
	for path, want := range map[string]string{
		"docs": "040000 tree", PathSymlink: "120000 blob", PathExec: "100755 blob",
		PathSubmodule: "160000 commit", PathNested: "100644 blob",
	} {
		if modes[path] != want {
			t.Errorf("%s: %q, want %q", path, modes[path], want)
		}
	}

	var page []entry
	if err := s.Session.GetJSON(context.Background(), gitlabProj+"/repository/tree?ref="+fx.Head.String()+"&path=docs", &page); err != nil ||
		len(page) != 1 || page[0].Path != "docs/guide" {
		t.Errorf("docs = %+v, %v", page, err)
	}
	if err := s.Session.GetJSON(context.Background(), gitlabProj+"/repository/tree?recursive=true&pagination=none", &page); err != nil || len(page) != len(all) {
		t.Errorf("unpaged: %d entries, %v", len(page), err)
	}
	if err := s.Session.GetJSON(context.Background(), gitlabProj+"/repository/tree?per_page=2&page=2", &page); err != nil || len(page) != 2 {
		t.Errorf("offset: %d entries, %v", len(page), err)
	}
	for _, q := range []string{"ref=nope", "path=nope", "path=" + PathReadme, "path=" + PathSubmodule} {
		err := s.Session.GetJSON(context.Background(), gitlabProj+"/repository/tree?"+q, &page)
		if !errors.Is(err, forgehttp.ErrNotFound) {
			t.Errorf("%s: %v", q, err)
		}
	}
	bad := base64.RawURLEncoding.EncodeToString([]byte("nope"))
	resp, _ := gitlabGet(t, s, "GET", gitlabProj+"/repository/tree?pagination=keyset&page_token="+bad)
	if resp.StatusCode != 400 {
		t.Errorf("bad token: %d", resp.StatusCode)
	}
}

func TestGitLabFiles(t *testing.T) {
	s, _, fx := newGitLab(t)
	file := gitlabProj + "/repository/files/" + strings.ReplaceAll(PathExec, "/", "%2F")
	resp, body := gitlabGet(t, s, "HEAD", file+"?ref=main")
	h := resp.Header
	if resp.StatusCode != 200 || body != "" || h.Get("X-Gitlab-Blob-Id") != fx.Blobs[PathExec].String() ||
		h.Get("X-Gitlab-Size") != "19" || h.Get("X-Gitlab-Execute-Filemode") != "true" || h.Get("X-Gitlab-Commit-Id") != fx.Head.String() {
		t.Fatalf("HEAD: %d %v", resp.StatusCode, h)
	}
	var meta struct {
		Content string `json:"content"`
		BlobID  string `json:"blob_id"`
	}
	if err := s.Session.GetJSON(context.Background(), file+"?ref=main", &meta); err != nil || meta.Content != base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\necho run\n")) {
		t.Errorf("JSON: %+v, %v", meta, err)
	}
	resp, body = gitlabGet(t, s, "GET", file+"/raw")
	if resp.StatusCode != 200 || body != "#!/bin/sh\necho run\n" || resp.Header.Get("X-Gitlab-Blob-Id") == "" {
		t.Errorf("raw: %d %q", resp.StatusCode, body)
	}
	for path, status := range map[string]int{
		file:               400, // no ref
		file + "?ref=nope": 404,
		gitlabProj + "/repository/files/docs?ref=main":         404,
		gitlabProj + "/repository/files/vendor%2Flib?ref=main": 200, // submodule: an empty file
		gitlabProj + "/repository/files/vendor/lib?ref=main":   404, // unencoded slash
		file + "/raw?lfs=true":                                 404,
		file + "/blame?ref=main":                               404,
	} {
		if resp, _ := gitlabGet(t, s, "GET", path); resp.StatusCode != status {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, status)
		}
	}
}

func TestGitLabBlobs(t *testing.T) {
	s, _, fx := newGitLab(t)
	id := fx.Blobs[PathBinary].String()
	resp, body := gitlabGet(t, s, "GET", gitlabProj+"/repository/blobs/"+id+"/raw")
	if resp.StatusCode != 200 || body != string(Binary) || resp.ContentLength != int64(len(Binary)) {
		t.Fatalf("raw: %d %q", resp.StatusCode, body)
	}
	var b struct {
		Size    int    `json:"size"`
		Content string `json:"content"`
	}
	if err := s.Session.GetJSON(context.Background(), gitlabProj+"/repository/blobs/"+id, &b); err != nil ||
		b.Size != len(Binary) || b.Content != base64.StdEncoding.EncodeToString(Binary) {
		t.Errorf("JSON: %+v, %v", b, err)
	}
	for _, sha := range []string{fx.Head.String(), "nope"} {
		resp, body := gitlabGet(t, s, "GET", gitlabProj+"/repository/blobs/"+sha+"/raw")
		if resp.StatusCode != 404 || !strings.Contains(body, "404 Blob Not Found") {
			t.Errorf("%s: %d %s", sha, resp.StatusCode, body)
		}
	}
}

func TestGitLabMergeBase(t *testing.T) {
	s, r, fx := newGitLab(t)
	orphan := r.CommitTree(r.BuildTree(map[string]File{"x": {Data: []byte("x")}}), Epoch, "orphan\n")
	for q, want := range map[string]string{
		"refs[]=main&refs[]=feature":                               "200 " + fx.MergeBase.String(),
		"refs%5B%5D=feature&refs%5B%5D=" + fx.Head.String():        "200 " + fx.MergeBase.String(),
		"refs[]=main&refs[]=feature&refs[]=" + fx.Main[1].String(): "200 " + fx.Main[1].String(),
		"refs[]=main":                           "400 Provide at least 2 refs",
		"refs[]=main&refs[]=nope":               "400 Could not find ref: nope",
		"refs[]=x&refs[]=nope":                  "400 Could not find refs: x, nope",
		"refs[]=main&refs[]=" + orphan.String(): "404 404 Merge Base Not Found",
	} {
		resp, body := gitlabGet(t, s, "GET", gitlabProj+"/repository/merge_base?"+q)
		var v struct{ ID, Message string }
		_ = json.Unmarshal([]byte(body), &v)
		got := strconv.Itoa(resp.StatusCode) + " " + v.Message
		if resp.StatusCode == 200 {
			got = "200 " + v.ID
		}
		if got != want {
			t.Errorf("%s: %q, want %q", q, got, want)
		}
	}
}

func TestGitLabRoutes(t *testing.T) {
	s, _, _ := newGitLab(t)
	for _, path := range []string{
		"/users", gitlabProj + "/issues", gitlabProj + "/repository", gitlabProj + "/repository/nope",
		gitlabProj + "/repository/commits/a/b", gitlabProj + "/repository/branches/feat/x",
		"/projects/999",
	} {
		if resp, _ := gitlabGet(t, s, "GET", path); resp.StatusCode != 404 {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
}
