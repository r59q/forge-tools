package management

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// Connection errors. Use errors.Is to check them. They never include
// credentials or upstream details.
var (
	// ErrNotConfigured means no connector is configured.
	ErrNotConfigured = errors.New("management: connector not configured")
	// ErrInvalid means the input is invalid or the forge rejected the credential.
	ErrInvalid = errors.New("management: invalid input or rejected credential")
	// ErrNotFound means the connection does not exist.
	ErrNotFound = errors.New("management: connection not found")
	// ErrCheckFailed means a test of a stored credential failed, because the
	// forge rejected it or could not be reached. The status is updated.
	ErrCheckFailed = errors.New("management: connection check failed")
)

// ConnectorAdmin administers forge-connector. *broker.Manager implements it,
// as do adapters for the broker admin API. Implementations return broker
// errors, such as broker.ErrNotFound, so they can be told apart.
type ConnectorAdmin interface {
	ListForges(context.Context) ([]broker.Forge, error)
	CreateForge(ctx context.Context, kind forgeauth.Kind, baseURL, name string) (broker.Forge, error)
	DeleteForge(context.Context, broker.ID) error
	ListConnections(context.Context) ([]broker.Connection, error)
	CreateConnection(ctx context.Context, forgeID broker.ID, name string, credential forgeauth.StaticToken) (broker.Connection, error)
	TestConnection(context.Context, broker.ID) (broker.Connection, error)
	RotateCredential(context.Context, broker.ID, forgeauth.StaticToken) (broker.Connection, error)
	DeleteConnection(context.Context, broker.ID) error
	ListClients(context.Context) ([]broker.Client, error)
	RegisterClient(context.Context, broker.Client, forgeauth.StaticToken) (broker.Client, error)
	DeleteClient(context.Context, broker.ID) error
	// ClientConnections uses token as a client token. It fails with
	// broker.ErrUnauthorized when no client has that token.
	ClientConnections(ctx context.Context, token forgeauth.StaticToken) ([]broker.ClientConnection, error)
}

// Connection is a forge connection, without its credential.
type Connection struct {
	ID        string
	Name      string
	Kind      forgeauth.Kind
	BaseURL   string
	Login     string
	Status    broker.Status
	CheckedAt time.Time
}

// Connections distinguishes an absent connector from an empty list. It is
// valid only when ListConnections returns no error.
type Connections struct {
	Configured  bool
	Connections []Connection
}

// NewConnection describes a connection to add. Host is the forge's web
// address, such as gitlab.example.com. It defaults to github.com, gitlab.com
// or codeberg.org for its Kind.
type NewConnection struct {
	Kind       forgeauth.Kind
	Host       string
	Name       string
	Credential forgeauth.StaticToken
}

var defaultHosts = map[forgeauth.Kind]string{
	forgeauth.GitHub:  "github.com",
	forgeauth.GitLab:  "gitlab.com",
	forgeauth.Forgejo: "codeberg.org",
}

// ListConnections returns every connection, ordered by ID.
func (c *Client) ListConnections(ctx context.Context) (Connections, error) {
	if c.connector == nil {
		return Connections{Connections: []Connection{}}, ctx.Err()
	}
	forges, err := c.connector.ListForges(ctx)
	if err != nil {
		return Connections{}, connectorError(ctx, err)
	}
	conns, err := c.connector.ListConnections(ctx)
	if err != nil {
		return Connections{}, connectorError(ctx, err)
	}
	out := Connections{Configured: true, Connections: make([]Connection, 0, len(conns))}
	for _, conn := range conns {
		out.Connections = append(out.Connections, view(conn, forges))
	}
	return out, nil
}

// AddConnection checks the credential with the forge and stores it. It reuses
// a forge endpoint with the same kind and URL, or creates one.
func (c *Client) AddConnection(ctx context.Context, n NewConnection) (Connection, error) {
	if c.connector == nil {
		return Connection{}, ErrNotConfigured
	}
	host := strings.TrimSpace(n.Host)
	if host == "" {
		host = defaultHosts[n.Kind]
	}
	baseURL, err := forgeauth.DefaultBaseURL(n.Kind, host)
	if err != nil || strings.TrimSpace(string(n.Credential)) == "" {
		return Connection{}, ErrInvalid
	}
	forges, err := c.connector.ListForges(ctx)
	if err != nil {
		return Connection{}, connectorError(ctx, err)
	}
	i := slices.IndexFunc(forges, func(f broker.Forge) bool { return f.Kind == n.Kind && f.BaseURL == baseURL })
	var forge broker.Forge
	created := i < 0
	if created {
		if forge, err = c.connector.CreateForge(ctx, n.Kind, baseURL, host); err != nil {
			return Connection{}, connectorError(ctx, err)
		}
		forges = append(forges, forge)
	} else {
		forge = forges[i]
	}
	name := strings.TrimSpace(n.Name)
	conn, err := c.connector.CreateConnection(ctx, forge.ID, name, n.Credential)
	if err != nil {
		if created {
			_ = c.connector.DeleteForge(context.WithoutCancel(ctx), forge.ID)
		}
		return Connection{}, connectorError(ctx, err)
	}
	return view(conn, forges), nil
}

