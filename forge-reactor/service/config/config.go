// Package config reads the trusted forge-reactor configuration: webhook
// sources and poll targets. Webhook secrets are named by file or variable,
// never written into the configuration itself.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-reactor/library/poll"
	"github.com/r59q/forge-tools/forge-reactor/library/webhook"
	"github.com/r59q/forge-tools/forge-reactor/service/api"
)

// maxFile caps the configuration file.
const maxFile = 1 << 20

// File is the JSON configuration file. Unknown fields are rejected.
type File struct {
	// Webhooks are the sources that may deliver native webhooks.
	Webhooks []Webhook `json:"webhooks"`
	// Polls are the Forgejo branches to poll through forge-connector.
	Polls []Poll `json:"polls"`
	// PollInterval is a Go duration such as 1m; empty uses one minute.
	PollInterval string `json:"pollInterval"`
}

// Webhook configures one source, served at /webhooks/{id}.
type Webhook struct {
	// ID names the source.
	ID string `json:"id"`
	// Forge is github or forgejo.
	Forge string `json:"forge"`
	// Connection is the forge-connector connection ID its events carry.
	Connection string `json:"connection"`
	// SecretFile is a file holding the webhook secret.
	SecretFile string `json:"secretFile"`
	// SecretEnv is a variable holding the webhook secret.
	SecretEnv string `json:"secretEnv"`
	// Repositories, if set, are the only owner/name paths accepted.
	Repositories []string `json:"repositories"`
}

// Poll configures one poll target.
type Poll struct {
	// ID names the target; its events carry it as their source.
	ID string `json:"id"`
	// Connection is the forge-connector connection to read with.
	Connection string `json:"connection"`
	// Repository is the owner/name path.
	Repository string `json:"repository"`
	// Branches are the branch names to watch.
	Branches []string `json:"branches"`
}

// Config is a checked configuration.
type Config struct {
	// Sources are the webhook sources.
	Sources []api.Source
	// Targets are the poll targets.
	Targets []poll.Target
	// Interval is the poll interval.
	Interval time.Duration
}

// Load reads and checks the file at path. An empty path is an empty
// configuration. Errors never contain secrets.
func Load(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	b, err := os.ReadFile(path) //nolint:gosec // the operator names the configuration file.
	if err != nil {
		return Config{}, errors.New("cannot read configuration file")
	}
	if len(b) > maxFile {
		return Config{}, errors.New("configuration file is too large")
	}

	return Parse(b)
}

// Parse checks an encoded configuration file.
func Parse(b []byte) (Config, error) {
	var f File
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil || dec.More() {
		return Config{}, errors.New("invalid configuration file")
	}

	var c Config
	seen := map[string]bool{}
	for _, w := range f.Webhooks {
		s, err := w.source()
		if err != nil || seen[w.ID] {
			return Config{}, fmt.Errorf("invalid webhook source %q", w.ID)
		}
		seen[w.ID] = true
		c.Sources = append(c.Sources, s)
	}

	for _, p := range f.Polls {
		c.Targets = append(c.Targets, poll.Target{ID: p.ID, Connection: p.Connection, Repository: p.Repository, Branches: p.Branches})
	}
	if f.PollInterval != "" {
		d, err := time.ParseDuration(f.PollInterval)
		if err != nil || d < time.Second {
			return Config{}, errors.New("invalid poll interval")
		}
		c.Interval = d
	}

	return c, nil
}

// source checks w and names its secret source.
func (w Webhook) source() (api.Source, error) {
	var secret forgeauth.Credentials
	switch {
	case w.SecretFile != "" && w.SecretEnv == "":
		secret = forgeauth.FileToken(w.SecretFile)
	case w.SecretEnv != "" && w.SecretFile == "":
		secret = forgeauth.EnvToken(w.SecretEnv)
	default:
		return api.Source{}, errors.New("choose one secret source")
	}

	forge := forgeauth.Kind(w.Forge)
	if forge != forgeauth.GitHub && forge != forgeauth.Forgejo {
		return api.Source{}, errors.New("unsupported forge")
	}

	return api.Source{
		Source: webhook.Source{ID: w.ID, Forge: forge, Connection: w.Connection, Repositories: w.Repositories},
		Secret: secret,
	}, nil
}
