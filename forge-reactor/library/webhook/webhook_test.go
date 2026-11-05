package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
)

const (
	githubPush = `{
  "ref": "refs/heads/main",
  "before": "1111111111111111111111111111111111111111",
  "after": "2222222222222222222222222222222222222222",
  "created": false, "deleted": false, "forced": true,
  "repository": {"full_name": "Owner/Repo", "pushed_at": 1767323045},
  "pusher": {"name": "octocat", "email": "octo@example.com"},
  "sender": {"login": "octocat"}
}`
	forgejoPush = `{
  "secret": "leaked-by-old-gitea",
  "ref": "refs/heads/feature/x",
  "before": "aaaa",
  "after": "bbbb",
  "repository": {"full_name": "team/app"},
  "pusher": {"login": "dev", "username": "dev"}
}`
	githubPull = `{
  "action": "opened",
  "number": 7,
  "repository": {"full_name": "Owner/Repo"},
  "pull_request": {
    "number": 7, "title": "Add thing", "html_url": "https://github.com/Owner/Repo/pull/7", "draft": true,
    "user": {"login": "octocat"}, "created_at": "2026-01-02T03:04:05Z",
    "head": {"ref": "topic", "sha": "3333", "repo": {"full_name": "fork/Repo"}},
    "base": {"ref": "main", "sha": "4444", "repo": {"full_name": "Owner/Repo"}}
  }
}`
	forgejoPull = `{
  "action": "opened",
  "number": 3,
  "repository": {"full_name": "team/app"},
  "pull_request": {
    "number": 3, "title": "Fix", "html_url": "https://forge.example/team/app/pulls/3",
    "user": {"login": "dev"}, "created_at": "2026-01-02T04:05:06+01:00",
    "head": {"ref": "fix", "sha": "5555", "repo": {"full_name": "team/app"}},
    "base": {"ref": "main", "sha": "6666", "repo": {"full_name": "team/app"}}
  }
}`
)

var (
	githubSource  = Source{ID: "github", Forge: forgeauth.GitHub, Connection: "gh", Secret: []byte("gh-secret")}
	forgejoSource = Source{ID: "forgejo", Forge: forgeauth.Forgejo, Connection: "fj", Secret: []byte("fj-secret")}
)

func sign(secret []byte, body string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))

	return hex.EncodeToString(mac.Sum(nil))
}

// githubHeaders signs body as GitHub does.
func githubHeaders(event, body string) http.Header {
	h := http.Header{}
	h.Set("X-GitHub-Event", event)
	h.Set("X-GitHub-Delivery", "72d3162e-cc78-11e3-81ab-4c9367dc0958")
	h.Set("X-Hub-Signature-256", "sha256="+sign(githubSource.Secret, body))
	h.Set("Authorization", "Bearer should-not-matter")

	return h
}

// forgejoHeaders signs body as Forgejo does, including its GitHub-style
// compatibility headers.
func forgejoHeaders(event, body string) http.Header {
	h := http.Header{}
	h.Set("X-Forgejo-Event", event)
	h.Set("X-Forgejo-Delivery", "fj-delivery-1")
	h.Set("X-Forgejo-Signature", sign(forgejoSource.Secret, body))
	h.Set("X-GitHub-Event", event)
	h.Set("X-GitHub-Delivery", "fj-delivery-1")

	return h
}

func parse(t *testing.T, src Source, h http.Header, body string) reactor.Input {
	t.Helper()
	res, err := Parse(src, h, []byte(body))
	if err != nil || res.Input == nil {
		t.Fatalf("Parse() = %+v, %v", res, err)
	}

	return *res.Input
}

func TestGitHubPush(t *testing.T) {
	in := parse(t, githubSource, githubHeaders("push", githubPush), githubPush)
	e := in.Event
	if in.Key != "webhook:github:72d3162e-cc78-11e3-81ab-4c9367dc0958" {
		t.Errorf("key = %q", in.Key)
	}
	if e.Type != reactor.TypeBranchUpdated || e.Origin != reactor.OriginWebhook || e.Source != "github" || e.Forge != forgeauth.GitHub ||
		e.Connection != "gh" || e.Repository != "Owner/Repo" || e.Subject != "refs/heads/main" ||
		!e.OccurredAt.Equal(time.Unix(1767323045, 0)) {
		t.Errorf("event = %+v", e)
	}
	var data reactor.BranchUpdate
	if err := json.Unmarshal(e.Data, &data); err != nil {
		t.Fatal(err)
	}
	want := reactor.BranchUpdate{
		Branch: "main", Before: "1111111111111111111111111111111111111111", After: "2222222222222222222222222222222222222222",
		Forced: true, Pusher: "octocat",
	}
	if data != want {
		t.Errorf("data = %+v", data)
	}
	if e.Native == nil || e.Native.Event != "push" || e.Native.Delivery != "72d3162e-cc78-11e3-81ab-4c9367dc0958" ||
		!strings.Contains(string(e.Native.Payload), `"sender"`) {
		t.Errorf("native = %+v", e.Native)
	}
}

