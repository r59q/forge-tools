// Package forgeauth opens authenticated HTTP sessions to forges such as
// GitHub, GitLab and Forgejo.
//
// A Provider for each forge lives in a subpackage. Add the ones you need to
// a Registry, then open a Session for a Connection:
//
//	var reg forgeauth.Registry
//	reg.Register(github.Provider{})
//	s, err := reg.Open(ctx, forgeauth.Connection{Kind: forgeauth.GitHub}, forgeauth.EnvToken("GITHUB_TOKEN"))
//
// A Session's client sends the token only to the session's own scheme and
// host, also across redirects and for absolute URLs. Tokens never appear in
// errors or formatted output.
//
// For Git over HTTPS, NewGitRemote opens a GitRemote, DefaultGitURL derives
// the Git host from an API base URL, and providers implementing
// GitAuthenticator supply the Git auth header.
package forgeauth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Kind is a type of forge.
type Kind string

// Supported forge kinds.
const (
	// GitHub is github.com or GitHub Enterprise Server.
	GitHub Kind = "github"
	// GitLab is gitlab.com or a self-hosted GitLab.
	GitLab Kind = "gitlab"
	// Forgejo is a Forgejo server, such as Codeberg.
	Forgejo Kind = "forgejo"
)

// Connection says which forge to talk to.
type Connection struct {
	// Kind is the type of forge.
	Kind Kind
	// BaseURL is the forge's API base URL, such as https://api.github.com
	// or https://gitlab.example.com/api/v4. If empty, the provider uses
	// DefaultBaseURL(Kind, ""), which only works for GitHub.
	BaseURL string
	// Transport sends the session's requests. If nil, http.DefaultTransport
	// is used.
	Transport http.RoundTripper
}

// ErrNoBaseURL is returned when a forge kind has no default API URL and none
// was given.
var ErrNoBaseURL = errors.New("forgeauth: base URL required")

// DefaultBaseURL returns the API base URL for a forge of the given kind
// served at host, such as "https://gitlab.example.com". A host without a
// scheme gets https.
//
// GitHub uses https://api.github.com for an empty host or github.com, and
// <host>/api/v3 for GitHub Enterprise Server. GitLab uses <host>/api/v4 and
// Forgejo uses <host>/api/v1; both need a host.
func DefaultBaseURL(kind Kind, host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		if kind == GitHub {
			return "https://api.github.com", nil
		}
		return "", fmt.Errorf("%w for %s", ErrNoBaseURL, kind)
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	u, err := parseBaseURL(host)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(u.String(), "/")
	switch kind {
	case GitHub:
		if h := strings.ToLower(u.Hostname()); (h == "github.com" || h == "www.github.com") && strings.Trim(u.Path, "/") == "" {
			return "https://api.github.com", nil
		}
		return base + "/api/v3", nil
	case GitLab:
		return base + "/api/v4", nil
	case Forgejo:
		return base + "/api/v1", nil
	}
	return "", fmt.Errorf("forgeauth: unknown kind %q", kind)
}

// parseBaseURL parses and checks an http or https URL with a host and no
// user info, query or fragment.
func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		// url.Error repeats the input; the URL is not secret, but keep the
		// message short.
		return nil, fmt.Errorf("forgeauth: invalid base URL: %w", errors.Unwrap(err))
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("forgeauth: base URL %q must use http or https", redact(u))
	case u.Host == "":
		return nil, fmt.Errorf("forgeauth: base URL %q has no host", redact(u))
	case u.User != nil:
		return nil, fmt.Errorf("forgeauth: base URL %q must not contain user info", redact(u))
	case u.RawQuery != "" || u.Fragment != "":
		return nil, fmt.Errorf("forgeauth: base URL %q must not have a query or fragment", redact(u))
	}
	return u, nil
}

func redact(u *url.URL) string {
	r := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}
	return r.String()
}
