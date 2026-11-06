package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

const example = `{
  "webhooks": [
    {"id": "github", "forge": "github", "connection": "gh", "secretEnv": "TEST_REACTOR_SECRET"},
    {"id": "forgejo", "forge": "forgejo", "connection": "fj", "secretFile": "SECRET_FILE", "repositories": ["team/app"]}
  ],
  "polls": [{"id": "fj-main", "connection": "fj", "repository": "team/app", "branches": ["main"]}],
  "pollInterval": "30s"
}`

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "reactor.json")
	if err := os.WriteFile(path, []byte(strings.Replace(example, "SECRET_FILE", secret, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_REACTOR_SECRET", "env-secret")

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Sources) != 2 || c.Sources[0].Forge != forgeauth.GitHub || c.Sources[1].Repositories[0] != "team/app" ||
		len(c.Targets) != 1 || c.Targets[0].Branches[0] != "main" || c.Interval != 30*time.Second {
		t.Fatalf("got %+v", c)
	}
	for i, want := range []string{"env-secret", "file-secret"} {
		if got, err := c.Sources[i].Secret.Token(context.Background()); err != nil || got != want {
			t.Errorf("secret %d: %q %v", i, got, err)
		}
	}

	if c, err := Load(""); err != nil || len(c.Sources)+len(c.Targets) != 0 {
		t.Fatalf("empty path: %+v %v", c, err)
	}
	if _, err := Load(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("loaded a missing file")
	}
}

func TestParseRejects(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field":  `{"webhook": []}`,
		"two documents":  `{} {}`,
		"inline secret":  `{"webhooks": [{"id": "a", "forge": "github", "secret": "x"}]}`,
		"no secret":      `{"webhooks": [{"id": "a", "forge": "github"}]}`,
		"two secrets":    `{"webhooks": [{"id": "a", "forge": "github", "secretEnv": "A", "secretFile": "B"}]}`,
		"gitlab":         `{"webhooks": [{"id": "a", "forge": "gitlab", "secretEnv": "A"}]}`,
		"duplicate":      `{"webhooks": [{"id": "a", "forge": "github", "secretEnv": "A"}, {"id": "a", "forge": "forgejo", "secretEnv": "B"}]}`,
		"interval":       `{"pollInterval": "soon"}`,
		"short interval": `{"pollInterval": "10ms"}`,
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