func TestForgejoPushDropsSecretAndHeaders(t *testing.T) {
	in := parse(t, forgejoSource, forgejoHeaders("push", forgejoPush), forgejoPush)
	e := in.Event
	if e.Forge != forgeauth.Forgejo || e.Connection != "fj" || e.Repository != "team/app" || e.Subject != "refs/heads/feature/x" ||
		!e.OccurredAt.IsZero() || in.Key != "webhook:forgejo:fj-delivery-1" {
		t.Errorf("event = %+v, key %q", e, in.Key)
	}
	var data reactor.BranchUpdate
	if err := json.Unmarshal(e.Data, &data); err != nil || data.Branch != "feature/x" || data.Pusher != "dev" {
		t.Errorf("data = %+v, %v", data, err)
	}
	encoded, _ := json.Marshal(e)
	for _, leak := range []string{"leaked-by-old-gitea", "fj-secret", "Signature", "Authorization"} {
		if strings.Contains(string(encoded), leak) {
			t.Errorf("event contains %q: %s", leak, encoded)
		}
	}
}

func TestPullRequestOpened(t *testing.T) {
	for name, tc := range map[string]struct {
		src     Source
		headers http.Header
		body    string
		subject string
		want    reactor.PullRequest
		at      time.Time
	}{
		"github": {githubSource, githubHeaders("pull_request", githubPull), githubPull, "pulls/7", reactor.PullRequest{
			Number: 7, Title: "Add thing", URL: "https://github.com/Owner/Repo/pull/7", Author: "octocat", Draft: true,
			Head: reactor.Ref{Repository: "fork/Repo", Branch: "topic", Commit: "3333"},
			Base: reactor.Ref{Repository: "Owner/Repo", Branch: "main", Commit: "4444"},
		}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		"forgejo": {forgejoSource, forgejoHeaders("pull_request", forgejoPull), forgejoPull, "pulls/3", reactor.PullRequest{
			Number: 3, Title: "Fix", URL: "https://forge.example/team/app/pulls/3", Author: "dev",
			Head: reactor.Ref{Repository: "team/app", Branch: "fix", Commit: "5555"},
			Base: reactor.Ref{Repository: "team/app", Branch: "main", Commit: "6666"},
		}, time.Date(2026, 1, 2, 3, 5, 6, 0, time.UTC)},
	} {
		in := parse(t, tc.src, tc.headers, tc.body)
		var got reactor.PullRequest
		if err := json.Unmarshal(in.Event.Data, &got); err != nil {
			t.Fatal(err)
		}
		if in.Event.Type != reactor.TypePullRequestOpened || in.Event.Subject != tc.subject || got != tc.want || !in.Event.OccurredAt.Equal(tc.at) {
			t.Errorf("%s: event = %+v, data %+v", name, in.Event, got)
		}
	}
}

func TestIgnoredDeliveries(t *testing.T) {
	edit := func(body, from, to string) string { return strings.Replace(body, from, to, 1) }
	for name, tc := range map[string]struct {
		event, body, reason string
	}{
		"ping":           {"ping", `{"zen":"hi"}`, "unsupported event ping"},
		"issues":         {"issues", `{"action":"opened"}`, "unsupported event issues"},
		"tag":            {"push", edit(githubPush, "refs/heads/main", "refs/tags/v1"), "not a branch push"},
		"created flag":   {"push", edit(githubPush, `"created": false`, `"created": true`), "branch created"},
		"created zeros":  {"push", edit(githubPush, "1111111111111111111111111111111111111111", strings.Repeat("0", 40)), "branch created"},
		"deleted zeros":  {"push", edit(githubPush, "2222222222222222222222222222222222222222", strings.Repeat("0", 40)), "branch deleted"},
		"pull closed":    {"pull_request", edit(githubPull, `"opened"`, `"closed"`), "pull request action closed"},
		"pull reopened":  {"pull_request", edit(githubPull, `"opened"`, `"reopened"`), "pull request action reopened"},
		"pull synchrony": {"pull_request", edit(githubPull, `"opened"`, `"synchronize"`), "pull request action synchronize"},
	} {
		res, err := Parse(githubSource, githubHeaders(tc.event, tc.body), []byte(tc.body))
		if err != nil || res.Input != nil || res.Ignored != tc.reason {
			t.Errorf("%s: Parse() = %+v, %v; want ignored %q", name, res, err, tc.reason)
		}
	}
}

func TestRejectedDeliveries(t *testing.T) {
	without := func(h http.Header, key string) http.Header { h.Del(key); return h }
	with := func(h http.Header, key, value string) http.Header { h.Set(key, value); return h }
	resigned := func(body string) (http.Header, string) { return githubHeaders("push", body), body }

	notObject, _ := resigned(`[1,2]`)
	truncated, _ := resigned(`{"ref":`)
	noRepo, _ := resigned(strings.Replace(githubPush, `"full_name": "Owner/Repo", `, "", 1))

	for name, tc := range map[string]struct {
		src     Source
		headers http.Header
		body    string
		want    error
	}{
		"missing signature": {githubSource, without(githubHeaders("push", githubPush), "X-Hub-Signature-256"), githubPush, ErrSignature},
		"wrong secret": {githubSource, with(githubHeaders("push", githubPush), "X-Hub-Signature-256", "sha256="+sign([]byte("other"), githubPush)),
			githubPush, ErrSignature},
		"sha1 only":      {githubSource, with(githubHeaders("push", githubPush), "X-Hub-Signature-256", "sha1=abc"), githubPush, ErrSignature},
		"not hex":        {githubSource, with(githubHeaders("push", githubPush), "X-Hub-Signature-256", "sha256=zz"), githubPush, ErrSignature},
		"tampered body":  {githubSource, githubHeaders("push", githubPush), strings.Replace(githubPush, "main", "prod", 1), ErrSignature},
		"forgejo no sig": {forgejoSource, without(forgejoHeaders("push", forgejoPush), "X-Forgejo-Signature"), forgejoPush, ErrSignature},
		"github at forgejo": {
			Source{ID: "forgejo", Forge: forgeauth.Forgejo, Secret: githubSource.Secret},
			with(githubHeaders("push", githubPush), "X-Forgejo-Signature", sign(githubSource.Secret, githubPush)), githubPush, ErrMismatch,
		},
		"forgejo at github": {
			Source{ID: "github", Forge: forgeauth.GitHub, Secret: forgejoSource.Secret},
			with(forgejoHeaders("push", forgejoPush), "X-Hub-Signature-256", "sha256="+sign(forgejoSource.Secret, forgejoPush)),
			forgejoPush, ErrMismatch,
		},
		"disallowed repo": {
			Source{ID: "github", Forge: forgeauth.GitHub, Secret: githubSource.Secret, Repositories: []string{"owner/other"}},
			githubHeaders("push", githubPush), githubPush, ErrMismatch,
		},
		"no delivery":  {githubSource, without(githubHeaders("push", githubPush), "X-GitHub-Delivery"), githubPush, ErrMalformed},
		"no event":     {githubSource, without(githubHeaders("push", githubPush), "X-GitHub-Event"), githubPush, ErrMismatch},
		"bad delivery": {githubSource, with(githubHeaders("push", githubPush), "X-GitHub-Delivery", "a\x01b"), githubPush, ErrMalformed},
		"not object":   {githubSource, notObject, `[1,2]`, ErrMalformed},
		"truncated":    {githubSource, truncated, `{"ref":`, ErrMalformed},
		"no repository": {githubSource, noRepo,
			strings.Replace(githubPush, `"full_name": "Owner/Repo", `, "", 1), ErrMalformed},
		"invalid source": {Source{ID: "github", Forge: forgeauth.GitLab, Secret: []byte("x")}, githubHeaders("push", githubPush), githubPush,
			reactor.ErrInvalid},
		"empty secret": {Source{ID: "github", Forge: forgeauth.GitHub}, githubHeaders("push", githubPush), githubPush, reactor.ErrInvalid},
	} {
		res, err := Parse(tc.src, tc.headers, []byte(tc.body))
		if !errors.Is(err, tc.want) || res.Input != nil {
			t.Errorf("%s: Parse() = %+v, %v; want %v", name, res, err, tc.want)
		}
	}
}

func TestAllowedRepositoryIgnoresCase(t *testing.T) {
	src := githubSource
	src.Repositories = []string{"owner/repo"}
	parse(t, src, githubHeaders("push", githubPush), githubPush)
}
