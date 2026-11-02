package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-connector/library/proxy"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Config configures the HTTP side of the service.
type Config struct {
	// Broker supplies connections, verification and tokens.
	Broker *Broker
	// Registry contains the registered forgeauth providers.
	Registry *forgeauth.Registry
	// Transport sends forge requests. Nil uses http.DefaultTransport.
	Transport http.RoundTripper
	// PublicURL is the proxy's public origin. See proxy.Config.PublicURL.
	PublicURL string
	// AllowGitPush enables pushes on the Git route. See proxy.Config.
	AllowGitPush bool
	// MaxGitPushBytes caps a push body. Zero uses the proxy default.
	MaxGitPushBytes int64
	// Logger receives request logs. Nil discards them.
	Logger *slog.Logger
}

// Server is the proxy's HTTP handler with request logging, a health endpoint
// and the 401 token-drop rule. Use New to construct it.
type Server struct {
	handler http.Handler
	log     *slog.Logger
	stopped atomic.Bool
}

var _ http.Handler = (*Server)(nil)

// New builds a Server around proxy.New.
func New(c Config) (*Server, error) {
	if c.Broker == nil {
		return nil, broker.ErrInvalid
	}
	transport := c.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	h, err := proxy.New(proxy.Config{
		Verifier: c.Broker, TokenIssuer: c.Broker, Connections: c.Broker, Registry: c.Registry,
		Transport: &dropOn401{base: transport, drop: c.Broker.DropToken},
		PublicURL: c.PublicURL, AllowGitPush: c.AllowGitPush, MaxGitPushBytes: c.MaxGitPushBytes,
	})
	if err != nil {
		return nil, err
	}
	logger := c.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Server{handler: h, log: logger}, nil
}

// Healthy reports whether the server is serving. Shutdown sets it false.
func (s *Server) Healthy() bool { return !s.stopped.Load() }

// SetStopping marks the server unhealthy so /healthz fails during shutdown.
func (s *Server) SetStopping() { s.stopped.Store(true) }

// ServeHTTP serves GET /healthz and forwards everything else, including
// GET /connections, to the proxy handler. It does not clean paths, as the handler rejects unsafe ones.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		s.healthz(w, r)
		return
	}
	start := time.Now()
	id := connectionID(r.URL.EscapedPath())
	rec := &recorder{ResponseWriter: w, status: http.StatusOK}
	ctx := context.WithValue(r.Context(), connectionKey{}, id)
	s.handler.ServeHTTP(rec, r.WithContext(ctx))
	// The query string is left out: forges accept tokens there.
	s.log.Info("request", "connection", string(id), "method", r.Method,
		"path", r.URL.Path, "status", rec.status, "duration", time.Since(start))
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !s.Healthy() {
		http.Error(w, "stopping", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte("ok\n"))
	}
}

type connectionKey struct{}

// connectionID extracts the ID from /c/{id}/... or /g/{id}/..., or "" if
// the path has none. Only URL-safe ID characters are accepted, so logs stay
// clean.
func connectionID(escapedPath string) broker.ID {
	rest, ok := strings.CutPrefix(escapedPath, "/c/")
	if !ok {
		if rest, ok = strings.CutPrefix(escapedPath, proxy.GitPrefix); !ok {
			return ""
		}
	}
	id, _, _ := strings.Cut(rest, "/")
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return ""
		}
	}
	return broker.ID(id)
}

// dropOn401 forgets the connection's cached token when the forge answers 401.
// The request is not retried; the next one fetches a fresh token.
type dropOn401 struct {
	base http.RoundTripper
	drop func(broker.ID)
}

func (t *dropOn401) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		if id, _ := req.Context().Value(connectionKey{}).(broker.ID); id != "" {
			t.drop(id)
		}
	}
	return resp, err
}

// CloseIdleConnections closes idle connections of the base transport.
func (t *dropOn401) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// recorder remembers the response status for logging.
type recorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *recorder) WriteHeader(code int) {
	// Informational responses are not the final status.
	if !r.wrote && (code < 100 || code >= 200 || code == http.StatusSwitchingProtocols) {
		r.status, r.wrote = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(p []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(p)
}
