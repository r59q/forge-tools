package proxy

import (
	"encoding/base64"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// GitPrefix starts the Git smart-HTTP route:
// /g/{connectionID}/{repoPath}.git/{endpoint}.
const GitPrefix = "/g/"

// Git request body caps.
const (
	// DefaultMaxGitPushBytes caps a git-receive-pack request body unless
	// Config.MaxGitPushBytes is set.
	DefaultMaxGitPushBytes int64 = 2 << 30
	// MaxGitFetchBytes caps a git-upload-pack request body.
	MaxGitFetchBytes int64 = 16 << 20
)

// gitRealm replaces upstream WWW-Authenticate headers on the Git route.
const gitRealm = `Basic realm="forge-connector", charset="UTF-8"`

const (
	uploadPack  = "git-upload-pack"
	receivePack = "git-receive-pack"
	infoRefs    = "info/refs"
)

// Request headers sent upstream. Everything else, including credentials and
// cookies, is dropped.
var gitRequestHeaders = []string{"Accept", "Accept-Encoding", "Content-Encoding", "Git-Protocol", "User-Agent"}

// Response headers returned to the caller. Location and WWW-Authenticate are
// handled separately; everything else, including Set-Cookie, is dropped.
var gitResponseHeaders = []string{
	"Cache-Control", "Content-Encoding", "Content-Length", "Content-Type",
	"Date", "Expires", "Pragma", "Retry-After", "Vary",
}

// gitRoute is a parsed request on the Git route.
type gitRoute struct {
	id       broker.ID
	repo     string // "owner/name", without ".git"
	endpoint string // infoRefs, uploadPack or receivePack
	service  string // uploadPack or receivePack
}

// parseGitRoute checks the URL, method, query and content type before any
// broker call. It returns an HTTP status on failure.
func (h *Handler) parseGitRoute(r *http.Request) (gitRoute, int) {
	u := r.URL
	if u == nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return gitRoute{}, http.StatusBadRequest
	}
	rest, ok := strings.CutPrefix(u.EscapedPath(), GitPrefix)
	if !ok {
		return gitRoute{}, http.StatusNotFound
	}
	id, rest, ok := strings.Cut(rest, "/")
	if !ok || id == "" {
		return gitRoute{}, http.StatusNotFound
	}
	if !validID(id) {
		return gitRoute{}, http.StatusBadRequest
	}
	repo, endpoint, code := parseRepoPath(rest)
	if code != 0 {
		return gitRoute{}, code
	}
	route := gitRoute{id: broker.ID(id), repo: repo, endpoint: endpoint, service: endpoint}
	if endpoint == infoRefs {
		route.service = strings.TrimPrefix(u.RawQuery, "service=")
	}
	if !gitQuery(endpoint, u.RawQuery, u.ForceQuery) || (route.service == receivePack && !h.allowPush) {
		return gitRoute{}, http.StatusNotFound
	}
	method := http.MethodPost
	if endpoint == infoRefs {
		method = http.MethodGet
	}
	if r.Method != method {
		return gitRoute{endpoint: endpoint}, http.StatusMethodNotAllowed
	}
	if method == http.MethodPost {
		types := r.Header.Values("Content-Type")
		if len(types) != 1 {
			return gitRoute{}, http.StatusUnsupportedMediaType
		}
		if mt, _, err := mime.ParseMediaType(types[0]); err != nil || mt != "application/x-"+route.service+"-request" {
			return gitRoute{}, http.StatusUnsupportedMediaType
		}
		if enc := r.Header.Values("Content-Encoding"); len(enc) > 1 || len(enc) == 1 && enc[0] != "gzip" {
			return gitRoute{}, http.StatusUnsupportedMediaType
		}
	}
	return route, 0
}

// parseRepoPath splits "{repoPath}.git/{endpoint}" from the right. Repository
// segments use only [A-Za-z0-9._-] and are never all dots, so no escape,
// encoded slash or traversal can pass.
func parseRepoPath(path string) (repo, endpoint string, code int) {
	for _, e := range []string{infoRefs, uploadPack, receivePack} {
		if r, ok := strings.CutSuffix(path, ".git/"+e); ok {
			repo, endpoint = r, e
			break
		}
	}
	if endpoint == "" {
		return "", "", http.StatusNotFound
	}
	if repo == "" {
		return "", "", http.StatusBadRequest
	}
	for _, seg := range strings.Split(repo, "/") {
		if strings.Trim(seg, ".") == "" {
			return "", "", http.StatusBadRequest
		}
		for _, c := range seg {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return "", "", http.StatusBadRequest
			}
		}
	}
	return repo, endpoint, 0
}

