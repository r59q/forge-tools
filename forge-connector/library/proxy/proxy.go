// Package proxy authenticates scoped clients and streams forge API requests
// without exposing forge credentials. Broker adapters are trusted dependencies.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Verifier supplies connection metadata and checks the caller's scope. Service
// adapters translate broker metadata and errors into these library types.
type Verifier interface {
	// Connection returns the kind and API base URL, never a credential.
	Connection(ctx context.Context, id broker.ID) (forgeauth.Connection, error)
	// VerifyClient authenticates token and authorizes its use of id.
	VerifyClient(ctx context.Context, token forgeauth.StaticToken, id broker.ID) (broker.Client, error)
}

// TokenIssuer retrieves forge credentials using the proxy's own authorized
// broker client. It must not use the caller's token or require callers to have
// CanIssueTokens. Service adapters bind that broker credential internally.
type TokenIssuer interface {
	// IssueToken returns the credential and endpoint for id.
	IssueToken(ctx context.Context, id broker.ID) (broker.IssuedToken, error)
}

// Config contains trusted broker adapters and HTTP configuration.
type Config struct {
	// Verifier looks up connections and authorizes callers.
	Verifier Verifier
	// TokenIssuer retrieves credentials only after caller authorization.
	TokenIssuer TokenIssuer
	// Connections serves GET /connections when set. Nil disables it.
	Connections ConnectionLister
	// Registry contains the explicitly registered forgeauth providers.
	Registry *forgeauth.Registry
	// Transport sends forge requests without redirects. Nil uses http.DefaultTransport.
	// It must not redirect requests or log credentials.
	Transport http.RoundTripper
	// PublicURL is the proxy's public HTTP(S) origin, without a path. If empty,
	// response URLs use the incoming Host and TLS state, ignoring forwarded headers.
	PublicURL string
	// AllowGitPush enables git-receive-pack on the Git route. When false,
	// push requests get 404 and only fetches are served.
	AllowGitPush bool
	// MaxGitPushBytes caps a push request body. Zero means
	// DefaultMaxGitPushBytes.
	MaxGitPushBytes int64
	// DownloadHosts allows credential-free HTTPS GET redirects to these DNS
	// hosts. A leading *. permits subdomains. Empty disables external downloads.
	// Configure only trusted object-storage hosts; no caller headers are sent.
	DownloadHosts []string
}

// Handler serves /c/{connectionID}/<API path>, the Git smart-HTTP route
// /g/{connectionID}/{repoPath}.git/{endpoint}, and GET /connections when
// Config.Connections is set. Use New to construct it. Mount
// it without path cleaning so encoded traversal is rejected, not redirected.
// Requests inherit the caller's context; the service should set deadlines.
type Handler struct {
	verifier      Verifier
	issuer        TokenIssuer
	lister        ConnectionLister
	registry      *forgeauth.Registry
	transport     http.RoundTripper
	publicURL     string
	allowPush     bool
	maxPush       int64
	downloadHosts []string
}

var _ http.Handler = (*Handler)(nil)

// errMetadataChanged reports that issued credentials no longer match the
// authorized connection.
var errMetadataChanged = errors.New("proxy: connection metadata changed")

// New validates config and constructs a Handler. Invalid configuration returns
// broker.ErrInvalid. The adapters and transport are shared across requests.
func New(config Config) (*Handler, error) {
	if config.Verifier == nil || config.TokenIssuer == nil || config.Registry == nil || config.MaxGitPushBytes < 0 {
		return nil, broker.ErrInvalid
	}

	for _, host := range config.DownloadHosts {
		if !validDownloadHost(host) {
			return nil, broker.ErrInvalid
		}
	}

	maxPush := config.MaxGitPushBytes
	if maxPush == 0 {
		maxPush = DefaultMaxGitPushBytes
	}

	publicURL := ""
	if config.PublicURL != "" {
		u, ok := baseURL(config.PublicURL)
		if !ok || (u.Path != "" && u.Path != "/") {
			return nil, broker.ErrInvalid
		}

		publicURL = u.Scheme + "://" + u.Host
	}

	rt := config.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}

	return &Handler{
		verifier:      config.Verifier,
		issuer:        config.TokenIssuer,
		lister:        config.Connections,
		registry:      config.Registry,
		transport:     rt,
		publicURL:     publicURL,
		allowPush:     config.AllowGitPush,
		maxPush:       maxPush,
		downloadHosts: append([]string(nil), config.DownloadHosts...),
	}, nil
}

// Format keeps adapter credentials out of formatted output.
func (*Handler) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte("proxy.Handler")) }

// ServeHTTP authorizes a request before issuing a token or contacting its forge.
// Proxy errors have fixed messages prefixed with "proxy:"; upstream statuses and
// bodies pass through, with Link and Location headers restricted to this route.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL != nil && r.URL.EscapedPath() == ConnectionsPath {
		h.connections(w, r)
		return
	}

	if r.URL != nil && strings.HasPrefix(r.URL.EscapedPath(), GitPrefix) {
		h.serveGit(w, r)
		return
	}

	h.serveAPI(w, r)
}

