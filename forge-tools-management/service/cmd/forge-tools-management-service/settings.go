package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"
)

// secureDefault is the default insecure setting: TLS to every backend.
const secureDefault = "false"

type environment func(string, string) string

type transportSettings struct {
	repositories string
	issues       string
	actions      string
	reactor      string
	connector    string
}

func (s *settings) backendFlags(fs *flag.FlagSet, env environment) *transportSettings {
	values := &transportSettings{}

	fs.StringVar(&s.repositoriesAddress, "repositories-address", env("REPOSITORIES_ADDRESS", ""), "optional forge-repositories gRPC host:port")
	fs.StringVar(&s.repositoriesTokenFile, "repositories-token-file", env("REPOSITORIES_TOKEN_FILE", ""), "repository service token file")
	fs.StringVar(
		&s.repositoriesTokenEnv,
		"repositories-token-env",
		env("REPOSITORIES_TOKEN_ENV", ""),
		"repository service token variable name",
	)
	fs.StringVar(
		&values.repositories,
		"repositories-insecure",
		env("REPOSITORIES_INSECURE", secureDefault),
		"use plaintext to a trusted local repository service: true or false",
	)

	fs.StringVar(&s.issuesAddress, "issues-address", env("ISSUES_ADDRESS", ""), "optional forge-issues gRPC host:port")
	fs.StringVar(&s.issuesTokenFile, "issues-token-file", env("ISSUES_TOKEN_FILE", ""), "issue service token file")
	fs.StringVar(&s.issuesTokenEnv, "issues-token-env", env("ISSUES_TOKEN_ENV", ""), "issue service token variable name")
	fs.StringVar(
		&values.issues,
		"issues-insecure",
		env("ISSUES_INSECURE", secureDefault),
		"use plaintext to a trusted local issue service: true or false",
	)

	fs.StringVar(&s.actionsAddress, "actions-address", env("ACTIONS_ADDRESS", ""), "optional forge-actions gRPC host:port")
	fs.StringVar(&s.actionsTokenFile, "actions-token-file", env("ACTIONS_TOKEN_FILE", ""), "actions service token file")
	fs.StringVar(&s.actionsTokenEnv, "actions-token-env", env("ACTIONS_TOKEN_ENV", ""), "actions service token variable name")
	fs.StringVar(
		&values.actions,
		"actions-insecure",
		env("ACTIONS_INSECURE", secureDefault),
		"use plaintext to a trusted local actions service: true or false",
	)

	fs.StringVar(&s.reactorAddress, "reactor-address", env("REACTOR_ADDRESS", ""), "optional forge-reactor gRPC host:port")
	fs.StringVar(&s.reactorTokenFile, "reactor-token-file", env("REACTOR_TOKEN_FILE", ""), "reactor service token file")
	fs.StringVar(&s.reactorTokenEnv, "reactor-token-env", env("REACTOR_TOKEN_ENV", ""), "reactor service token variable name")
	fs.StringVar(
		&values.reactor,
		"reactor-insecure",
		env("REACTOR_INSECURE", secureDefault),
		"use plaintext to a trusted local reactor service: true or false",
	)

	fs.StringVar(&s.connectorAddress, "connector-address", env("CONNECTOR_ADDRESS", ""), "optional forge-connector broker gRPC host:port")
	fs.StringVar(&s.connectorTokenFile, "connector-admin-token-file", env("CONNECTOR_ADMIN_TOKEN_FILE", ""), "broker admin token file")
	fs.StringVar(&s.connectorTokenEnv, "connector-admin-token-env", env("CONNECTOR_ADMIN_TOKEN_ENV", ""), "broker admin token variable name")
	fs.StringVar(
		&values.connector,
		"connector-insecure",
		env("CONNECTOR_INSECURE", secureDefault),
		"use plaintext to a trusted local broker: true or false",
	)

	return values
}

func clientFlags(fs *flag.FlagSet, env environment) []serviceClient {
	definitions := []struct {
		name           string
		flagPrefix     string
		envPrefix      string
		canIssueTokens bool
	}{
		{"forge-connector-proxy", "proxy", "PROXY", true},
		{"forge-repositories", "repositories-connector", "REPOSITORIES_CONNECTOR", false},
		{"forge-issues", "issues-connector", "ISSUES_CONNECTOR", false},
		{"forge-checkouts", "checkouts-connector", "CHECKOUTS_CONNECTOR", false},
		{"forge-actions", "actions-connector", "ACTIONS_CONNECTOR", false},
		{"forge-reactor", "reactor-connector", "REACTOR_CONNECTOR", false},
		{"forge-code", "code-connector", "CODE_CONNECTOR", false},
		{"forge-changes", "changes-connector", "CHANGES_CONNECTOR", false},
		{"forge-review", "review-connector", "REVIEW_CONNECTOR", false},
	}
	clients := make([]serviceClient, len(definitions))
	for i, definition := range definitions {
		client := &clients[i]
		client.name = definition.name
		client.canIssueTokens = definition.canIssueTokens
		client.defaultEnv = "FORGE_MANAGEMENT_" + definition.envPrefix + "_TOKEN"
		fs.StringVar(
			&client.file,
			definition.flagPrefix+"-token-file",
			env(definition.envPrefix+"_TOKEN_FILE", ""),
			definition.name+" client token file, registered in the broker",
		)
		fs.StringVar(
			&client.env,
			definition.flagPrefix+"-token-env",
			env(definition.envPrefix+"_TOKEN_ENV", ""),
			definition.name+" client token variable name",
		)
	}

	return clients
}

