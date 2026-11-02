package fakeforge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/internal/gitobj"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// forgejoStatus returns the status of GET path on s, decoding a 2xx JSON
// body into v.
func forgejoStatus(t *testing.T, s *Server, path string, v any) (int, http.Header) {
	t.Helper()
	req, err := s.Session.NewRequest(context.Background(), http.MethodGet, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.Session.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 && v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, resp.Header
}

func TestForgejoFront(t *testing.T) {
	s := NewServer(t, forgeauth.Forgejo, ForgejoAPIWith(ForgejoOptions{MaxBlobSize: 1024, TreesPerPage: 5}))
	repo := s.NewRepo("o/r")
	fx := Seed(repo)
	base := "/repos/o/r"

	var info struct {
		DefaultBranch string `json:"default_branch"`
		Empty         bool   `json:"empty"`
	}
	if st, _ := forgejoStatus(t, s, base, &info); st != 200 || info.DefaultBranch != "main" || info.Empty {
		t.Fatalf("repo: %d %+v", st, info)
	}

	t.Run("refs", func(t *testing.T) {
		var refs []map[string]any
		// Prefix match: tags/c matches chain and clash; always an array.
		if st, _ := forgejoStatus(t, s, base+"/git/refs/tags/c", &refs); st != 200 || len(refs) != 2 {
			t.Fatalf("%d %v", st, refs)
		}
		if st, _ := forgejoStatus(t, s, base+"/git/refs/heads/main", &refs); st != 200 || len(refs) != 1 {
			t.Fatalf("%d %v", st, refs)
		}
		if st, _ := forgejoStatus(t, s, base+"/git/refs/heads/nope", nil); st != 404 {
			t.Fatalf("none: %d", st)
		}
		if st, _ := forgejoStatus(t, s, base+"/git/refs", &refs); st != 200 || len(refs) != 7 {
			t.Fatalf("all: %d %d", st, len(refs))
		}
	})

	t.Run("tags", func(t *testing.T) {
		var tags []struct {
			Name   string
			ID     string
			Commit struct{ SHA string }
		}
		if st, _ := forgejoStatus(t, s, base+"/tags?limit=50", &tags); st != 200 || len(tags) != 4 {
			t.Fatalf("%d %v", st, tags)
		}
		for _, tg := range tags {
			if tg.Name == "chain" && (tg.ID != fx.TagOuter.String() || tg.Commit.SHA != fx.TagInner.String()) {
				t.Fatalf("chain peeled more than one level: %+v", tg)
			}
		}
		var tag struct{ Object struct{ Type, SHA string } }
		if st, _ := forgejoStatus(t, s, base+"/git/tags/"+fx.TagOuter.String(), &tag); st != 200 || tag.Object.Type != "tag" {
			t.Fatalf("%d %+v", st, tag)
		}
		if st, _ := forgejoStatus(t, s, base+"/git/tags/"+fx.Head.String(), nil); st != 400 {
			t.Fatalf("not a tag: %d", st)
		}
		// The inner tag of a chain has no ref of its own: 400, as
		// GetTagNameBySHA fails. git/commits still peels the chain.
		if st, _ := forgejoStatus(t, s, base+"/git/tags/"+fx.TagInner.String(), nil); st != 400 {
			t.Fatalf("inner tag: %d", st)
		}
		var c struct{ SHA string }
		if st, _ := forgejoStatus(t, s, base+"/git/commits/"+fx.TagOuter.String(), &c); st != 200 || c.SHA != fx.Main[1].String() {
			t.Fatalf("chain peel: %d %s", st, c.SHA)
		}
	})

	t.Run("commits", func(t *testing.T) {
		if st, _ := forgejoStatus(t, s, base+"/git/commits/main~1", nil); st != 422 {
			t.Fatalf("rev expression: %d", st)
		}
		var c struct{ SHA string }
		if st, _ := forgejoStatus(t, s, base+"/git/commits/"+fx.Tag.String(), &c); st != 200 || c.SHA != fx.Main[3].String() {
			t.Fatalf("tag peel: %d %s", st, c.SHA)
		}
		if st, _ := forgejoStatus(t, s, base+"/git/commits/"+gitobj.ZeroID.String(), nil); st != 404 {
			t.Fatalf("missing: %d", st)
		}
		if st, _ := forgejoStatus(t, s, base+"/commits?sha="+fx.Head.String()+"&path=nope", nil); st != 404 {
			t.Fatalf("no history: %d", st)
		}
	})

	t.Run("path skip bug", func(t *testing.T) {
		r := s.NewRepo("o/long")
		var tip gitobj.ID
		for i := range 120 {
			tree := r.BuildTree(map[string]File{"x": {Data: fmt.Appendf(nil, "%d", i)}})
			var parents []gitobj.ID
			if i > 0 {
				parents = []gitobj.ID{tip}
			}
			tip = r.CommitTree(tree, Epoch.Add(time.Duration(i)*time.Minute), "c\n", parents...)
		}
		var page []struct{ SHA string }
		// Page 2 by 10 skips 50 commits with a path, 10 without.
		_, h := forgejoStatus(t, s, "/repos/o/long/commits?limit=10&page=2&path=x&sha="+tip.String(), &page)
		log, _ := r.Log(tip, LogOptions{})
		if len(page) != 10 || page[0].SHA != log[50].String() || h.Get("X-HasMore") != "true" {
			t.Fatalf("path: %v %v", page, h)
		}
		forgejoStatus(t, s, "/repos/o/long/commits?limit=10&page=2&sha="+tip.String(), &page)
		if page[0].SHA != log[10].String() {
			t.Fatalf("no path: %v", page)
		}
		// ":(literal)" is stripped; a glob matches paths in the tree.
		for _, p := range []string{":(literal)x", "%3F"} {
			_, h = forgejoStatus(t, s, "/repos/o/long/commits?limit=10&path="+p+"&sha="+tip.String(), &page)
			if len(page) != 10 || page[0].SHA != log[0].String() || h.Get("X-Total") != "120" {
				t.Fatalf("path %s: %v %v", p, page, h)
			}
		}
		if st, _ := forgejoStatus(t, s, "/repos/o/long/commits?path=:(literal)%3F&sha="+tip.String(), nil); st != 404 {
			t.Fatalf("literal ?: %d", st)
		}
	})

	t.Run("commit settings", func(t *testing.T) {
		s := NewServer(t, forgeauth.Forgejo, ForgejoAPIWith(ForgejoOptions{CommitsRangeSize: 20, MaxResponseItems: 10}))
		r := s.NewRepo("o/r")
		var tip gitobj.ID
		for i := range 60 {
			tree := r.BuildTree(map[string]File{"x": {Data: fmt.Appendf(nil, "%d", i)}})
			var parents []gitobj.ID
			if i > 0 {
				parents = []gitobj.ID{tip}
			}
			tip = r.CommitTree(tree, Epoch.Add(time.Duration(i)*time.Minute), "c\n", parents...)
		}
		log, _ := r.Log(tip, LogOptions{})
		var page []struct{ SHA string }
		// limit 50 is cut to MAX_RESPONSE_ITEMS; a path skips by
		// COMMITS_RANGE_SIZE.
		_, h := forgejoStatus(t, s, "/repos/o/r/commits?limit=50&page=2&path=x&sha="+tip.String(), &page)
		if len(page) != 10 || page[0].SHA != log[20].String() || h.Get("X-PerPage") != "10" {
			t.Fatalf("path: %d %v", len(page), h)
		}
	})

	t.Run("trees", func(t *testing.T) {
		all, _ := repo.ListTree(fx.Head, true)
		last := (len(all) + 4) / 5
		var tr struct {
			Tree       []map[string]any
			Truncated  bool
			TotalCount int `json:"total_count"`
		}
		path := fmt.Sprintf("%s/git/trees/%s?recursive=1&per_page=1000&page=%d", base, fx.Head, last)
		if st, _ := forgejoStatus(t, s, path, &tr); st != 200 || !tr.Truncated || tr.TotalCount != len(all) || len(tr.Tree) == 0 {
			t.Fatalf("last page: %d %+v", st, tr)
		}
		tr.Tree = nil
		path = fmt.Sprintf("%s/git/trees/%s?recursive=1&page=%d", base, fx.Head, last+1)
		if st, _ := forgejoStatus(t, s, path, &tr); st != 200 || tr.Truncated || tr.Tree != nil {
			t.Fatalf("past the end: %d %+v", st, tr)
		}
		if st, _ := forgejoStatus(t, s, base+"/git/trees/"+gitobj.ZeroID.String(), nil); st != 400 {
			t.Fatalf("missing: %d", st)
		}
	})

	t.Run("blobs", func(t *testing.T) {
		var b struct {
			Content string
			Size    int
		}
		if st, _ := forgejoStatus(t, s, base+"/git/blobs/"+fx.Blobs[PathLarge].String(), &b); st != 200 || b.Content != "" || b.Size != LargeSize {
			t.Fatalf("large: %d %d %d", st, len(b.Content), b.Size)
		}
		if st, _ := forgejoStatus(t, s, base+"/git/blobs/"+gitobj.ZeroID.String(), nil); st != 400 {
			t.Fatalf("missing: %d", st)
		}
	})

	t.Run("raw", func(t *testing.T) {
		req, _ := s.Session.NewRequest(context.Background(), http.MethodGet, base+"/raw/link?ref="+fx.Head.String(), nil)
		resp, err := s.Session.Client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(data) != PathReadme || resp.Header.Get("ETag") != `"`+fx.Blobs[PathSymlink].String()+`"` ||
			resp.Header.Get("X-Forgejo-Object-Type") != "symlink" {
			t.Fatalf("symlink: %q %v", data, resp.Header)
		}
		for _, p := range []string{"docs", PathSubmodule, "nope"} {
			if st, _ := forgejoStatus(t, s, base+"/raw/"+p+"?ref="+fx.Head.String(), nil); st != 404 {
				t.Fatalf("%s: %d", p, st)
			}
		}
	})

	t.Run("private", func(t *testing.T) {
		r := s.NewRepo("o/private")
		r.SetPrivate(true)
		s.SetAccess("o/private", AccessNone)
		err := s.Session.GetJSON(context.Background(), "/repos/o/private", nil)
		if !errors.Is(err, forgehttp.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
}
