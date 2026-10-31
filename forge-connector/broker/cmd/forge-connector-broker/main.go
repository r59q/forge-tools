// Command forge-connector-broker serves the single-owner connection broker.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/r59q/forge-tools/forge-connector/broker/api"
	"github.com/r59q/forge-tools/forge-connector/broker/sqlite"
	"github.com/r59q/forge-tools/forge-connector/library/broker"
	pb "github.com/r59q/forge-tools/forge-connector/library/brokerpb"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"google.golang.org/grpc"
)

type settings struct {
	grpcAddress, httpAddress, dbPath     string
	keyFile, keyEnv, adminFile, adminEnv string
	timeout                              time.Duration
}

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	var s settings
	env := func(key, fallback string) string {
		if value := getenv("FORGE_BROKER_" + key); value != "" {
			return value
		}
		return fallback
	}
	fs := flag.NewFlagSet("forge-connector-broker", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&s.grpcAddress, "grpc-listen", env("GRPC_LISTEN", "127.0.0.1:9090"), "gRPC listen address")
	fs.StringVar(&s.httpAddress, "http-listen", env("HTTP_LISTEN", "127.0.0.1:8080"), "REST listen address")
	fs.StringVar(&s.dbPath, "db", env("DB", "broker.db"), "SQLite file path (parent must exist)")
	fs.StringVar(&s.keyFile, "key-file", env("KEY_FILE", ""), "file containing the base64 AES-256 key")
	fs.StringVar(&s.keyEnv, "key-env", env("KEY_ENV", ""), "key variable name (default FORGE_BROKER_KEY)")
	fs.StringVar(&s.adminFile, "admin-token-file", env("ADMIN_TOKEN_FILE", ""), "file containing the admin bearer token")
	fs.StringVar(&s.adminEnv, "admin-token-env", env("ADMIN_TOKEN_ENV", ""), "admin variable name (default FORGE_BROKER_ADMIN_TOKEN)")
	var timeout string
	fs.StringVar(&timeout, "request-timeout", env("REQUEST_TIMEOUT", "30s"), "maximum operation duration")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(help)
			fs.PrintDefaults()
			return settings{}, flag.ErrHelp
		}
		return settings{}, errors.New("invalid startup flags; use --help")
	}
	var err error
	s.timeout, err = time.ParseDuration(timeout)
	if err != nil {
		return settings{}, errors.New("invalid request timeout")
	}
	if fs.NArg() != 0 || s.dbPath == "" || s.grpcAddress == "" || s.httpAddress == "" || s.timeout <= 0 {
		return settings{}, errors.New("invalid startup settings")
	}
	if s.keyFile != "" && s.keyEnv != "" {
		return settings{}, errors.New("choose one key source")
	}
	if s.adminFile != "" && s.adminEnv != "" {
		return settings{}, errors.New("choose one admin token source")
	}
	if s.keyFile == "" && s.keyEnv == "" {
		s.keyEnv = "FORGE_BROKER_KEY"
	}
	if s.adminFile == "" && s.adminEnv == "" {
		s.adminEnv = "FORGE_BROKER_ADMIN_TOKEN"
	}
	return s, nil
}

func source(file, env string) forgeauth.Credentials {
	if file != "" {
		return forgeauth.FileToken(file)
	}
	return forgeauth.EnvToken(env)
}
func (s settings) secrets(ctx context.Context) (*broker.AESGCMSealer, forgeauth.Credentials, error) {
	encoded, err := source(s.keyFile, s.keyEnv).Token(ctx)
	if err != nil {
		return nil, nil, errors.New("sealer key required: configure key file or environment source")
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	defer clear(key)
	if err != nil || len(key) != 32 {
		return nil, nil, errors.New("sealer key must be a base64-encoded 32-byte key")
	}
	sealer, err := broker.NewAESGCMSealer(key)
	if err != nil {
		return nil, nil, errors.New("cannot initialize sealer")
	}
	admin := source(s.adminFile, s.adminEnv)
	token, err := admin.Token(ctx)
	if err != nil {
		return nil, nil, errors.New("admin token required: configure token file or environment source")
	}
	// Check header safety without ever including its value in an error.
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	if parsed, ok := forgeauth.AuthorizationToken(h, "Bearer"); !ok || parsed != token {
		return nil, nil, errors.New("admin token must be a nonempty bearer token")
	}
	return sealer, admin, nil
}

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
	var registry forgeauth.Registry
	for _, provider := range []forgeauth.Provider{github.Provider{}, gitlab.Provider{}, forgejo.Provider{}} {
		if err := registry.Register(provider); err != nil {
			return errors.New("cannot register forge provider")
		}
	}
	manager, err := broker.NewManager(store, sealer, &registry)
	if err != nil {
		return errors.New("cannot initialize broker")
	}
	service, err := api.New(manager, admin, s.timeout)
	if err != nil {
		return errors.New("cannot initialize API")
	}
	handler, err := service.REST(ctx)
	if err != nil {
		return errors.New("cannot initialize REST API")
	}
	grpcListener, err := net.Listen("tcp", s.grpcAddress)
	if err != nil {
		return errors.New("cannot listen for gRPC")
	}
	defer grpcListener.Close()
	httpListener, err := net.Listen("tcp", s.httpAddress)
	if err != nil {
		return errors.New("cannot listen for REST")
	}
	defer httpListener.Close()
	grpcServer := grpc.NewServer(grpc.MaxRecvMsgSize(1 << 20))
	pb.RegisterBrokerServer(grpcServer, service)
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: s.timeout + 5*time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	failures := make(chan error, 2)
	go func() { failures <- grpcServer.Serve(grpcListener) }()
	go func() { failures <- httpServer.Serve(httpListener) }()
	select {
	case <-ctx.Done():
		err = nil
	case <-failures:
		err = errors.New("broker listener stopped unexpectedly")
	}
	// Drain in-flight requests for up to five seconds, then force shutdown.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stopped := make(chan struct{})
	go func() { grpcServer.GracefulStop(); close(stopped) }()
	_ = httpServer.Shutdown(shutdownCtx)
	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		grpcServer.Stop()
	}
	_ = httpServer.Close()
	return err
}

func main() {
	s, err := parse(os.Args[1:], os.Getenv, os.Stdout)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = run(ctx, s)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
