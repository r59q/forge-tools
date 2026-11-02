package api

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// forgeToken must never reach a caller.
const forgeToken = "forge-secret-sentinel"

// item is one tree entry of the fake forge.
type item struct {
	name, mode, sha string
}

// fakeCommit is one commit of the fake forge.
type fakeCommit struct {
	sha, tree, message string
	parents            []string
}

// fakeRepo is one repository of the fake forge.
type fakeRepo struct {
	def     string
	refs    map[string]map[string]string // "heads/main" -> {type, sha}
	tags    map[string]string            // annotated tag object -> commit
	commits map[string]fakeCommit
	trees   map[string][]item
	blobs   map[string][]byte
}

func newRepo() *fakeRepo {
	return &fakeRepo{refs: map[string]map[string]string{}, tags: map[string]string{}, commits: map[string]fakeCommit{},
		trees: map[string][]item{}, blobs: map[string][]byte{}}
}

func id(kind string, data []byte) string {
	h := sha1.Sum(append([]byte(kind+"\x00"), data...))
	return hex.EncodeToString(h[:])
}

func (r *fakeRepo) blob(content string) string {
	sha := id("blob", []byte(content))
	r.blobs[sha] = []byte(content)
	return sha
}

func (r *fakeRepo) tree(items ...item) string {
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
	data, _ := json.Marshal(fmt.Sprint(items))
	sha := id("tree", data)
	r.trees[sha] = items
	return sha
}

func (r *fakeRepo) commit(tree, message string, parents ...string) string {
	sha := id("commit", []byte(tree+message+strings.Join(parents, ",")))
	r.commits[sha] = fakeCommit{sha: sha, tree: tree, message: message, parents: parents}
	return sha
}

// GitHub-shaped fixture objects in mrbean/teddy.
var (
	fx                                       = newRepo()
	readme1, readme2, script, lfs, big, four string
	huge, broken, empty                      string
	binTree, libCommit, tree1, tree2         string
	c1, c2, c3, tagObj                       string
)

func init() {
	readme1 = fx.blob("hello\n")
	readme2 = fx.blob("hello world\n")
	script = fx.blob("#!/bin/sh\necho hi\n")
	lfs = fx.blob("version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("ab", 32) + "\nsize 12345\n")
	big = fx.blob(strings.Repeat("0123456789abcdef", 150<<10/16))
	four = fx.blob(strings.Repeat("x", MaxFileBytes))
	huge = fx.blob(strings.Repeat("y", MaxFileBytes+1))
	broken = fx.blob(strings.Repeat("z", 300000))
	empty = fx.blob("")
	libCommit = id("commit", []byte("elsewhere"))
	binTree = fx.tree(item{"run.sh", "100755", script})
	tree1 = fx.tree(item{"README.md", "100644", readme1})
	tree2 = fx.tree(
		item{"README.md", "100644", readme2},
		item{"bin", "040000", binTree},
		item{"link", "120000", fx.blob("README.md")},
		item{"lib", "160000", libCommit},
		item{"model.bin", "100644", lfs},
		item{"big.bin", "100644", big},
		item{"four.bin", "100644", four},
		item{"huge.bin", "100644", huge},
		item{"empty", "100644", empty},
	)
	c1 = fx.commit(tree1, "First\n")
	c2 = fx.commit(tree2, "Second\n", c1)
	c3 = fx.commit(tree1, "Side\n", c1)
	tagObj = id("tag", []byte("v1"))
	fx.tags[tagObj] = c1
	fx.def = "main"
	fx.refs["heads/main"] = map[string]string{"type": "commit", "sha": c2}
	fx.refs["heads/side"] = map[string]string{"type": "commit", "sha": c3}
	fx.refs["heads/dup"] = map[string]string{"type": "commit", "sha": c2}
	fx.refs["tags/dup"] = map[string]string{"type": "commit", "sha": c1}
	fx.refs["tags/v1"] = map[string]string{"type": "tag", "sha": tagObj}
}

// forge is a GitHub-shaped fake forge serving fx as mrbean/teddy. The
// repositories denied/x, limited/x, broken/x and expired/x fail with 403,
// rate-limited 403, 502 and 401; slow/x waits for release or the request's
// end. Blob broken stops mid-body.
type forge struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests int
	waiting  chan struct{} // receives one value per waiting slow request
	release  chan struct{} // closed to let slow requests finish
}

