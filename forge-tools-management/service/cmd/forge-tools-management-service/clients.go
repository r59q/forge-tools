package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-connector/library/broker"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
)

// serviceClient is a connector client that management registers at startup,
// with a token from its configuration, for a service such as the proxy.
type serviceClient struct {
	name           string
	canIssueTokens bool
	file, env      string
	// optional marks the default environment source, skipped when unset.
	optional bool
	token    forgeauth.StaticToken
}

// logf reports registration progress. It never receives tokens.
var logf = log.Printf

// clientTokens reads the service client tokens once. Each must be long
// enough for the broker and differ from every other token in use.
func (s settings) clientTokens(ctx context.Context, others ...forgeauth.Credentials) ([]serviceClient, error) {
	seen := map[forgeauth.StaticToken]bool{}
	for _, c := range others {
		if token, err := c.Token(ctx); err == nil {
			seen[forgeauth.StaticToken(token)] = true
		}
	}
	var out []serviceClient
	for _, c := range s.clients {
		var source forgeauth.Credentials = forgeauth.EnvToken(c.env)
		if c.file != "" {
			source = forgeauth.FileToken(c.file)
		}
		token, err := source.Token(ctx)
		if err != nil && c.optional {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s client token required", c.name)
		}
		c.token = forgeauth.StaticToken(token)
		if len(token) < broker.MinClientTokenLength || strings.IndexFunc(token, func(r rune) bool { return r <= ' ' || r >= 0x7f }) >= 0 {
			return nil, fmt.Errorf("%s client token needs at least %d printable characters", c.name, broker.MinClientTokenLength)
		}
		if seen[c.token] {
			return nil, fmt.Errorf("%s client token must differ from the other tokens", c.name)
		}
		seen[c.token] = true
		out = append(out, c)
	}
	return out, nil
}

// register makes the connector accept each service client token, retrying
// while the broker is unavailable. Services using them get errors until then.
func register(ctx context.Context, client *management.Client, clients []serviceClient, timeout time.Duration) {
	delay := time.Second
	for _, c := range clients {
		for {
			attempt, cancel := context.WithTimeout(ctx, timeout)
			err := client.EnsureClient(attempt, c.name, c.canIssueTokens, c.token)
			cancel()
			if err == nil {
				logf("registered connector client %s", c.name)
				break
			}
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, management.ErrInvalid) {
				logf("connector rejected the %s client token", c.name)
				break
			}
			logf("connector unavailable; retrying %s client registration in %s", c.name, delay)
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			delay = min(2*delay, 30*time.Second)
		}
	}
}
