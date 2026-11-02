package forgeauth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// GitRemote is an authenticated Git smart-HTTP endpoint: a forge's Git
// host, or a proxy in front of one.
type GitRemote struct {
	// BaseURL is the Git base URL, without a trailing slash, such as
	// https://github.com. Repositories live at BaseURL/<path>.git.
	BaseURL string
	// Client sends requests. It adds the token only to requests for
	// BaseURL's scheme and host, and follows redirects only within
	// BaseURL.
	Client *http.Client
}

// URL returns the repository URL for repoPath, such as "owner/name" or
// "group/sub/project": BaseURL + "/" + repoPath + ".git". repoPath follows
// the rules of forgerest.CleanPath.
func (g *GitRemote) URL(repoPath string) (string, error) {
	if g == nil || g.BaseURL == "" {
		return "", errors.New("forgeauth: no Git remote")
	}
	p, ok := cleanRepoPath(repoPath)
	if !ok {
		return "", errors.New("forgeauth: invalid repository path")
	}
	return strings.TrimRight(g.BaseURL, "/") + "/" + p + ".git", nil
}

// NewGitRemote returns a GitRemote for baseURL. setToken adds the token to
// a request's headers, such as a provider's SetGitToken. rt sends the
// requests; if nil, http.DefaultTransport is used.
//
// The token is read for each request and sent only to baseURL's scheme and
// host, also across redirects. Redirects that leave baseURL or change the
// method are refused.
func NewGitRemote(baseURL string, creds Credentials, setToken func(h http.Header, token string), rt http.RoundTripper) (*GitRemote, error) {
	if creds == nil {
		return nil, errors.New("forgeauth: nil credentials")
	}
	if setToken == nil {
		return nil, errors.New("forgeauth: nil setToken")
	}
	u, err := parseBaseURL(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, err
	}
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &GitRemote{
		BaseURL: strings.TrimRight(u.String(), "/"),
		Client: &http.Client{
			Transport: &transport{
				base:     rt,
				origin:   originOf(u),
				creds:    creds,
				setToken: setToken,
			},
			CheckRedirect: gitRedirect(u),
		},
	}, nil
}

// maxRedirects matches net/http's default limit.
const maxRedirects = 10

// gitRedirect refuses redirects that leave base or change the method.
func gitRedirect(base *url.URL) func(*http.Request, []*http.Request) error {
	root := strings.TrimRight(base.Path, "/")
	return func(req *http.Request, via []*http.Request) error {
		u := req.URL
		inside := originOf(u) == originOf(base) && u.User == nil &&
			(root == "" || u.Path == root || strings.HasPrefix(u.Path, root+"/"))
		for _, seg := range strings.Split(u.Path, "/") {
			if seg == "." || seg == ".." {
				inside = false
			}
		}
		switch {
		case len(via) >= maxRedirects:
			return fmt.Errorf("forgeauth: stopped after %d redirects", maxRedirects)
		case req.Method != via[0].Method:
			return fmt.Errorf("forgeauth: %s was redirected to %s", via[0].Method, req.Method)
		case !inside:
			return fmt.Errorf("forgeauth: redirect to %s leaves the Git remote", redact(u))
		}
		return nil
	}
}

// cleanRepoPath checks a repository path with the rules of
// forgerest.CleanPath, which this package cannot import.
func cleanRepoPath(path string) (string, bool) {
	p := strings.Trim(strings.TrimSpace(path), "/")
	segments := strings.Split(p, "/")
	if len(segments) < 2 {
		return "", false
	}
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, `%?#\`) {
			return "", false
		}
		for _, r := range seg {
			if r <= ' ' || r == 0x7f {
				return "", false
			}
		}
	}
	return p, true
}

// GitAuthenticator is implemented by providers that can authenticate Git
// smart-HTTP requests, which use a different header than the API.
type GitAuthenticator interface {
	// SetGitToken sets the forge's Git auth header in h.
	SetGitToken(h http.Header, token string)
}

// ErrNoGitURL is returned when no Git base URL can be derived from an API
// base URL.
var ErrNoGitURL = errors.New("forgeauth: Git base URL required")

// DefaultGitURL derives a forge's Git base URL from its API base URL. An
// empty apiBase means DefaultBaseURL(kind, "").
//
// GitHub maps https://api.github.com to https://github.com and
// https://api.<x>.ghe.com to https://<x>.ghe.com, and strips a trailing
// /api/v3 otherwise. GitLab strips /api/v4 and Forgejo strips /api/v1.
// Other URLs give an error wrapping ErrNoGitURL.
func DefaultGitURL(kind Kind, apiBase string) (string, error) {
	apiBase = strings.TrimSpace(apiBase)
	if apiBase == "" {
		var err error
		if apiBase, err = DefaultBaseURL(kind, ""); err != nil {
			return "", err
		}
	}
	u, err := parseBaseURL(apiBase)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(u.Path, "/")
	host := strings.ToLower(u.Hostname())
	var suffix string
	switch kind {
	case GitHub:
		if path == "" {
			if sub, ok := gitHubHost(host); ok {
				g := url.URL{Scheme: u.Scheme, Host: sub}
				if port := u.Port(); port != "" {
					g.Host += ":" + port
				}
				return g.String(), nil
			}
		}
		suffix = "/api/v3"
	case GitLab:
		suffix = "/api/v4"
	case Forgejo:
		suffix = "/api/v1"
	default:
		return "", fmt.Errorf("forgeauth: unknown kind %q", kind)
	}
	if !strings.HasSuffix(path, suffix) {
		return "", fmt.Errorf("%w: cannot derive it from %q", ErrNoGitURL, redact(u))
	}
	g := url.URL{Scheme: u.Scheme, Host: u.Host, Path: strings.TrimSuffix(path, suffix)}
	return g.String(), nil
}

// gitHubHost maps api.github.com and api.<x>.ghe.com to their Git hosts.
func gitHubHost(host string) (string, bool) {
	if host == "api.github.com" {
		return "github.com", true
	}
	sub, ok := strings.CutPrefix(host, "api.")
	if !ok {
		return "", false
	}
	name, ok := strings.CutSuffix(sub, ".ghe.com")
	if !ok || name == "" || strings.Contains(name, ".") {
		return "", false
	}
	return sub, true
}
