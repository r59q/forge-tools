package forgeauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// Session is an authenticated connection to a forge API.
type Session struct {
	// Kind is the type of forge.
	Kind Kind
	// BaseURL is the API base URL, without a trailing slash.
	BaseURL string
	// Client sends requests. It adds the token only to requests for
	// BaseURL's scheme and host.
	Client *http.Client
}

// URL resolves path against the session's BaseURL. A path that is already
// an absolute http or https URL, such as a Link header URL, is returned
// as is.
func (s *Session) URL(path string) string {
	if isAbsolute(path) {
		return path
	}
	return strings.TrimRight(s.BaseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

// NewRequest creates a request for path, resolved with URL.
func (s *Session) NewRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, method, s.URL(path), body)
}

// GetJSON sends a GET request for path and decodes the JSON response into
// v. Error responses are returned as *forgehttp.Error.
func (s *Session) GetJSON(ctx context.Context, path string, v any) error {
	req, err := s.NewRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := forgehttp.CheckResponse(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("forgeauth: decode %s: %w", forgehttp.RedactURL(req.URL), err)
	}
	return nil
}

func isAbsolute(path string) bool {
	p := strings.ToLower(path)
	return strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://")
}

// OpenSession opens a session for a forge of the given kind. Providers use
// it to implement Provider.Open; setToken adds the token to a request's
// headers in the forge's format.
//
// conn.Kind must be empty or equal kind. An empty conn.BaseURL is replaced
// by DefaultBaseURL(kind, ""). OpenSession reads a token once to check that
// the credentials work, but does not contact the forge.
func OpenSession(ctx context.Context, kind Kind, conn Connection, creds Credentials, setToken func(h http.Header, token string)) (*Session, error) {
	if conn.Kind != "" && conn.Kind != kind {
		return nil, fmt.Errorf("forgeauth: connection kind %q does not match provider %q", conn.Kind, kind)
	}
	if creds == nil {
		return nil, fmt.Errorf("forgeauth: nil credentials")
	}
	base := strings.TrimSpace(conn.BaseURL)
	if base == "" {
		var err error
		if base, err = DefaultBaseURL(kind, ""); err != nil {
			return nil, err
		}
	}
	u, err := parseBaseURL(base)
	if err != nil {
		return nil, err
	}
	tok, err := creds.Token(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkToken(tok); err != nil {
		return nil, err
	}
	rt := conn.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &Session{
		Kind:    kind,
		BaseURL: strings.TrimRight(u.String(), "/"),
		Client: &http.Client{Transport: &transport{
			base:     rt,
			origin:   originOf(u),
			creds:    creds,
			setToken: setToken,
		}},
	}, nil
}

// transport adds the token to requests for one origin only. The http.Client
// sends every request, including each redirect, through RoundTrip, so
// redirects to other hosts never get the token.
type transport struct {
	base     http.RoundTripper
	origin   string
	creds    Credentials
	setToken func(http.Header, string)
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || originOf(req.URL) != t.origin {
		return t.base.RoundTrip(req)
	}
	tok, err := t.creds.Token(req.Context())
	if err == nil {
		err = checkToken(tok)
	}
	if err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	// RoundTrippers must not change the caller's request.
	r := req.Clone(req.Context())
	t.setToken(r.Header, tok)
	return t.base.RoundTrip(r)
}

// Format prints the origin only. Without it, fmt would print the
// credentials field directly, bypassing their own redaction.
func (t *transport) Format(f fmt.State, _ rune) {
	fmt.Fprintf(f, "forgeauth.transport{origin: %s, creds: %s}", t.origin, redacted)
}

// CloseIdleConnections closes idle connections of the base transport.
func (t *transport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// originOf returns u's scheme and host in a canonical form: lower case,
// with the default port filled in. URLs with other schemes or no host get
// an empty origin, which never matches a session.
func originOf(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ""
	}
	port := u.Port()
	switch scheme {
	case "https":
		if port == "" {
			port = "443"
		}
	case "http":
		if port == "" {
			port = "80"
		}
	default:
		return ""
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}
