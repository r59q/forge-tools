package proxy

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// ConnectionsPath is where the handler lists the caller's connections.
const ConnectionsPath = "/connections"

// ConnectionLister lists the connections in a client token's scope. Service
// adapters call the broker with the caller's token, never the proxy's own.
type ConnectionLister interface {
	// ClientConnections authenticates token and returns its connections.
	ClientConnections(ctx context.Context, token forgeauth.StaticToken) ([]broker.ClientConnection, error)
}

// connectionsJSON is the body of GET /connections.
type connectionsJSON struct {
	Connections []connectionJSON `json:"connections"`
}

type connectionJSON struct {
	ID     broker.ID      `json:"id"`
	Name   string         `json:"name,omitempty"`
	Kind   forgeauth.Kind `json:"kind"`
	Status broker.Status  `json:"status"`
}

func (h *Handler) connections(w http.ResponseWriter, r *http.Request) {
	if h.lister == nil {
		proxyError(w, http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		proxyError(w, http.StatusMethodNotAllowed)
		return
	}
	token, ok := forgeauth.AuthorizationToken(r.Header, "Bearer")
	if !ok {
		proxyError(w, http.StatusUnauthorized)
		return
	}
	values, err := h.lister.ClientConnections(r.Context(), forgeauth.StaticToken(token))
	if err != nil {
		dependencyError(w, err)
		return
	}
	body := connectionsJSON{Connections: []connectionJSON{}}
	for _, c := range values {
		body.Connections = append(body.Connections, connectionJSON(c))
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
