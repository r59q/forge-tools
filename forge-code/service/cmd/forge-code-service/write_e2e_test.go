package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-code/service/api"
	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-connector/library/connector"
	"github.com/r59q/forge-tools/forge-connector/library/proxy"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
)

// gitBrokerStub is a brokerStub whose issued tokens carry a Git base URL.
type gitBrokerStub struct {
	brokerStub
	gitBase string
}

func (b gitBrokerStub) IssueToken(ctx context.Context, id broker.ID) (broker.IssuedToken, error) {
	t, err := b.brokerStub.IssueToken(ctx, id)
	t.GitBaseURL = b.gitBase
	return t, err
}

// TestWritesThroughProxy drives CreateCommit and UpdateRefs over the
// service's REST API, through a real proxy handler, to git http-backend on
// a bare repository. A GitHub-shaped fake reads the same repository with
// git for the REST calls a commit needs.
func TestWritesThroughProxy(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	dir := t.TempDir()
	env := append(os.Environ(),
		"HOME="+dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(dir, "gitconfig"),
		"GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.com",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.com",
	)
	run := func(wd string, args ...string) (string, error) {
		cmd := exec.Command(gitBin, args...)
		cmd.Dir, cmd.Env = wd, env
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &stderr
		err := cmd.Run()
		if err != nil {
			err = fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, stderr.String())
		}
		return out.String(), err
	}
	git := func(wd string, args ...string) string {
		t.Helper()
		out, err := run(wd, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	backend := filepath.Join(git(dir, "--exec-path"), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend not found")
	}

	// A bare repository with README.md and dir/a.txt on main.
	root := filepath.Join(dir, "repos")
	bare := filepath.Join(root, "mrbean", "teddy.git")
	git(dir, "init", "--bare", "-b", "main", bare)
	git(bare, "config", "http.receivepack", "true")
	git(bare, "config", "receive.fsckObjects", "true")
	work := filepath.Join(dir, "work")
	git(dir, "init", "-b", "main", work)
	for name, content := range map[string]string{"README.md": "hello\n", "dir/a.txt": "a\n"} {
		p := filepath.Join(work, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(work, "add", ".")
	git(work, "commit", "-m", "first")
	git(work, "push", bare, "main")
	base := git(bare, "rev-parse", "refs/heads/main")

	gitAuth := http.Header{}
	github.SetGitToken(gitAuth, "forge")
	cgiHandler := &cgi.Handler{Path: backend, Root: "/git", Env: []string{
		"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(dir, "gitconfig"),
	}}
	const repo = "/repos/mrbean/teddy/git/"
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/git/") {
			if r.Header.Get("Authorization") != gitAuth.Get("Authorization") {
				http.Error(w, "denied", http.StatusUnauthorized)
				return
			}
			cgiHandler.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer forge" {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		if v, ok := githubFromGit(bare, r.URL.Path, repo, run); ok {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
			return
		}
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer forge.Close()

	stub := gitBrokerStub{brokerStub: brokerStub{"gh": {Kind: forgeauth.GitHub, BaseURL: forge.URL}}, gitBase: forge.URL + "/git"}
	reg := &forgeauth.Registry{}
	if err := reg.Register(github.Provider{}); err != nil {
		t.Fatal(err)
	}
	// service serves the REST API through a proxy with pushes on or off.
	service := func(allowPush bool) http.Handler {
		h, err := proxy.New(proxy.Config{Verifier: stub, TokenIssuer: stub, Connections: stub, Registry: reg, AllowGitPush: allowPush})
		if err != nil {
			t.Fatal(err)
		}
		p := httptest.NewServer(h)
		t.Cleanup(p.Close)
		conn, err := connector.New(connector.Config{URL: p.URL, Credentials: forgeauth.StaticToken("client")})
		if err != nil {
			t.Fatal(err)
		}
		s, err := api.New(sources(conn), forgeauth.StaticToken("service"), api.Config{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		rest, err := s.REST(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return rest
	}
	post := func(h http.Handler, path string, body any) (int, map[string]any) {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", path, bytes.NewReader(data))
		r.Header.Set("Authorization", "Bearer service")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	author := map[string]any{"name": "Mr Bean", "email": "bean@example.com", "when": "2026-01-02T03:04:05Z"}
	newCommit := func(old string) map[string]any {
		return map[string]any{
			"source": "gh", "repository": "mrbean/teddy", "parents": []string{base},
			"ref": "refs/heads/main", "old": old, "message": "Update\n", "author": author,
			"changes": []map[string]any{
				{"path": "README.md", "content": b64("hello world\n")},
				{"path": "dir/a.txt", "delete": true},
				{"path": "bin/run.sh", "content": b64("#!/bin/sh\n"), "mode": "100755"},
			},
		}
	}
	const zero = "0000000000000000000000000000000000000000"

	// Pushes off: the proxy hides the receive-pack route, so writes fail
	// with NOT_FOUND and nothing changes.
	closed := service(false)
	if code, _ := post(closed, "/v1/commits", newCommit(base)); code != http.StatusNotFound {
		t.Fatalf("commit with pushes off: %d, want 404", code)
	}
	if code, _ := post(closed, "/v1/ref-updates", map[string]any{"source": "gh", "repository": "mrbean/teddy",
		"updates": []map[string]any{{"name": "refs/heads/x", "old": zero, "new": base}}}); code != http.StatusNotFound {
		t.Fatalf("ref update with pushes off: %d, want 404", code)
	}
	if got := git(bare, "for-each-ref", "--format=%(refname) %(objectname)"); got != "refs/heads/main "+base {
		t.Fatalf("refs changed with pushes off: %q", got)
	}

	open := service(true)
	code, c := post(open, "/v1/commits", newCommit(base))
	if code != http.StatusOK {
		t.Fatalf("commit: %d %v", code, c)
	}
	id, _ := c["id"].(string)
	if got := git(bare, "rev-parse", "refs/heads/main"); got != id || len(id) != 40 {
		t.Fatalf("main is %s, commit %v", got, c)
	}
	if got := git(bare, "rev-parse", id+"^"); got != base {
		t.Fatalf("parent %s, want %s", got, base)
	}
	if got := git(bare, "cat-file", "blob", id+":README.md"); got != "hello world" {
		t.Fatalf("README.md: %q", got)
	}
	if got := git(bare, "ls-tree", "-r", "--format=%(objectmode) %(path)", id); got != "100644 README.md\n100755 bin/run.sh" {
		t.Fatalf("tree:\n%s", got)
	}
	if got := git(bare, "cat-file", "-p", id); !strings.Contains(got, "author Mr Bean <bean@example.com> 1767323045 +0000") || strings.Contains(got, "gpgsig") {
		t.Fatalf("commit object:\n%s", got)
	}
	git(bare, "fsck", "--strict")

	// A second commit from the old base is stale: ABORTED, main unchanged.
	if code, _ := post(open, "/v1/commits", newCommit(base)); code != http.StatusConflict {
		t.Fatalf("stale commit: %d, want 409", code)
	}

	// Create a branch and move main back in one atomic push.
	code, out := post(open, "/v1/ref-updates", map[string]any{"source": "gh", "repository": "mrbean/teddy", "updates": []map[string]any{
		{"name": "refs/heads/feature", "old": zero, "new": id},
		{"name": "refs/heads/main", "old": id, "new": base},
	}})
	if code != http.StatusOK {
		t.Fatalf("ref updates: %d %v", code, out)
	}
	if got := git(bare, "for-each-ref", "--format=%(refname) %(objectname)"); got != "refs/heads/feature "+id+"\nrefs/heads/main "+base {
		t.Fatalf("refs after update:\n%s", got)
	}

	// Stale: main is no longer at id, so nothing applies.
	code, _ = post(open, "/v1/ref-updates", map[string]any{"source": "gh", "repository": "mrbean/teddy", "updates": []map[string]any{
		{"name": "refs/heads/feature", "old": id, "new": zero},
		{"name": "refs/heads/main", "old": id, "new": base},
	}})
	if code != http.StatusConflict {
		t.Fatalf("stale ref update: %d, want 409", code)
	}
	if got := git(bare, "rev-parse", "refs/heads/feature"); got != id {
		t.Fatalf("feature moved by a stale update: %s", got)
	}
}

// githubFromGit answers the GitHub git/commits and git/trees reads from a
// bare repository, or reports false.
func githubFromGit(bare, path, prefix string, run func(string, ...string) (string, error)) (any, bool) {
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return nil, false
	}
	kind, sha, _ := strings.Cut(rest, "/")
	if len(sha) != 40 || strings.Trim(sha, "0123456789abcdef") != "" {
		return nil, false
	}
	switch kind {
	case "commits":
		out, err := run(bare, "show", "-s", "--format=%T%n%P%n%an%n%ae%n%aI%n%cn%n%ce%n%cI%n%B", sha+"^{commit}")
		if err != nil {
			return nil, false
		}
		f := strings.SplitN(out, "\n", 9)
		if len(f) < 9 {
			return nil, false
		}
		parents := []map[string]string{}
		for _, p := range strings.Fields(f[1]) {
			parents = append(parents, map[string]string{"sha": p})
		}
		return map[string]any{
			"sha": sha, "tree": map[string]string{"sha": f[0]}, "parents": parents,
			"author":    map[string]string{"name": f[2], "email": f[3], "date": f[4]},
			"committer": map[string]string{"name": f[5], "email": f[6], "date": f[7]},
			"message":   strings.TrimSuffix(f[8], "\n"),
		}, true
	case "trees":
		out, err := run(bare, "ls-tree", "-z", "-l", sha+"^{tree}")
		if err != nil {
			return nil, false
		}
		items := []map[string]any{}
		for _, line := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
			if line == "" {
				continue
			}
			meta, name, _ := strings.Cut(line, "\t")
			f := strings.Fields(meta) // mode type sha size
			item := map[string]any{"path": name, "mode": f[0], "type": f[1], "sha": f[2]}
			if n, err := strconv.ParseInt(f[3], 10, 64); err == nil {
				item["size"] = n
			}
			items = append(items, item)
		}
		return map[string]any{"sha": sha, "tree": items, "truncated": false}, true
	}
	return nil, false
}