// TestConnection rechecks a stored credential with its forge. A rejected
// credential returns ErrCheckFailed; ListConnections shows the new status.
func (c *Client) TestConnection(ctx context.Context, id string) (Connection, error) {
	return c.update(ctx, id, func() (broker.Connection, error) {
		conn, err := c.connector.TestConnection(ctx, broker.ID(id))
		if errors.Is(err, broker.ErrInvalid) || errors.Is(err, broker.ErrInvalidCredential) {
			return conn, ErrCheckFailed
		}
		return conn, err
	})
}

// RotateCredential replaces a connection's credential after the forge accepts
// it. A rejected credential keeps the old one.
func (c *Client) RotateCredential(ctx context.Context, id string, credential forgeauth.StaticToken) (Connection, error) {
	if strings.TrimSpace(string(credential)) == "" {
		return Connection{}, ErrInvalid
	}
	return c.update(ctx, id, func() (broker.Connection, error) {
		return c.connector.RotateCredential(ctx, broker.ID(id), credential)
	})
}

// RemoveConnection deletes a connection and its credential. Its forge endpoint
// is deleted too once no connection uses it.
func (c *Client) RemoveConnection(ctx context.Context, id string) error {
	if c.connector == nil {
		return ErrNotConfigured
	}
	conns, err := c.connector.ListConnections(ctx)
	if err != nil {
		return connectorError(ctx, err)
	}
	i := slices.IndexFunc(conns, func(conn broker.Connection) bool { return conn.ID == broker.ID(id) })
	if i < 0 {
		return ErrNotFound
	}
	if err := c.connector.DeleteConnection(ctx, broker.ID(id)); err != nil {
		return connectorError(ctx, err)
	}
	// A forge still in use cannot be deleted; that error is expected.
	_ = c.connector.DeleteForge(ctx, conns[i].ForgeID)
	return nil
}

// EnsureClient makes sure the connector accepts token as the client token of
// a service named name, such as a proxy, granted every connection. If no
// client has token yet, every client named name is replaced by one that
// does. This applies a token from a deployment's configuration, including
// after rotation.
func (c *Client) EnsureClient(ctx context.Context, name string, canIssueTokens bool, token forgeauth.StaticToken) error {
	if c.connector == nil {
		return ErrNotConfigured
	}
	if name == "" || len(token) < broker.MinClientTokenLength {
		return ErrInvalid
	}
	_, err := c.connector.ClientConnections(ctx, token)
	if err == nil {
		return nil
	}
	if !errors.Is(err, broker.ErrUnauthorized) {
		return connectorError(ctx, err)
	}
	clients, err := c.connector.ListClients(ctx)
	if err != nil {
		return connectorError(ctx, err)
	}
	for _, client := range clients {
		if client.Name != name {
			continue
		}
		if err := c.connector.DeleteClient(ctx, client.ID); err != nil {
			return connectorError(ctx, err)
		}
	}
	_, err = c.connector.RegisterClient(ctx, broker.Client{Name: name, AllConnections: true, CanIssueTokens: canIssueTokens}, token)
	return connectorError(ctx, err)
}

func (c *Client) update(ctx context.Context, id string, fn func() (broker.Connection, error)) (Connection, error) {
	if c.connector == nil {
		return Connection{}, ErrNotConfigured
	}
	if id == "" {
		return Connection{}, ErrNotFound
	}
	conn, err := fn()
	if errors.Is(err, ErrCheckFailed) {
		return Connection{}, err
	}
	if err != nil {
		return Connection{}, connectorError(ctx, err)
	}
	forges, err := c.connector.ListForges(ctx)
	if err != nil {
		return Connection{}, connectorError(ctx, err)
	}
	return view(conn, forges), nil
}

func view(conn broker.Connection, forges []broker.Forge) Connection {
	out := Connection{
		ID: string(conn.ID), Name: conn.Name, Login: conn.Identity.Login,
		Status: conn.Status, CheckedAt: conn.CheckedAt,
	}
	if i := slices.IndexFunc(forges, func(f broker.Forge) bool { return f.ID == conn.ForgeID }); i >= 0 {
		out.Kind, out.BaseURL = forges[i].Kind, forges[i].BaseURL
	}
	return out
}

// connectorError keeps only a fixed error class, never upstream details.
func connectorError(ctx context.Context, err error) error {
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, broker.ErrInvalid), errors.Is(err, broker.ErrInvalidCredential), errors.Is(err, forgeauth.ErrUnknownKind):
		return ErrInvalid
	case errors.Is(err, broker.ErrNotFound):
		return ErrNotFound
	}
	return ErrUnavailable
}
