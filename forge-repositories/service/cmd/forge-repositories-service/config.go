package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-repositories/library/repositories"
)

// config is the JSON configuration file. It names where tokens live, never
// the tokens themselves.
type config struct {
	// Proxy is a forge-connector proxy shared by every source with a
	// Connection.
	Proxy *proxyConfig `json:"proxy"`
	// Sources are the forge accounts to read.
	Sources []sourceConfig `json:"sources"`
}

type proxyConfig struct {
	URL       string `json:"url"`
	TokenEnv  string `json:"tokenEnv"`
	TokenFile string `json:"tokenFile"`
}

// sourceConfig is either a proxy connection (Connection) or a direct forge
// account (BaseURL, optional for public GitHub and GitLab, plus a token).
type sourceConfig struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Connection string `json:"connection"`
	BaseURL    string `json:"baseUrl"`
	TokenEnv   string `json:"tokenEnv"`
	TokenFile  string `json:"tokenFile"`
}

// maxConfig caps how much of the configuration file is read.
const maxConfig = 1 << 20

func readConfig(path string) (config, error) {
	f, err := os.Open(path)
	if err != nil {
		return config{}, errors.New("cannot open configuration file")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfig+1))
	if err != nil || len(data) > maxConfig {
		return config{}, errors.New("cannot read configuration file")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c config
	if err := dec.Decode(&c); err != nil || dec.More() {
		return config{}, errors.New("configuration file is not valid JSON for this service; see the README")
	}
	return c, nil
}

func tokenSource(env, file string) (forgeauth.Credentials, error) {
	switch {
	case env != "" && file != "":
		return nil, errors.New("choose one of tokenEnv and tokenFile")
	case file != "":
		return forgeauth.FileToken(file), nil
	case env != "":
		return forgeauth.EnvToken(env), nil
	}
	return nil, errors.New("tokenEnv or tokenFile is required")
}

// client opens a session per source and combines them. Opening checks that
// each token source has a token; sessions read it again for every request, so
// a rotated token file takes effect without a restart. Errors name the source
// but never include a token.
func (c config) client(ctx context.Context) (*repositories.Client, error) {
	if len(c.Sources) == 0 {
		return nil, errors.New("configuration lists no sources")
	}
	var reg forgeauth.Registry
	for _, p := range []forgeauth.Provider{github.Provider{}, gitlab.Provider{}, forgejo.Provider{}} {
		if err := reg.Register(p); err != nil {
			return nil, errors.New("cannot register forge provider")
		}
	}
	var proxyURL string
	var proxyCreds forgeauth.Credentials
	if c.Proxy != nil {
		u, err := url.Parse(c.Proxy.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("proxy: url must be an http or https URL")
		}
		if proxyCreds, err = tokenSource(c.Proxy.TokenEnv, c.Proxy.TokenFile); err != nil {
			return nil, fmt.Errorf("proxy: %w", err)
		}
		proxyURL = strings.TrimRight(u.String(), "/")
	}
	var sources []repositories.Source
	for i, s := range c.Sources {
		id := s.ID
		if id == "" {
			id = s.Connection
		}
		if id == "" {
			return nil, fmt.Errorf("source %d: id or connection is required", i+1)
		}
		conn := forgeauth.Connection{Kind: forgeauth.Kind(s.Kind), BaseURL: s.BaseURL}
		var creds forgeauth.Credentials
		switch {
		case s.Connection != "":
			if proxyURL == "" {
				return nil, fmt.Errorf("source %q: connection needs a proxy", id)
			}
			if s.BaseURL != "" || s.TokenEnv != "" || s.TokenFile != "" {
				return nil, fmt.Errorf("source %q: a proxy connection takes no baseUrl or token", id)
			}
			conn.BaseURL = proxyURL + "/c/" + url.PathEscape(s.Connection)
			creds = proxyCreds
		default:
			var err error
			if creds, err = tokenSource(s.TokenEnv, s.TokenFile); err != nil {
				return nil, fmt.Errorf("source %q: %w", id, err)
			}
		}
		if s.Kind == "" {
			return nil, fmt.Errorf("source %q: kind is required", id)
		}
		session, err := reg.Open(ctx, conn, creds)
		if err != nil {
			// Provider errors describe the kind, URL or missing token source,
			// never a token value.
			return nil, fmt.Errorf("source %q: %w", id, err)
		}
		sources = append(sources, repositories.Source{ID: id, Session: session})
	}
	client, err := repositories.New(sources...)
	if err != nil {
		return nil, err
	}
	return client, nil
}
