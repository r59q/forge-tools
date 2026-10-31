// Package service wires the forge-connector proxy handler to a broker service
// over gRPC. It adds short-lived caches, request logging and health checks.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	pb "github.com/r59q/forge-tools/forge-connector/library/brokerpb"
	"github.com/r59q/forge-tools/forge-connector/library/proxy"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Default cache lifetimes.
const (
	DefaultTokenTTL      = 60 * time.Second
	DefaultVerifyTTL     = 30 * time.Second
	DefaultBrokerTimeout = 10 * time.Second

	// maxDenyTTL bounds how long a denial is cached. It is also capped by the
	// approval lifetime, so a denial never outlives an approval.
	maxDenyTTL = 5 * time.Second
	// Cache size caps. Verification keys depend on caller input.
	maxTokens   = 1024
	maxVerified = 10000
)

// BrokerConfig configures a Broker.
type BrokerConfig struct {
	// Client calls the broker.
	Client pb.BrokerClient
	// Credentials is the proxy's own client token. The client needs
	// CanIssueTokens for every connection the proxy serves. It is read on each
	// broker call, so rotating the source takes effect without a restart.
	Credentials forgeauth.Credentials
	// TokenTTL is how long forge tokens are cached. Zero disables the cache.
	TokenTTL time.Duration
	// VerifyTTL is how long an approval is cached. Zero disables the cache.
	// Denials are cached for at most min(VerifyTTL, 5s).
	VerifyTTL time.Duration
	// Timeout bounds each broker call. Zero means no extra deadline.
	Timeout time.Duration
	// Logger receives operational warnings, never tokens. Nil discards them.
	Logger *slog.Logger
	// Now returns the current time. Nil uses time.Now. Tests set it.
	Now func() time.Time
}

// Broker implements proxy.Verifier and proxy.TokenIssuer with a broker gRPC
// client and in-memory caches. It is safe for concurrent use.
type Broker struct {
	client    pb.BrokerClient
	creds     forgeauth.Credentials
	tokenTTL  time.Duration
	verifyTTL time.Duration
	denyTTL   time.Duration
	timeout   time.Duration
	log       *slog.Logger
	tokens    *cache[broker.IssuedToken]
	verified  *cache[verdict]
}

var (
	_ proxy.Verifier    = (*Broker)(nil)
	_ proxy.TokenIssuer = (*Broker)(nil)
)

// verdict is a cached VerifyClient result: nil err means approved.
type verdict struct {
	client broker.Client
	err    error
}

