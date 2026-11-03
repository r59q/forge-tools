// Package servicekit holds the startup, authentication, REST and shutdown
// code shared by forge-tools services.
package servicekit

import (
	"errors"
	"flag"
	"io"
	"time"
)

// Options describe a service's shared flags and their defaults.
type Options struct {
	// Name is the command name, such as forge-issues-service.
	Name string
	// Prefix starts every environment variable, such as FORGE_ISSUES.
	Prefix string
	// GRPCListen and HTTPListen are the default listen addresses.
	GRPCListen, HTTPListen string
	// Timeout is the default request timeout, such as 30s.
	Timeout string
	// ConnectorRequired makes the connector URL mandatory.
	ConnectorRequired bool
}

// Settings are the startup settings every service shares.
type Settings struct {
	GRPCAddress, HTTPAddress              string
	TokenFile, TokenEnv                   string
	ConnectorURL                          string
	ConnectorTokenFile, ConnectorTokenEnv string
	Timeout                               time.Duration
}

// Env returns the value of the PREFIX_KEY variable, or fallback when it is
// unset or empty.
type Env func(key, fallback string) string

// Parse reads the shared flags, and any that extra registers, from args and
// the environment. Flags win over variables. It prints help and returns
// flag.ErrHelp for --help. Errors never contain flag values.
func Parse(args []string, getenv func(string) string, help io.Writer, o Options, extra func(*flag.FlagSet, Env)) (Settings, error) {
	env := func(key, fallback string) string {
		if value := getenv(o.Prefix + "_" + key); value != "" {
			return value
		}

		return fallback
	}

	var s Settings
	var timeout string
	fs := flag.NewFlagSet(o.Name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	register(fs, env, o, &s, &timeout)
	if extra != nil {
		extra(fs, env)
	}

	if err := fs.Parse(args); err != nil {
		return Settings{}, parseError(err, fs, help)
	}

	if fs.NArg() != 0 {
		return Settings{}, errors.New("invalid startup settings")
	}

	return s, s.finish(o, timeout)
}

// register adds the shared flags to fs.
func register(fs *flag.FlagSet, env Env, o Options, s *Settings, timeout *string) {
	connectorHelp := "optional forge-connector proxy URL"
	if o.ConnectorRequired {
		connectorHelp = "forge-connector proxy URL (required)"
	}

	fs.StringVar(&s.GRPCAddress, "grpc-listen", env("GRPC_LISTEN", o.GRPCListen), "gRPC listen address")
	fs.StringVar(&s.HTTPAddress, "http-listen", env("HTTP_LISTEN", o.HTTPListen), "REST listen address")
	fs.StringVar(&s.TokenFile, "token-file", env("TOKEN_FILE", ""), "file containing the service bearer token")
	fs.StringVar(&s.TokenEnv, "token-env", env("TOKEN_ENV", ""), "service token variable name (default "+o.Prefix+"_TOKEN)")
	fs.StringVar(&s.ConnectorURL, "connector-url", env("CONNECTOR_URL", ""), connectorHelp)
	fs.StringVar(
		&s.ConnectorTokenFile,
		"connector-token-file",
		env("CONNECTOR_TOKEN_FILE", ""),
		"file containing the connector client token",
	)
	fs.StringVar(
		&s.ConnectorTokenEnv,
		"connector-token-env",
		env("CONNECTOR_TOKEN_ENV", ""),
		"connector client token variable name (default "+o.Prefix+"_CONNECTOR_TOKEN)",
	)
	fs.StringVar(timeout, "request-timeout", env("REQUEST_TIMEOUT", o.Timeout), "maximum request duration")
}

// parseError prints help for --help and hides flag values otherwise.
func parseError(err error, fs *flag.FlagSet, help io.Writer) error {
	if !errors.Is(err, flag.ErrHelp) {
		return errors.New("invalid startup flags; use --help")
	}

	fs.SetOutput(help)
	fs.PrintDefaults()

	return flag.ErrHelp
}

// finish checks s and fills in the default token variables.
func (s *Settings) finish(o Options, timeout string) error {
	var err error
	s.Timeout, err = time.ParseDuration(timeout)
	if err != nil {
		return errors.New("invalid request timeout")
	}

	if s.GRPCAddress == "" || s.HTTPAddress == "" || s.Timeout <= 0 {
		return errors.New("invalid startup settings")
	}

	if o.ConnectorRequired && s.ConnectorURL == "" {
		return errors.New("connector URL required: set --connector-url or " + o.Prefix + "_CONNECTOR_URL")
	}

	if s.TokenFile != "" && s.TokenEnv != "" {
		return errors.New("choose one service token source")
	}

	if s.TokenFile == "" && s.TokenEnv == "" {
		s.TokenEnv = o.Prefix + "_TOKEN"
	}

	if s.ConnectorTokenFile != "" && s.ConnectorTokenEnv != "" {
		return errors.New("choose one connector token source")
	}

	if s.ConnectorTokenFile == "" && s.ConnectorTokenEnv == "" {
		s.ConnectorTokenEnv = o.Prefix + "_CONNECTOR_TOKEN"
	}

	return nil
}