// gitQuery reports whether the query is exactly the one endpoint allows.
func gitQuery(endpoint, rawQuery string, force bool) bool {
	if force {
		return false
	}
	if endpoint == infoRefs {
		return rawQuery == "service="+uploadPack || rawQuery == "service="+receivePack
	}
	return rawQuery == ""
}

func validID(id string) bool {
	// Broker IDs are URL-safe opaque identifiers, never escaped path segments.
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return id != ""
}

// gitToken reads the caller's client token from a Bearer header, or from the
// password of a Basic header, as Git clients send it.
func gitToken(h http.Header) (string, bool) {
	if token, ok := forgeauth.AuthorizationToken(h, "Bearer"); ok {
		return token, true
	}
	encoded, ok := forgeauth.AuthorizationToken(h, "Basic")
	if !ok {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", false
	}
	_, password, ok := strings.Cut(string(decoded), ":")
	if !ok || !validToken(password) {
		return "", false
	}
	return password, true
}

// serveGit proxies one allowed Git smart-HTTP request. The upstream comes only
// from the broker's Git base URL, redirects are never followed and only
// allow-listed headers cross the proxy in either direction.
func (h *Handler) serveGit(w http.ResponseWriter, r *http.Request) {
	route, code := h.parseGitRoute(r)
	if code != 0 {
		if code == http.StatusMethodNotAllowed {
			allow := http.MethodPost
			if route.endpoint == infoRefs {
				allow = http.MethodGet
			}
			w.Header().Set("Allow", allow)
		}
		proxyError(w, code)
		return
	}
	limit := MaxGitFetchBytes
	if route.service == receivePack {
		limit = h.maxPush
	}
	if r.ContentLength > limit {
		proxyError(w, http.StatusRequestEntityTooLarge)
		return
	}
	conn, err := h.verifier.Connection(r.Context(), route.id)
	if err != nil {
		gitDependencyError(w, err)
		return
	}
	provider, err := h.registry.Provider(conn.Kind)
	if err != nil {
		proxyError(w, http.StatusBadGateway)
		return
	}
	auth, ok := provider.(forgeauth.GitAuthenticator)
	if !ok {
		proxyError(w, http.StatusBadGateway)
		return
	}
	token, ok := gitToken(r.Header)
	if !ok {
		gitUnauthorized(w)
		return
	}
	if _, err := h.verifier.VerifyClient(r.Context(), forgeauth.StaticToken(token), route.id); err != nil {
		gitDependencyError(w, err)
		return
	}
	issued, err := h.issuer.IssueToken(r.Context(), route.id)
	if err != nil {
		gitDependencyError(w, err)
		return
	}
	// Fail closed if metadata changed between authorization and issuance.
	if issued.Kind != conn.Kind || issued.BaseURL != conn.BaseURL || !validToken(string(issued.Token)) {
		proxyError(w, http.StatusBadGateway)
		return
	}
	gitBase, ok := baseURL(issued.GitBaseURL)
	if !ok {
		proxyError(w, http.StatusBadGateway)
		return
	}
	origin, ok := h.origin(r)
	if !ok {
		proxyError(w, http.StatusBadRequest)
		return
	}
	target := gitTarget(gitBase, route.repo, route.endpoint, r.URL.RawQuery)
	proxyBase := origin + GitPrefix + string(route.id)
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}
	p := httputil.ReverseProxy{
		Transport:     h.transport,
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				proxyError(w, http.StatusRequestEntityTooLarge)
				return
			}
			proxyError(w, http.StatusBadGateway)
		},
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = target
			pr.Out.Host = target.Host
			out := http.Header{}
			for _, name := range gitRequestHeaders {
				if values := pr.In.Header.Values(name); len(values) > 0 {
					out[name] = append([]string(nil), values...)
				}
			}
			if protocol := out.Values("Git-Protocol"); len(protocol) != 1 || len(protocol[0]) > 256 {
				out.Del("Git-Protocol")
			}
			if pr.In.Method == http.MethodPost {
				out.Set("Content-Type", "application/x-"+route.service+"-request")
			} else {
				pr.Out.Body, pr.Out.ContentLength = nil, 0
			}
			pr.Out.Header = out
			pr.Out.Trailer = nil
			auth.SetGitToken(pr.Out.Header, string(issued.Token))
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode == http.StatusSwitchingProtocols {
				return errors.New("proxy: upgrade refused")
			}
			return nil
		},
	}
	p.ServeHTTP(&gitResponseWriter{rw: w, clean: func(hd http.Header, code int) {
		cleanGitHeaders(hd, code, gitBase, target, proxyBase, h.allowPush)
	}}, r)
}

