package fakeforge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// githubGet sends GET path with an Accept header and returns the status,
// headers and body.
func githubGet(t *testing.T, s *Server, path, accept string) (int, http.Header, []byte) {
	t.Helper()
	req, err := s.Session.NewRequest(context.Background(), http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := s.Session.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, body
}

func newGitHubServer(t *testing.T, opts GitHubOptions) (*Server, *Repo, Fixture) {
	s := NewServer(t, forgeauth.GitHub, GitHubFront(opts))
	r := s.NewRepo("octo/fixture")
	return s, r, Seed(r)
}

func TestGitHubFrontShapes(t *testing.T) {
	s, _, fx := newGitHubServer(t, GitHubOptions{})
	const repo = "/repos/octo/fixture"

	var info struct {
		DefaultBranch string `json:"default_branch"`
		FullName      string `json:"full_name"`
	}
	if err := get(t, s.Session, repo, &info); err != nil || info.DefaultBranch != "main" || info.FullName != "octo/fixture" {
		t.Errorf("repo = %+v, %v", info, err)
	}

	var c struct {
		SHA     string               `json:"sha"`
		Tree    struct{ SHA string } `json:"tree"`
		Parents []struct{ SHA string }
		Author  struct{ Date string }
		Message string
	}
	if err := get(t, s.Session, repo+"/git/commits/"+fx.Merge.String(), &c); err != nil ||
		c.SHA != fx.Merge.String() || len(c.Parents) != 2 || c.Author.Date != "2024-01-01T07:00:00Z" ||
		strings.HasSuffix(c.Message, "\n") {
		t.Errorf("git/commits = %+v, %v", c, err)
	}

	var ref struct {
		Ref    string
		Object struct{ Type, SHA string }
	}
	if err := get(t, s.Session, repo+"/git/ref/tags/chain", &ref); err != nil ||
		ref.Ref != "refs/tags/chain" || ref.Object.Type != "tag" || ref.Object.SHA != fx.TagOuter.String() {
		t.Errorf("git/ref = %+v, %v", ref, err)
	}
	// Only exact names match.
	if err := get(t, s.Session, repo+"/git/ref/heads/feat", &ref); !errors.Is(err, forgehttp.ErrNotFound) {
		t.Errorf("git/ref prefix = %v, want 404", err)
	}

	// matching-refs is not paged: per_page and page are ignored.
	var refs []struct{ Ref string }
	status, h, body := githubGet(t, s, repo+"/git/matching-refs/heads/?per_page=1&page=2", "")
	if err := json.Unmarshal(body, &refs); err != nil || status != http.StatusOK ||
		len(refs) != 3 || refs[0].Ref != "refs/heads/clash" || h.Get("Link") != "" {
		t.Errorf("matching-refs = %d %+v, %v", status, refs, err)
	}

	var tag struct{ Object struct{ Type, SHA string } }
	if err := get(t, s.Session, repo+"/git/tags/"+fx.TagOuter.String(), &tag); err != nil ||
		tag.Object.Type != "tag" || tag.Object.SHA != fx.TagInner.String() {
		t.Errorf("git/tags = %+v, %v", tag, err)
	}

	var tags []struct {
		Name   string
		Commit struct{ SHA string }
	}
	if err := get(t, s.Session, repo+"/tags", &tags); err != nil || len(tags) != 4 ||
		tags[0].Name != "v1.0" || tags[0].Commit.SHA != fx.Main[3].String() {
		t.Errorf("tags = %+v, %v", tags, err)
	}

	var tree struct {
		SHA  string
		Tree []struct {
			Path, Mode, Type, SHA string
			Size                  *int64
		}
		Truncated bool
	}
	if err := get(t, s.Session, repo+"/git/trees/"+fx.Head.String(), &tree); err != nil || tree.SHA != fx.Head.String() {
		t.Fatalf("git/trees on a commit = %+v, %v", tree.SHA, err)
	}
	modes := map[string]string{}
	for _, e := range tree.Tree {
		modes[e.Path] = e.Mode + " " + e.Type
		if (e.Type == "blob") != (e.Size != nil) {
			t.Errorf("entry %s: size %v", e.Path, e.Size)
		}
	}
	if modes["docs"] != "040000 tree" || modes["link"] != "120000 blob" {
		t.Errorf("modes = %v", modes)
	}
}

func TestGitHubFrontQuirks(t *testing.T) {
	s, r, fx := newGitHubServer(t, GitHubOptions{TreeLimit: 3, MaxBlobBytes: 64})
	const repo = "/repos/octo/fixture"
	root, _ := r.Lookup(fx.Head, "")

	var tree struct {
		SHA       string
		Tree      []struct{ Path string }
		Truncated bool
	}
	if err := get(t, s.Session, repo+"/git/trees/"+root.ID.String()+"?recursive=1", &tree); err != nil ||
		!tree.Truncated || len(tree.Tree) != 3 || tree.SHA != root.ID.String() {
		t.Errorf("recursive over limit = %+v, %v", tree, err)
	}

	blob := fx.Blobs[PathBinary]
	status, h, body := githubGet(t, s, repo+"/git/blobs/"+blob.String(), "application/vnd.github.raw+json")
	if status != http.StatusOK || !bytes.Equal(body, Binary) || h.Get("Content-Length") != "16" {
		t.Errorf("raw blob = %d %q", status, body)
	}
	var b64 struct{ Content, Encoding string }
	if err := get(t, s.Session, repo+"/git/blobs/"+blob.String(), &b64); err != nil || b64.Encoding != "base64" {
		t.Fatalf("JSON blob = %+v, %v", b64, err)
	}
	if data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(b64.Content, "\n", "")); err != nil || !bytes.Equal(data, Binary) {
		t.Errorf("JSON blob content = %q, %v", data, err)
	}
	status, _, body = githubGet(t, s, repo+"/git/blobs/"+fx.Blobs[PathLarge].String(), "application/vnd.github.raw+json")
	if status != http.StatusForbidden || !strings.Contains(string(body), "too_large") {
		t.Errorf("large blob = %d %s", status, body)
	}

	for path, want := range map[string]int{
		repo + "/commits?sha=" + strings.Repeat("ab", 20):            http.StatusNotFound,
		repo + "/git/commits/" + fx.Blobs[PathReadme].String():       http.StatusUnprocessableEntity,
		repo + "/git/commits/" + strings.Repeat("ab", 20):            http.StatusNotFound,
		repo + "/git/commits/main":                                   http.StatusNotFound,
		repo + "/compare/main..feature":                              http.StatusNotFound,
		repo + "/compare/main...nope":                                http.StatusNotFound,
		repo + "/nope":                                               http.StatusNotFound,
		"/repos/octo/missing/git/ref/heads/main":                     http.StatusNotFound,
		repo + "/git/blobs/" + fx.Head.String():                      http.StatusUnprocessableEntity,
		repo + "/git/trees/" + fx.Blobs[PathReadme].String():         http.StatusNotFound,
		repo + "/compare/" + fx.Main[1].String() + "..." + "feature": http.StatusOK,
	} {
		if status, _, body := githubGet(t, s, path, ""); status != want {
			t.Errorf("GET %s = %d %s, want %d", path, status, body, want)
		}
	}

	var cmp struct {
		Status    string `json:"status"`
		AheadBy   int    `json:"ahead_by"`
		BehindBy  int    `json:"behind_by"`
		MergeBase struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
		Commits []struct{ SHA string }
	}
	if err := get(t, s.Session, repo+"/compare/main...feature?per_page=1", &cmp); err != nil ||
		cmp.Status != "diverged" || cmp.AheadBy != 1 || cmp.MergeBase.SHA != fx.MergeBase.String() ||
		len(cmp.Commits) != 1 || cmp.Commits[0].SHA != fx.Feature[2].String() {
		t.Errorf("compare = %+v, %v", cmp, err)
	}
	orphan := r.CommitTree(r.BuildTree(map[string]File{"x": {Data: []byte("x")}}), Epoch, "Orphan\n")
	r.SetRef("refs/heads/orphan", orphan)
	status, _, body = githubGet(t, s, repo+"/compare/main...orphan", "")
	if status != http.StatusNotFound || !strings.Contains(string(body), "No common ancestor") {
		t.Errorf("unrelated compare = %d %s", status, body)
	}

	// An empty repository answers 409 for Git data.
	s.NewRepo("octo/empty")
	if status, _, _ := githubGet(t, s, "/repos/octo/empty/git/ref/heads/main", ""); status != http.StatusConflict {
		t.Errorf("empty git/ref = %d, want 409", status)
	}
	if status, _, _ := githubGet(t, s, "/repos/octo/empty", ""); status != http.StatusOK {
		t.Errorf("empty repo = %d, want 200", status)
	}
}

func TestGitHubFrontPrivate(t *testing.T) {
	s, r, _ := newGitHubServer(t, GitHubOptions{})
	r.SetPrivate(true)
	s.SetAccess("octo/fixture", AccessNone)
	if status, _, _ := githubGet(t, s, "/repos/octo/fixture", ""); status != http.StatusNotFound {
		t.Errorf("private repo = %d, want 404", status)
	}
	if status, _, _ := githubGet(t, s, "/repos/octo/fixture/git/ref/heads/main", ""); status != http.StatusNotFound {
		t.Errorf("private ref = %d, want 404", status)
	}
}
