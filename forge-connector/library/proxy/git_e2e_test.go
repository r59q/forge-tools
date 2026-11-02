package proxy_test

import (
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
)

// TestGitEndToEnd pushes and fetches with the git CLI through a real
// Handler in front of git http-backend.
func TestGitEndToEnd(t *testing.T) {
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
	git := func(wd string, args ...string) string {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir, cmd.Env = wd, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	execPath := git(dir, "--exec-path")
	backend := filepath.Join(execPath, "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend not found")
	}
	root := filepath.Join(dir, "repos")
	bare := filepath.Join(root, "group", "sub", "project.git")
	git(dir, "init", "--bare", "-b", "main", bare)
	git(bare, "config", "http.receivepack", "true")

	// The forge accepts only the GitLab Git credential for the forge token.
	want := http.Header{}
	gitlab.SetGitToken(want, forgeToken)
	cgiHandler := &cgi.Handler{Path: backend, Root: "/git", Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	var mu sync.Mutex
	var protocols []string
	forge := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != want.Get("Authorization") {
			w.Header().Set("WWW-Authenticate", `Basic realm="forge"`)
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		protocols = append(protocols, r.Header.Get("Git-Protocol"))
		mu.Unlock()
		cgiHandler.ServeHTTP(w, r)
	}))
	b := &fakeBroker{conn: forgeauth.Connection{Kind: forgeauth.GitLab, BaseURL: forge.URL + "/api/v4"}, gitBase: forge.URL + "/git"}
	c := config(t, b)
	c.AllowGitPush = true
	p := serve(t, handler(t, c))
	remote := p.URL + "/g/conn/group/sub/project.git"
	auth := "http.extraHeader=Authorization: Bearer " + clientToken

	work := filepath.Join(dir, "work")
	git(dir, "init", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "file.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "file.txt")
	git(work, "commit", "-m", "first")
	head := git(work, "rev-parse", "HEAD")
	git(work, "-c", auth, "push", remote, "main")
	if got := git(bare, "rev-parse", "refs/heads/main"); got != head {
		t.Fatalf("pushed %s, forge has %s", head, got)
	}

	clone := filepath.Join(dir, "clone")
	git(dir, "-c", auth, "-c", "protocol.version=2", "clone", remote, clone)
	if got := git(clone, "rev-parse", "HEAD"); got != head {
		t.Fatalf("fetched %s, want %s", got, head)
	}
	mu.Lock()
	v2 := slices.Contains(protocols, "version=2")
	mu.Unlock()
	if !v2 {
		t.Errorf("Git-Protocol not forwarded: %q", protocols)
	}

	// Git's own Basic auth carries the client token as the password.
	basic := strings.Replace(remote, "://", "://git:"+clientToken+"@", 1)
	if out := git(dir, "ls-remote", basic, "refs/heads/main"); !strings.Contains(out, head+"\trefs/heads/main") {
		t.Fatalf("ls-remote with Basic auth = %q", out)
	}

	// A wrong client token never reaches the forge.
	cmd := exec.Command(gitBin, "-c", "http.extraHeader=Authorization: Bearer wrong", "ls-remote", remote)
	cmd.Dir, cmd.Env = dir, env
	if out, err := cmd.CombinedOutput(); err == nil || strings.Contains(string(out), head) {
		t.Fatalf("wrong token: %v\n%s", err, out)
	}

	// With push off, the same push is refused.
	c.AllowGitPush = false
	p = serve(t, handler(t, c))
	git(work, "commit", "--allow-empty", "-m", "second")
	cmd = exec.Command(gitBin, "-c", auth, "push", p.URL+"/g/conn/group/sub/project.git", "main")
	cmd.Dir, cmd.Env = work, env
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("push with AllowGitPush off succeeded\n%s", out)
	}
	if got := git(bare, "rev-parse", "refs/heads/main"); got != head {
		t.Fatal("forge ref moved")
	}
}