func newForge(t *testing.T) *forge {
	t.Helper()
	f := &forge{waiting: make(chan struct{}, 16), release: make(chan struct{})}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *forge) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func notFound(w http.ResponseWriter) {
	http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
}

func (f *forge) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/repos/"), "/", 3)
	if len(parts) < 2 {
		notFound(w)
		return
	}
	switch parts[0] {
	case "denied":
		http.Error(w, `{"message":"Must have admin rights"}`, http.StatusForbidden)
		return
	case "limited":
		w.Header().Set("X-RateLimit-Remaining", "0")
		http.Error(w, `{"message":"rate limited"}`, http.StatusForbidden)
		return
	case "broken":
		http.Error(w, `{"message":"upstream exploded with `+forgeToken+`"}`, http.StatusBadGateway)
		return
	case "expired":
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	case "slow":
		f.waiting <- struct{}{}
		select {
		case <-f.release:
		case <-r.Context().Done():
			return
		}
		writeJSON(w, map[string]string{"default_branch": "main"})
		return
	}
	if parts[0]+"/"+parts[1] != "mrbean/teddy" {
		notFound(w)
		return
	}
	rest := ""
	if len(parts) == 3 {
		rest = parts[2]
	}
	q := r.URL.Query()
	switch {
	case rest == "":
		writeJSON(w, map[string]string{"default_branch": fx.def})
	case strings.HasPrefix(rest, "git/commits/"):
		c, ok := fx.commits[strings.TrimPrefix(rest, "git/commits/")]
		if !ok {
			http.Error(w, `{"message":"No commit found"}`, http.StatusUnprocessableEntity)
			return
		}
		writeJSON(w, gitCommit(c))
	case strings.HasPrefix(rest, "git/ref/"):
		name := strings.TrimPrefix(rest, "git/ref/")
		obj, ok := fx.refs[name]
		if !ok {
			notFound(w)
			return
		}
		writeJSON(w, map[string]any{"ref": "refs/" + name, "object": obj})
	case strings.HasPrefix(rest, "git/matching-refs/"):
		prefix := strings.TrimPrefix(rest, "git/matching-refs/")
		out := []any{}
		for name, obj := range fx.refs {
			if strings.HasPrefix(name, prefix) {
				out = append(out, map[string]any{"ref": "refs/" + name, "object": obj})
			}
		}
		writeJSON(w, out)
	case rest == "tags":
		out := []any{}
		for name, obj := range fx.refs {
			if tag, ok := strings.CutPrefix(name, "tags/"); ok {
				sha := obj["sha"]
				if c, ok := fx.tags[sha]; ok {
					sha = c
				}
				out = append(out, map[string]any{"name": tag, "commit": map[string]string{"sha": sha}})
			}
		}
		writeJSON(w, out)
	case strings.HasPrefix(rest, "git/tags/"):
		c, ok := fx.tags[strings.TrimPrefix(rest, "git/tags/")]
		if !ok {
			notFound(w)
			return
		}
		writeJSON(w, map[string]any{"object": map[string]string{"type": "commit", "sha": c}})
	case rest == "commits":
		f.history(w, r, q)
	case strings.HasPrefix(rest, "git/trees/"):
		sha := strings.TrimPrefix(rest, "git/trees/")
		if _, ok := fx.trees[sha]; !ok {
			notFound(w)
			return
		}
		writeJSON(w, map[string]any{"sha": sha, "tree": listTree(sha, "", q.Get("recursive") != ""), "truncated": false})
	case strings.HasPrefix(rest, "git/blobs/"):
		f.blob(w, r, strings.TrimPrefix(rest, "git/blobs/"))
	case strings.HasPrefix(rest, "compare/"):
		a, b, _ := strings.Cut(strings.TrimPrefix(rest, "compare/"), "...")
		base := mergeBase(a, b)
		if base == "" {
			notFound(w)
			return
		}
		writeJSON(w, map[string]any{"merge_base_commit": map[string]string{"sha": base}})
	default:
		notFound(w)
	}
}