func (s *settings) configureTransports(values *transportSettings) error {
	var err error
	if s.repositoriesInsecure, err = insecureSetting("repositories", values.repositories); err != nil {
		return err
	}
	if s.issuesInsecure, err = insecureSetting("issues", values.issues); err != nil {
		return err
	}
	if s.actionsInsecure, err = insecureSetting("actions", values.actions); err != nil {
		return err
	}
	if s.reactorInsecure, err = insecureSetting("reactor", values.reactor); err != nil {
		return err
	}
	if s.connectorInsecure, err = insecureSetting("connector", values.connector); err != nil {
		return err
	}

	return nil
}

// insecureSetting parses the <name>-insecure setting, true or false.
func insecureSetting(name, value string) (bool, error) {
	if value != "true" && value != secureDefault {
		return false, fmt.Errorf("invalid %s-insecure setting", name)
	}

	return value == "true", nil
}

func (s *settings) configureBackends() error {
	if s.tokenFile != "" && s.tokenEnv != "" {
		return errors.New("choose one service token source")
	}
	if s.tokenFile == "" && s.tokenEnv == "" {
		s.tokenEnv = "FORGE_MANAGEMENT_TOKEN"
	}

	backends := []backendSetting{
		{"repository service", "repositories", s.repositoriesAddress, &s.repositoriesTokenFile, &s.repositoriesTokenEnv, s.repositoriesInsecure},
		{"issue service", "issues", s.issuesAddress, &s.issuesTokenFile, &s.issuesTokenEnv, s.issuesInsecure},
		{"actions service", "actions", s.actionsAddress, &s.actionsTokenFile, &s.actionsTokenEnv, s.actionsInsecure},
		{"reactor service", "reactor", s.reactorAddress, &s.reactorTokenFile, &s.reactorTokenEnv, s.reactorInsecure},
	}
	for _, b := range backends {
		if err := b.check(); err != nil {
			return err
		}
	}

	return nil
}

// backendSetting is one backend service's address, token and transport.
type backendSetting struct {
	name, flag      string
	address         string
	tokenFile       *string
	tokenEnv        *string
	insecureEnabled bool
}

// check rejects conflicting or orphaned settings and defaults the token
// variable to FORGE_MANAGEMENT_<FLAG>_TOKEN.
func (b backendSetting) check() error {
	hasToken := *b.tokenFile != "" || *b.tokenEnv != ""
	switch {
	case *b.tokenFile != "" && *b.tokenEnv != "":
		return fmt.Errorf("choose one %s token source", b.name)
	case b.address == "" && (hasToken || b.insecureEnabled):
		return fmt.Errorf("%s settings require %s-address", b.flag, b.flag)
	case b.address != "" && !hasToken:
		*b.tokenEnv = "FORGE_MANAGEMENT_" + strings.ToUpper(b.flag) + "_TOKEN"
	}

	return nil
}

func (s *settings) configureClients(clients []serviceClient) error {
	if s.connectorTokenFile != "" && s.connectorTokenEnv != "" {
		return errors.New("choose one connector admin token source")
	}

	explicit := false
	for _, c := range clients {
		explicit = explicit || c.file != "" || c.env != ""
	}

	if s.connectorAddress == "" && (s.connectorTokenFile != "" || s.connectorTokenEnv != "" || s.connectorInsecure || explicit) {
		return errors.New("connector settings require connector-address")
	}
	if s.connectorAddress == "" {
		return nil
	}
	if s.connectorTokenFile == "" && s.connectorTokenEnv == "" {
		s.connectorTokenEnv = "FORGE_MANAGEMENT_CONNECTOR_ADMIN_TOKEN"
	}

	for i := range clients {
		c := &clients[i]
		if c.file != "" && c.env != "" {
			return fmt.Errorf("choose one %s client token source", c.name)
		}
		if c.file == "" && c.env == "" {
			c.env, c.optional = c.defaultEnv, true
		}
	}

	s.clients = clients

	return nil
}
