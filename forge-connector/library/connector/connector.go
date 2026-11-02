// Package connector gives tools ready forge sessions through a
// forge-connector proxy. A tool asks for its connections and makes forge API
// calls with each session; the proxy adds the forge credential. Tools never
// handle forge tokens, connection URLs or authentication headers.
package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-connector/library/proxy"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// maxBody caps the size of a connection listing.
const maxBody = 1 << 20

// Config configures a Client.
type Config struct {
	// URL is the forge-connector proxy, such as http://proxy:8081.
	URL string
	// Credentials is the tool's broker client token. It is read on each
	// call, so rotating the source takes effect without a restart.
	Credentials forgeauth.Credentials
	// HTTPClient lists connections. Nil uses http.DefaultClient.
	HTTPClient *http.Client
}

// Client supplies sessions for the connections a client token may use. It is
// safe for concurrent use.
type Client struct {
	url      *url.URL
	creds    forgeauth.Credentials
	http     *http.Client
	registry *forgeauth.Registry
}

// Connection is a usable connection with an open session. Requests made with
// Session reach the connection's forge, authenticated by the proxy.
type Connection struct {
	// ID identifies the connection.
	ID broker.ID
	// Name is an owner-defined label.
	Name string
	// Kind is the forge kind, which selects the forge API.
	Kind forgeauth.Kind
	// Session sends forge API requests through the proxy.
	Session *forgeauth.Session
	// Git sends Git smart-HTTP requests through the proxy's Git route, such
	// as a fetch from Git.URL("owner/name"). The proxy serves pushes only
	// when it allows them.
	Git *forgeauth.GitRemote
}

// New validates c and returns a Client. It does not contact the proxy.
// Invalid configuration returns broker.ErrInvalid.
func New(c Config) (*Client, error) {
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || c.Credentials == nil {
		return nil, broker.ErrInvalid
	}
	u.Path, u.RawPath = strings.TrimRight(u.Path, "/"), ""
	hc := c.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	reg := &forgeauth.Registry{}
	for _, p := range []forgeauth.Provider{github.Provider{}, gitlab.Provider{}, forgejo.Provider{}} {
		if err := reg.Register(p); err != nil {
			return nil, err
		}
	}
	return &Client{url: u, creds: c.Credentials, http: hc, registry: reg}, nil
}

// Connections returns the usable connections in ID order, each with an open
// session. Connections that are not ok or of an unsupported kind are left
// out. It asks the proxy on every call, so changes apply at once. HTTP
// failures wrap forgehttp error classes; errors never contain the token.
func (c *Client) Connections(ctx context.Context) ([]Connection, error) {
	listed, err := c.list(ctx)
	if err != nil {
		return nil, err
	}
	result := []Connection{}
	for _, l := range listed {
		if l.Status != broker.StatusOK {
			continue
		}
		if _, err := c.registry.Provider(l.Kind); err != nil {
			continue
		}
		session, err := c.registry.Open(ctx, forgeauth.Connection{
			Kind:    l.Kind,
			BaseURL: c.url.JoinPath("c", string(l.ID)).String(),
		}, c.creds)
		if err != nil {
			return nil, fmt.Errorf("connector: connection %s: %w", l.ID, err)
		}
		git, err := forgeauth.NewGitRemote(c.url.JoinPath(strings.Trim(proxy.GitPrefix, "/"), string(l.ID)).String(), c.creds, setBearer, nil)
		if err != nil {
			return nil, fmt.Errorf("connector: connection %s: %w", l.ID, err)
		}
		result = append(result, Connection{ID: l.ID, Name: l.Name, Kind: l.Kind, Session: session, Git: git})
	}
	return result, nil
}

// setBearer sends the client token to the proxy's Git route.
func setBearer(h http.Header, token string) { h.Set("Authorization", "Bearer "+token) }

// list reads GET /connections with the client token.
func (c *Client) list(ctx context.Context) ([]broker.ClientConnection, error) {
	token, err := c.creds.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("connector: %w", forgeauth.ErrNoToken)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url.JoinPath(proxy.ConnectionsPath).String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connector: list connections: %w", err)
	}
	defer resp.Body.Close()
	if err := forgehttp.CheckResponse(resp); err != nil {
		return nil, err
	}
	var body struct {
		Connections []struct {
			ID     broker.ID      `json:"id"`
			Name   string         `json:"name"`
			Kind   forgeauth.Kind `json:"kind"`
			Status broker.Status  `json:"status"`
		} `json:"connections"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&body); err != nil {
		return nil, fmt.Errorf("connector: decode connections: %w", err)
	}
	result := make([]broker.ClientConnection, len(body.Connections))
	for i, l := range body.Connections {
		result[i] = broker.ClientConnection(l)
	}
	return result, nil
}
