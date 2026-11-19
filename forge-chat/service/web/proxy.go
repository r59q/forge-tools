// Package web serves the standalone app and forwards its agent API calls.
package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/r59q/forge-tools/forge-chat/service/store"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc/metadata"
)

// AgentProxy forwards the chat's forge-agents REST routes to a fixed upstream.
type AgentProxy struct {
	proxy   *httputil.ReverseProxy
	token   forgeauth.Credentials
	timeout time.Duration
	stream  time.Duration
}

// NewAgentProxy configures forwarding with the server-side forge-agents token.
func NewAgentProxy(target *url.URL, token forgeauth.Credentials, timeout, stream time.Duration) *AgentProxy {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Header = make(http.Header)
			for _, name := range []string{"Authorization", "Content-Type", "Accept"} {
				if value := r.In.Header.Get(name); value != "" {
					r.Out.Header.Set(name, value)
				}
			}
		},
		FlushInterval: -1,
		ModifyResponse: func(r *http.Response) error {
			if r.StatusCode >= 300 && r.StatusCode < 400 || r.StatusCode == http.StatusUnauthorized {
				return errors.New("unexpected forge-agents response")
			}
			r.Header.Del("Set-Cookie")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "Forge agents unavailable", http.StatusBadGateway)
		},
	}
	return &AgentProxy{proxy: proxy, token: token, timeout: timeout, stream: stream}
}

func (p *AgentProxy) serve(w http.ResponseWriter, r *http.Request, stream bool) {
	id, request := r.PathValue("id"), r.PathValue("request")
	if (id != "" && !store.ValidID(id)) || (request != "" && !store.ValidID(request)) {
		http.NotFound(w, r)
		return
	}
	timeout := p.timeout
	if stream {
		timeout = p.stream
		// The normal response deadline covers unary requests. A stream has
		// its own timeout and must flush incremental events until it ends.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout + time.Second))
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	token, err := p.token.Token(ctx)
	if err != nil {
		http.Error(w, "Forge agents unavailable", http.StatusBadGateway)
		return
	}
	out := r.Clone(ctx)
	out.Header.Set("Authorization", "Bearer "+token)
	p.proxy.ServeHTTP(w, out)
}

func (p *AgentProxy) routes(mux *http.ServeMux, browserToken forgeauth.Credentials) {
	for _, pattern := range []string{
		"GET /v1/agents", "POST /v1/agents", "GET /v1/agents/{id}", "GET /v1/agents/{id}/events",
		"POST /v1/agents/{id}/messages", "POST /v1/agents/{id}/inputs/{request}", "POST /v1/agents/{id}/stop",
	} {
		mux.Handle(pattern, authorize(browserToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p.serve(w, r, false)
		})))
	}
	mux.Handle("GET /v1/agents/{id}/stream", authorize(browserToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.serve(w, r, true)
	})))
}

func authorize(token forgeauth.Credentials, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := metadata.NewIncomingContext(r.Context(), metadata.MD{"authorization": r.Header.Values("Authorization")})
		if err := servicekit.Authorize(ctx, token); err != nil {
			http.Error(w, "Authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