func gitCommit(c fakeCommit) map[string]any {
	person := map[string]string{"name": "Mr Bean", "email": "bean@example.com", "date": "2024-01-02T03:04:05Z"}
	parents := []any{}
	for _, p := range c.parents {
		parents = append(parents, map[string]string{"sha": p})
	}
	return map[string]any{"sha": c.sha, "tree": map[string]string{"sha": c.tree}, "parents": parents,
		"author": person, "committer": person, "message": c.message}
}

// history lists first-parent history from sha, one page at a time.
func (f *forge) history(w http.ResponseWriter, r *http.Request, q url.Values) {
	n, _ := strconv.Atoi(q.Get("per_page"))
	page, _ := strconv.Atoi(q.Get("page"))
	var all []fakeCommit
	for sha := q.Get("sha"); sha != ""; {
		c, ok := fx.commits[sha]
		if !ok {
			break
		}
		all = append(all, c)
		sha = ""
		if len(c.parents) > 0 {
			sha = c.parents[0]
		}
	}
	if len(all) == 0 {
		http.Error(w, `{"message":"No commit found"}`, http.StatusUnprocessableEntity)
		return
	}
	start := min((page-1)*n, len(all))
	end := min(start+n, len(all))
	out := []any{}
	for _, c := range all[start:end] {
		gc := gitCommit(c)
		out = append(out, map[string]any{"sha": c.sha, "commit": gc, "parents": gc["parents"]})
	}
	if end < len(all) {
		next := *r.URL
		nq := next.Query()
		nq.Set("page", strconv.Itoa(page+1))
		next.RawQuery = nq.Encode()
		w.Header().Set("Link", fmt.Sprintf(`<%s%s>; rel="next"`, f.srv.URL, next.RequestURI()))
	}
	writeJSON(w, out)
}

func listTree(sha, prefix string, recursive bool) []any {
	out := []any{}
	for _, it := range fx.trees[sha] {
		e := map[string]any{"path": prefix + it.name, "mode": it.mode, "sha": it.sha, "type": "blob"}
		switch it.mode {
		case "040000":
			e["type"] = "tree"
		case "160000":
			e["type"] = "commit"
		default:
			e["size"] = len(fx.blobs[it.sha])
		}
		out = append(out, e)
		if recursive && it.mode == "040000" {
			out = append(out, listTree(it.sha, prefix+it.name+"/", true)...)
		}
	}
	return out
}

// blob serves raw content. Blob broken declares its full size, sends a
// third of it and drops the connection.
func (f *forge) blob(w http.ResponseWriter, r *http.Request, sha string) {
	data, ok := fx.blobs[sha]
	if !ok {
		notFound(w)
		return
	}
	if r.Header.Get("Accept") != "application/vnd.github.raw+json" {
		http.Error(w, "want raw", http.StatusNotAcceptable)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	if sha == broken {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Write(data[:len(data)/3])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	// Content of 4 MiB or more is sent without a length, so its size is
	// unknown until read.
	if len(data) >= MaxFileBytes {
		w.Write(data[:1])
		w.(http.Flusher).Flush()
		w.Write(data[1:])
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

// mergeBase returns the first-parent ancestor of b that is also an
// ancestor of a, or "".
func mergeBase(a, b string) string {
	seen := map[string]bool{}
	for sha := a; sha != ""; {
		seen[sha] = true
		c := fx.commits[sha]
		sha = ""
		if len(c.parents) > 0 {
			sha = c.parents[0]
		}
	}
	for sha := b; sha != ""; {
		if seen[sha] {
			return sha
		}
		c := fx.commits[sha]
		sha = ""
		if len(c.parents) > 0 {
			sha = c.parents[0]
		}
	}
	return ""
}

// compact compacts JSON for comparisons; protojson varies its whitespace
// on purpose.
func compact(t *testing.T, data []byte) string {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, data); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	return out.String()
}