// gitTarget builds the upstream URL. The query is set only from the allow-list.
func gitTarget(base *url.URL, repo, endpoint, rawQuery string) *url.URL {
	u := *base
	u.Path = strings.TrimRight(base.Path, "/") + "/" + repo + ".git/" + endpoint
	u.RawPath = ""
	u.RawQuery, u.ForceQuery = "", false
	if endpoint == infoRefs {
		u.RawQuery = rawQuery
	}
	return &u
}

// cleanGitHeaders keeps allow-listed response headers, replaces
// WWW-Authenticate and keeps Location only when it maps to an allowed route.
func cleanGitHeaders(hd http.Header, code int, gitBase, target *url.URL, proxyBase string, allowPush bool) {
	locations := hd.Values("Location")
	challenged := len(hd.Values("Www-Authenticate")) > 0
	for name := range hd {
		keep := false
		for _, allowed := range gitResponseHeaders {
			if http.CanonicalHeaderKey(name) == allowed {
				keep = true
			}
		}
		if !keep {
			delete(hd, name)
		}
	}
	if challenged || code == http.StatusUnauthorized {
		hd.Set("WWW-Authenticate", gitRealm)
	}
	if len(locations) == 1 {
		if rewritten, ok := gitLocation(locations[0], gitBase, target, proxyBase, allowPush); ok {
			hd.Set("Location", rewritten)
		}
	}
	hd.Set("X-Content-Type-Options", "nosniff")
}

// gitLocation maps an upstream Location to the proxy, or reports false.
func gitLocation(raw string, gitBase, target *url.URL, proxyBase string, allowPush bool) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return "", false
	}
	u = target.ResolveReference(u)
	if !sameOrigin(u, gitBase) {
		return "", false
	}
	root := strings.TrimRight(gitBase.EscapedPath(), "/")
	suffix, ok := strings.CutPrefix(u.EscapedPath(), root+"/")
	if !ok {
		return "", false
	}
	repo, endpoint, code := parseRepoPath(suffix)
	if code != 0 || !gitQuery(endpoint, u.RawQuery, u.ForceQuery) {
		return "", false
	}
	if !allowPush && (endpoint == receivePack || u.RawQuery == "service="+receivePack) {
		return "", false
	}
	location := proxyBase + "/" + repo + ".git/" + endpoint
	if u.RawQuery != "" {
		location += "?" + u.RawQuery
	}
	return location, true
}

// gitResponseWriter cleans headers when the status is written and drops
// trailers. It exposes no Unwrap, so the stream cannot be hijacked.
type gitResponseWriter struct {
	rw    http.ResponseWriter
	clean func(http.Header, int)
	wrote bool
	// discard absorbs header writes after the status, which can only be
	// trailers.
	discard http.Header
}

func (w *gitResponseWriter) Header() http.Header {
	if w.wrote {
		if w.discard == nil {
			w.discard = http.Header{}
		}
		return w.discard
	}
	return w.rw.Header()
}

func (w *gitResponseWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.clean(w.rw.Header(), code)
	if code >= 200 || code == http.StatusSwitchingProtocols {
		w.wrote = true
	}
	w.rw.WriteHeader(code)
}

func (w *gitResponseWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.rw.Write(p)
}

// FlushError streams progress output from the forge as it arrives.
func (w *gitResponseWriter) FlushError() error {
	return http.NewResponseController(w.rw).Flush()
}

func gitUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", gitRealm)
	proxyError(w, http.StatusUnauthorized)
}

func gitDependencyError(w http.ResponseWriter, err error) {
	if errors.Is(err, broker.ErrUnauthorized) {
		gitUnauthorized(w)
		return
	}
	dependencyError(w, err)
}