// NewBroker validates config and returns a Broker.
func NewBroker(c BrokerConfig) (*Broker, error) {
	if c.Client == nil || c.Credentials == nil || c.TokenTTL < 0 || c.VerifyTTL < 0 || c.Timeout < 0 {
		return nil, broker.ErrInvalid
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	logger := c.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Broker{
		client: c.Client, creds: c.Credentials,
		tokenTTL: c.TokenTTL, verifyTTL: c.VerifyTTL, denyTTL: min(c.VerifyTTL, maxDenyTTL),
		timeout: c.Timeout, log: logger,
		tokens:   newCache[broker.IssuedToken](now, maxTokens),
		verified: newCache[verdict](now, maxVerified),
	}, nil
}

// Connection returns the connection's kind and API base URL. The proxy's
// broker client cannot read connection metadata, so this comes from the same
// cached IssueToken result that later supplies the credential. The credential
// is never returned.
func (b *Broker) Connection(ctx context.Context, id broker.ID) (forgeauth.Connection, error) {
	issued, err := b.IssueToken(ctx, id)
	if err != nil {
		return forgeauth.Connection{}, err
	}
	return forgeauth.Connection{Kind: issued.Kind, BaseURL: issued.BaseURL}, nil
}

// IssueToken returns the connection's forge token, from the cache when fresh.
func (b *Broker) IssueToken(ctx context.Context, id broker.ID) (broker.IssuedToken, error) {
	if issued, ok := b.tokens.get(string(id)); ok {
		return issued, nil
	}
	ctx, cancel := b.withTimeout(ctx)
	defer cancel()
	ctx, err := b.ownContext(ctx)
	if err != nil {
		return broker.IssuedToken{}, err
	}
	resp, err := b.client.IssueToken(ctx, &pb.IssueTokenRequest{ConnectionId: string(id)})
	if err != nil {
		return broker.IssuedToken{}, b.issueError(id, err)
	}
	issued := broker.IssuedToken{
		Kind: forgeauth.Kind(resp.GetKind()), BaseURL: resp.GetBaseUrl(),
		Token: forgeauth.StaticToken(resp.GetToken()),
	}
	b.tokens.set(string(id), issued, b.tokenTTL)
	return issued, nil
}

// DropToken forgets the cached forge token for id, for example after the forge
// rejected it.
func (b *Broker) DropToken(id broker.ID) { b.tokens.delete(string(id)) }

// VerifyClient asks the broker whether token may use connection id, using the
// caller's token as the bearer credential. Approvals and definitive denials
// are cached; broker failures never are.
func (b *Broker) VerifyClient(ctx context.Context, token forgeauth.StaticToken, id broker.ID) (broker.Client, error) {
	if !metadataSafe(string(token)) {
		return broker.Client{}, broker.ErrUnauthorized
	}
	// Hash the caller token so the cache never holds it.
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:]) + "/" + string(id)
	if v, ok := b.verified.get(key); ok {
		return v.client, v.err
	}
	ctx, cancel := b.withTimeout(ctx)
	defer cancel()
	ctx = bearer(ctx, string(token))
	resp, err := b.client.VerifyClient(ctx, &pb.VerifyClientRequest{ConnectionId: string(id)})
	if err != nil {
		err = brokerError(err)
		if errors.Is(err, broker.ErrUnauthorized) || errors.Is(err, broker.ErrForbidden) {
			b.verified.set(key, verdict{err: err}, b.denyTTL)
		}
		return broker.Client{}, err
	}
	ids := make([]broker.ID, len(resp.GetConnectionIds()))
	for i, cid := range resp.GetConnectionIds() {
		ids[i] = broker.ID(cid)
	}
	client := broker.Client{
		ID: broker.ID(resp.GetId()), Name: resp.GetName(),
		Connections: ids, CanIssueTokens: resp.GetCanIssueTokens(),
	}
	b.verified.set(key, verdict{client: client}, b.verifyTTL)
	return client, nil
}

func (b *Broker) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if b.timeout > 0 {
		return context.WithTimeout(ctx, b.timeout)
	}
	return ctx, func() {}
}

func (b *Broker) ownContext(ctx context.Context) (context.Context, error) {
	token, err := b.creds.Token(ctx)
	if err != nil || !metadataSafe(token) {
		b.log.Error("proxy broker token unavailable")
		return nil, broker.ErrUnavailable
	}
	return bearer(ctx, token), nil
}

// bearer replaces outgoing metadata, so a caller's own headers never leak into
// the broker call.
func bearer(ctx context.Context, token string) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
}

// metadataSafe reports whether token is a nonempty printable ASCII string,
// which gRPC accepts in a header value.
func metadataSafe(token string) bool {
	if token == "" {
		return false
	}
	for i := range token {
		if token[i] <= ' ' || token[i] >= 0x7f {
			return false
		}
	}
	return true
}

// issueError maps a failed IssueToken made with the proxy's own token. A
// rejection of that token is a proxy setup problem, not the caller's: report
// it as unavailable, and an out-of-scope connection as unknown.
func (b *Broker) issueError(id broker.ID, err error) error {
	switch status.Code(err) {
	case codes.Unauthenticated:
		b.log.Error("broker rejected the proxy token", "connection", string(id))
		return broker.ErrUnavailable
	case codes.PermissionDenied:
		b.log.Warn("proxy client lacks access or CanIssueTokens", "connection", string(id))
		return broker.ErrNotFound
	}
	return brokerError(err)
}

// brokerError maps a gRPC error to a broker error class. The gRPC message is
// dropped on purpose.
func brokerError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	switch status.Code(err) {
	case codes.Unauthenticated:
		return broker.ErrUnauthorized
	case codes.PermissionDenied:
		return broker.ErrForbidden
	case codes.NotFound:
		return broker.ErrNotFound
	case codes.DeadlineExceeded:
		return context.DeadlineExceeded
	}
	return broker.ErrUnavailable
}
