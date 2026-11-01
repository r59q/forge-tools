// Command forge-repositories-service serves repositories, branches and tags
// from every configured forge source over REST and gRPC.
package main

import (
	"context"
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

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-repositories/service/api"
	pb "github.com/r59q/forge-tools/forge-repositories/service/repositoriespb"
	"google.golang.org/grpc"
)

type settings struct {
	grpcAddress, httpAddress, configPath string
	tokenFile, tokenEnv                  string
	timeout                              time.Duration
}

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	var s settings
	env := func(key, fallback string) string {
		if value := getenv("FORGE_REPOSITORIES_" + key); value != "" {
			return value
		}
		return fallback
	}
	fs := flag.NewFlagSet("forge-repositories-service", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&s.grpcAddress, "grpc-listen", env("GRPC_LISTEN", "127.0.0.1:9092"), "gRPC listen address")
	fs.StringVar(&s.httpAddress, "http-listen", env("HTTP_LISTEN", "127.0.0.1:8082"), "REST listen address")
	fs.StringVar(&s.configPath, "config", env("CONFIG", ""), "JSON file listing the sources (required)")
	fs.StringVar(&s.tokenFile, "token-file", env("TOKEN_FILE", ""), "file containing the service bearer token")
	fs.StringVar(&s.tokenEnv, "token-env", env("TOKEN_ENV", ""), "service token variable name (default FORGE_REPOSITORIES_TOKEN)")
	var timeout string
	fs.StringVar(&timeout, "request-timeout", env("REQUEST_TIMEOUT", "30s"), "maximum request duration")
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
	if fs.NArg() != 0 || s.grpcAddress == "" || s.httpAddress == "" || s.timeout <= 0 {
		return settings{}, errors.New("invalid startup settings")
	}
	if s.configPath == "" {
		return settings{}, errors.New("configuration file required: set --config or FORGE_REPOSITORIES_CONFIG")
	}
	if s.tokenFile != "" && s.tokenEnv != "" {
		return settings{}, errors.New("choose one service token source")
	}
	if s.tokenFile == "" && s.tokenEnv == "" {
		s.tokenEnv = "FORGE_REPOSITORIES_TOKEN"
	}
	return s, nil
}

// serviceToken checks that the service token is set and usable as a bearer
// token, without ever including its value in an error.
func (s settings) serviceToken(ctx context.Context) (forgeauth.Credentials, error) {
	var creds forgeauth.Credentials = forgeauth.EnvToken(s.tokenEnv)
	if s.tokenFile != "" {
		creds = forgeauth.FileToken(s.tokenFile)
	}
	token, err := creds.Token(ctx)
	if err != nil {
		return nil, errors.New("service token required: configure token file or environment source")
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	if parsed, ok := forgeauth.AuthorizationToken(h, "Bearer"); !ok || parsed != token {
		return nil, errors.New("service token must be a nonempty bearer token")
	}
	return creds, nil
}

func run(ctx context.Context, s settings, ready func(grpcAddr, httpAddr net.Addr)) error {
	token, err := s.serviceToken(ctx)
	if err != nil {
		return err
	}
	cfg, err := readConfig(s.configPath)
	if err != nil {
		return err
	}
	client, err := cfg.client(ctx)
	if err != nil {
		return err
	}
	service, err := api.New(client, token, s.timeout)
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
	pb.RegisterRepositoriesServer(grpcServer, service)
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: s.timeout + 5*time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	failures := make(chan error, 2)
	go func() { failures <- grpcServer.Serve(grpcListener) }()
	go func() { failures <- httpServer.Serve(httpListener) }()
	if ready != nil {
		ready(grpcListener.Addr(), httpListener.Addr())
	}
	select {
	case <-ctx.Done():
		err = nil
	case <-failures:
		err = errors.New("listener stopped unexpectedly")
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
		err = run(ctx, s, nil)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