// serveAPI proxies one forge API request on the /c/ route.
func (h *Handler) serveAPI(w http.ResponseWriter, r *http.Request) {
	id, apiPath, code := route(r)
	if code != 0 {
		proxyError(w, code)
		return
	}

	conn, err := h.verifier.Connection(r.Context(), id)
	if err != nil {
		dependencyError(w, err)
		return
	}

	base, ok := baseURL(conn.BaseURL)
	if !ok {
		proxyError(w, http.StatusBadGateway)
		return
	}

	provider, err := h.registry.Provider(conn.Kind)
	if err != nil {
		proxyError(w, http.StatusBadGateway)
		return
	}

	token, ok := provider.TokenFromHeader(r.Header)
	if !ok {
		proxyError(w, http.StatusUnauthorized)
		return
	}

	issued, err := h.issue(r.Context(), token, id, conn)
	if err != nil {
		dependencyError(w, err)
		return
	}

	origin, ok := h.origin(r)
	if !ok {
		proxyError(w, http.StatusBadRequest)
		return
	}

	rewrite := urlRewrite{
		base:      base,
		target:    targetURL(base, apiPath, r.URL),
		proxyBase: origin + "/c/" + string(id),
	}

	// Downloads use the forge request's context, derived from r's by ReverseProxy.
	p := h.apiProxy(rewrite, provider, string(issued.Token)) //nolint:contextcheck // inherited via resp.Request
	p.ServeHTTP(&responseWriter{ResponseWriter: w, rewrite: rewrite.headers}, r)
}

// issue authorizes the caller's token for id, then issues the forge
// credential. It fails closed if metadata changed between authorization and
// issuance.
func (h *Handler) issue(ctx context.Context, token string, id broker.ID, conn forgeauth.Connection) (broker.IssuedToken, error) {
	if _, err := h.verifier.VerifyClient(ctx, forgeauth.StaticToken(token), id); err != nil {
		return broker.IssuedToken{}, err
	}

	issued, err := h.issuer.IssueToken(ctx, id)
	if err != nil {
		return broker.IssuedToken{}, err
	}

	if issued.Kind != conn.Kind || issued.BaseURL != conn.BaseURL || !validToken(string(issued.Token)) {
		return broker.IssuedToken{}, errMetadataChanged
	}

	return issued, nil
}

// urlRewrite maps forge URLs in response headers to the proxy route.
type urlRewrite struct {
	base      *url.URL
	target    *url.URL
	proxyBase string
}

func (u urlRewrite) headers(h http.Header) {
	rewriteHeaders(h, u.base, u.target, u.proxyBase)
}

// apiProxy sends the request to the forge with the issued token instead of
// the caller's credentials.
func (h *Handler) apiProxy(rewrite urlRewrite, provider forgeauth.Provider, token string) *httputil.ReverseProxy {
	p := h.reverseProxy()

	p.Rewrite = func(pr *httputil.ProxyRequest) {
		pr.Out.URL = rewrite.target
		pr.Out.Host = rewrite.target.Host
		stripCredentials(pr.Out.Header)
		// Credentials must not be smuggled as request trailers.
		pr.Out.Trailer = nil
		provider.SetToken(pr.Out.Header, token)
	}

	p.ModifyResponse = func(resp *http.Response) error {
		if err := h.download(resp); err != nil {
			return err
		}

		rewrite.headers(resp.Header)
		if resp.StatusCode != http.StatusSwitchingProtocols {
			resp.Body = &responseBody{ReadCloser: resp.Body, rewrite: func() {
				rewrite.headers(resp.Trailer)
			}}
		}

		return nil
	}

	return p
}

// reverseProxy returns a streaming proxy that answers transport failures
// with 502. ReverseProxy's default logger can include transport error
// messages, so logging is discarded.
func (h *Handler) reverseProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport:     h.transport,
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			proxyError(w, http.StatusBadGateway)
		},
	}
}

// origin returns the proxy's public origin for response URLs.
func (h *Handler) origin(r *http.Request) (string, bool) {
	origin := h.publicURL
	if origin == "" {
		origin = "http://" + r.Host
		if r.TLS != nil {
			origin = "https://" + r.Host
		}
	}

	if public, ok := baseURL(origin); !ok || public.Path != "" {
		return "", false
	}

	return origin, true
}

// Informational responses and trailers are sent outside ModifyResponse.
type responseWriter struct {
	http.ResponseWriter
	rewrite func(http.Header)
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 {
		w.rewrite(w.Header())
	}

	w.ResponseWriter.WriteHeader(code)
}

type responseBody struct {
	io.ReadCloser
	rewrite func()
}

func (b *responseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.rewrite()
	}

	return n, err
}

func stripCredentials(h http.Header) {
	for _, name := range []string{"Authorization", "Private-Token", "Cookie", "Cookie2", "Proxy-Authorization"} {
		h.Del(name)
	}
}

func validToken(token string) bool {
	if token == "" {
		return false
	}

	for i := range token {
		if token[i] <= ' ' || token[i] == 0x7f {
			return false
		}
	}

	return true
}

func dependencyError(w http.ResponseWriter, err error) {
	code := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, broker.ErrUnauthorized):
		code = http.StatusUnauthorized
	case errors.Is(err, broker.ErrForbidden):
		code = http.StatusForbidden
	case errors.Is(err, broker.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, context.DeadlineExceeded):
		code = http.StatusGatewayTimeout
	case errors.Is(err, errMetadataChanged):
		code = http.StatusBadGateway
	}

	proxyError(w, code)
}

func proxyError(w http.ResponseWriter, code int) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "proxy: "+http.StatusText(code), code)
}
