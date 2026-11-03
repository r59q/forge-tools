// Command forge-connector-broker serves the single-owner connection broker.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"github.com/r59q/forge-tools/forge-connector/broker/api"
	"github.com/r59q/forge-tools/forge-connector/broker/sqlite"
	"github.com/r59q/forge-tools/forge-connector/library/broker"
	pb "github.com/r59q/forge-tools/forge-connector/library/brokerpb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc"
)

// envPrefix starts every broker environment variable.
const envPrefix = "FORGE_BROKER_"

type settings struct {
	grpcAddress string
	httpAddress string
	dbPath      string
	keyFile     string
	keyEnv      string
	adminFile   string
	adminEnv    string
	timeout     time.Duration
}

// parse reads settings from args and FORGE_BROKER_* variables. Flags win.
// Errors never contain flag values.
func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	env := func(key, fallback string) string {
		if value := getenv(envPrefix + key); value != "" {
			return value
		}

		return fallback
	}

	var s settings
	var timeout string
	fs := flag.NewFlagSet("forge-connector-broker", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	s.register(fs, env, &timeout)

	if err := fs.Parse(args); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			return settings{}, errors.New("invalid startup flags; use --help")
		}

		fs.SetOutput(help)
		fs.PrintDefaults()

		return settings{}, flag.ErrHelp
	}

	if fs.NArg() != 0 {
		return settings{}, errors.New("invalid startup settings")
	}

	return s, s.finish(timeout)
}

// register adds the broker flags to fs.
func (s *settings) register(fs *flag.FlagSet, env func(key, fallback string) string, timeout *string) {
	fs.StringVar(&s.grpcAddress, "grpc-listen", env("GRPC_LISTEN", "127.0.0.1:9090"), "gRPC listen address")
	fs.StringVar(&s.httpAddress, "http-listen", env("HTTP_LISTEN", "127.0.0.1:8080"), "REST listen address")
	fs.StringVar(&s.dbPath, "db", env("DB", "broker.db"), "SQLite file path (parent must exist)")
	fs.StringVar(&s.keyFile, "key-file", env("KEY_FILE", ""), "file containing the base64 AES-256 key")
	fs.StringVar(&s.keyEnv, "key-env", env("KEY_ENV", ""), "key variable name (default "+envPrefix+"KEY)")
	fs.StringVar(&s.adminFile, "admin-token-file", env("ADMIN_TOKEN_FILE", ""), "file containing the admin bearer token")
	fs.StringVar(
		&s.adminEnv,
		"admin-token-env",
		env("ADMIN_TOKEN_ENV", ""),
		"admin variable name (default "+envPrefix+"ADMIN_TOKEN)",
	)
	fs.StringVar(timeout, "request-timeout", env("REQUEST_TIMEOUT", "30s"), "maximum operation duration")
}

// finish checks s and fills in the default secret variables.
func (s *settings) finish(timeout string) error {
	var err error
	s.timeout, err = time.ParseDuration(timeout)
	if err != nil {
		return errors.New("invalid request timeout")
	}

	if s.dbPath == "" || s.grpcAddress == "" || s.httpAddress == "" || s.timeout <= 0 {
		return errors.New("invalid startup settings")
	}

	if s.keyFile != "" && s.keyEnv != "" {
		return errors.New("choose one key source")
	}

	if s.adminFile != "" && s.adminEnv != "" {
		return errors.New("choose one admin token source")
	}

	if s.keyFile == "" && s.keyEnv == "" {
		s.keyEnv = envPrefix + "KEY"
	}

	if s.adminFile == "" && s.adminEnv == "" {
		s.adminEnv = envPrefix + "ADMIN_TOKEN"
	}

	return nil
}

// readSecret reads a secret from file, if set, or else the env variable.
func readSecret(ctx context.Context, file, env string) (string, error) {
	if file != "" {
		return forgeauth.FileToken(file).Token(ctx)
	}

	return forgeauth.EnvToken(env).Token(ctx)
}

// secrets returns the sealer and the admin token. Errors never contain
// either value.
func (s settings) secrets(ctx context.Context) (*broker.AESGCMSealer, servicekit.Token, error) {
	encoded, err := readSecret(ctx, s.keyFile, s.keyEnv)
	if err != nil {
		return nil, servicekit.Token{}, errors.New("sealer key required: configure key file or environment source")
	}

	key, err := base64.StdEncoding.DecodeString(encoded)
	defer clear(key)
	if err != nil || len(key) != 32 {
		return nil, servicekit.Token{}, errors.New("sealer key must be a base64-encoded 32-byte key")
	}

	sealer, err := broker.NewAESGCMSealer(key)
	if err != nil {
		return nil, servicekit.Token{}, errors.New("cannot initialize sealer")
	}

	admin, err := servicekit.BearerToken(ctx, "admin", s.adminFile, s.adminEnv)
	if err != nil {
		return nil, servicekit.Token{}, err
	}

	return sealer, admin, nil
}

// run serves the broker until ctx ends.
func run(ctx context.Context, s settings) error {
	sealer, admin, err := s.secrets(ctx)
	if err != nil {
		return err
	}

	store, err := sqlite.Open(ctx, s.dbPath)
	if err != nil {
		return errors.New("cannot open broker database")
	}

	defer store.Close()
	service, err := newService(store, sealer, admin, s.timeout)
	if err != nil {
		return err
	}

	handler, err := service.REST(ctx)
	if err != nil {
		return errors.New("cannot initialize REST API")
	}

	return servicekit.Server{
		GRPCAddress:  s.grpcAddress,
		HTTPAddress:  s.httpAddress,
		Register:     func(g *grpc.Server) { pb.RegisterBrokerServiceServer(g, service) },
		Handler:      handler,
		WriteTimeout: s.timeout + 5*time.Second,
	}.Run(ctx)
}

// newService builds the broker API over store with the supported forges.
func newService(store broker.Store, sealer broker.Sealer, admin forgeauth.Credentials, timeout time.Duration) (*api.Server, error) {
	var registry forgeauth.Registry
	for _, provider := range []forgeauth.Provider{github.Provider{}, gitlab.Provider{}, forgejo.Provider{}} {
		if err := registry.Register(provider); err != nil {
			return nil, errors.New("cannot register forge provider")
		}
	}

	manager, err := broker.NewManager(store, sealer, &registry)
	if err != nil {
		return nil, errors.New("cannot initialize broker")
	}

	service, err := api.New(manager, admin, timeout)
	if err != nil {
		return nil, errors.New("cannot initialize API")
	}

	return service, nil
}

func main() {
	servicekit.Main(func(ctx context.Context) error {
		s, err := parse(os.Args[1:], os.Getenv, os.Stdout)
		if err != nil {
			return err
		}

		return run(ctx, s)
	})
}
