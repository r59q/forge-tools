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
	// Registry contains the explicitly registered forgeauth providers.
	Registry *forgeauth.Registry
	// Transport sends forge requests without redirects. Nil uses http.DefaultTransport.
	// It must not redirect requests or log credentials.
	Transport http.RoundTripper
	// PublicURL is the proxy's public HTTP(S) origin, without a path. If empty,
	// response URLs use the incoming Host and TLS state, ignoring forwarded headers.
	PublicURL string
}

// Handler serves /c/{connectionID}/<API path>. Use New to construct it. Mount
// it without path cleaning so encoded traversal is rejected, not redirected.
// Requests inherit the caller's context; the service should set deadlines.
type Handler struct {
	verifier  Verifier
	issuer    TokenIssuer
	registry  *forgeauth.Registry
	transport http.RoundTripper
	publicURL string
}

var _ http.Handler = (*Handler)(nil)

// New validates config and constructs a Handler. Invalid configuration returns
// broker.ErrInvalid. The adapters and transport are shared across requests.
func New(config Config) (*Handler, error) {
	if config.Verifier == nil || config.TokenIssuer == nil || config.Registry == nil {
		return nil, broker.ErrInvalid
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
	return &Handler{verifier: config.Verifier, issuer: config.TokenIssuer,
		registry: config.Registry, transport: rt, publicURL: publicURL}, nil
}

// Format keeps adapter credentials out of formatted output.
func (*Handler) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte("proxy.Handler")) }

// ServeHTTP authorizes a request before issuing a token or contacting its forge.
// Proxy errors have fixed messages prefixed with "proxy:"; upstream statuses and
// bodies pass through, with Link and Location headers restricted to this route.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	if _, err := h.verifier.VerifyClient(r.Context(), forgeauth.StaticToken(token), id); err != nil {
		dependencyError(w, err)
		return
	}
	issued, err := h.issuer.IssueToken(r.Context(), id)
	if err != nil {
		dependencyError(w, err)
		return
	}
	// Fail closed if metadata changed between authorization and issuance.
	if issued.Kind != conn.Kind || issued.BaseURL != conn.BaseURL || !validToken(string(issued.Token)) {
		proxyError(w, http.StatusBadGateway)
		return
	}
	target := targetURL(base, apiPath, r.URL)
	origin := h.publicURL
	if origin == "" {
		origin = "http://" + r.Host
		if r.TLS != nil {
			origin = "https://" + r.Host
		}
	}
	if public, ok := baseURL(origin); !ok || public.Path != "" {
		proxyError(w, http.StatusBadRequest)
		return
	}
	proxyBase := origin + "/c/" + string(id)
	p := httputil.ReverseProxy{
		Transport:     h.transport,
		FlushInterval: -1,
		// ReverseProxy's default logger can include transport error messages.
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			proxyError(w, http.StatusBadGateway)
		},
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = target
			pr.Out.Host = target.Host
			stripCredentials(pr.Out.Header)
			// Credentials must not be smuggled as request trailers.
			pr.Out.Trailer = nil
			provider.SetToken(pr.Out.Header, string(issued.Token))
		},
		ModifyResponse: func(resp *http.Response) error {
			rewriteHeaders(resp.Header, base, target, proxyBase)
			if resp.StatusCode != http.StatusSwitchingProtocols {
				resp.Body = &responseBody{ReadCloser: resp.Body, rewrite: func() {
					rewriteHeaders(resp.Trailer, base, target, proxyBase)
				}}
			}
			return nil
		},
	}
	p.ServeHTTP(&responseWriter{ResponseWriter: w, rewrite: func(h http.Header) {
		rewriteHeaders(h, base, target, proxyBase)
	}}, r)
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
	}
	proxyError(w, code)
}

func proxyError(w http.ResponseWriter, code int) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "proxy: "+http.StatusText(code), code)
}
