package proxy

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

func route(r *http.Request) (broker.ID, string, int) {
	u := r.URL
	if u == nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Opaque != "" ||
		u.Fragment != "" || r.Method == http.MethodConnect || !safePath(u.EscapedPath()) {
		return "", "", http.StatusBadRequest
	}
	path, ok := strings.CutPrefix(u.EscapedPath(), "/c/")
	if !ok {
		return "", "", http.StatusNotFound
	}
	id, apiPath, ok := strings.Cut(path, "/")
	if !ok || id == "" {
		return "", "", http.StatusNotFound
	}
	// Broker IDs are URL-safe opaque identifiers, never escaped path segments.
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", "", http.StatusBadRequest
		}
	}
	if strings.HasPrefix(apiPath, "/") || strings.Contains(apiPath, ":") {
		return "", "", http.StatusBadRequest
	}
	return broker.ID(id), apiPath, 0
}

// safePath checks every decoding layer so downstream URL decoding cannot turn
// an apparently harmless path into traversal. Encoded slashes remain usable
// for GitLab project names. Ambiguous or malformed nested escapes fail closed.
func safePath(path string) bool {
	for {
		if strings.ContainsAny(path, "\\\x00\r\n") || strings.HasPrefix(path, "//") {
			return false
		}
		for _, part := range strings.Split(path, "/") {
			if part == "." || part == ".." {
				return false
			}
		}
		if !strings.Contains(path, "%") {
			return true
		}
		decoded, err := url.PathUnescape(path)
		if err != nil {
			return false
		}
		path = decoded
	}
}

func baseURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || !safePath(u.EscapedPath()) {
		return nil, false
	}
	return u, true
}

func targetURL(base *url.URL, apiPath string, incoming *url.URL) *url.URL {
	u := *base
	u.RawPath = strings.TrimRight(base.EscapedPath(), "/") + "/" + apiPath
	u.Path, _ = url.PathUnescape(u.RawPath)
	u.RawQuery, u.ForceQuery = incoming.RawQuery, incoming.ForceQuery
	return &u
}

func rewriteHeaders(h http.Header, base, target *url.URL, proxyBase string) {
	links := forgehttp.ParseLinks(h)
	h.Del("Link")
	for _, link := range links {
		if rewritten, ok := rewriteURL(link.URL, base, target, proxyBase); ok {
			link.URL = rewritten
			h.Add("Link", link.String())
		}
	}
	locations := h.Values("Location")
	h.Del("Location")
	if len(locations) == 1 {
		if rewritten, ok := rewriteURL(locations[0], base, target, proxyBase); ok {
			h.Set("Location", rewritten)
		}
	}
}

func rewriteURL(raw string, base, target *url.URL, proxyBase string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || !safePath(u.EscapedPath()) {
		return "", false
	}
	u = target.ResolveReference(u)
	if !sameOrigin(u, base) {
		return "", false
	}
	basePath := strings.TrimRight(base.EscapedPath(), "/")
	path := u.EscapedPath()
	if path != basePath && !strings.HasPrefix(path, basePath+"/") {
		return "", false
	}
	// Use absolute proxy URLs so forgeauth.Session.NewRequest can consume them
	// directly without appending its own /c/{id} prefix again.
	proxy, _ := url.Parse(proxyBase)
	proxy.RawPath = proxy.EscapedPath() + strings.TrimPrefix(path, basePath)
	proxy.Path, _ = url.PathUnescape(proxy.RawPath)
	proxy.RawQuery, proxy.ForceQuery = u.RawQuery, u.ForceQuery
	proxy.Fragment, proxy.RawFragment = u.Fragment, u.RawFragment
	return proxy.String(), true
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}
