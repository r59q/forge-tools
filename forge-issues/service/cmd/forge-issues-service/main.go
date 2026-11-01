// Command forge-issues-service serves issues and comments from every forge
// connected through forge-connector, over REST and gRPC.
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

	"github.com/r59q/forge-tools/forge-connector/library/connector"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-issues/library/issues"
	"github.com/r59q/forge-tools/forge-issues/service/api"
	pb "github.com/r59q/forge-tools/forge-issues/service/issuespb"
	"google.golang.org/grpc"
)

type settings struct {
	grpcAddress, httpAddress              string
	tokenFile, tokenEnv                   string
	connectorURL                          string
	connectorTokenFile, connectorTokenEnv string
	timeout                               time.Duration
}

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	var s settings
	env := func(key, fallback string) string {
		if value := getenv("FORGE_ISSUES_" + key); value != "" {
			return value
		}
		return fallback
	}
	fs := flag.NewFlagSet("forge-issues-service", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&s.grpcAddress, "grpc-listen", env("GRPC_LISTEN", "127.0.0.1:9094"), "gRPC listen address")
	fs.StringVar(&s.httpAddress, "http-listen", env("HTTP_LISTEN", "127.0.0.1:8084"), "REST listen address")
	fs.StringVar(&s.tokenFile, "token-file", env("TOKEN_FILE", ""), "file containing the service bearer token")
	fs.StringVar(&s.tokenEnv, "token-env", env("TOKEN_ENV", ""), "service token variable name (default FORGE_ISSUES_TOKEN)")
	fs.StringVar(&s.connectorURL, "connector-url", env("CONNECTOR_URL", ""), "forge-connector proxy URL (required)")
	fs.StringVar(&s.connectorTokenFile, "connector-token-file", env("CONNECTOR_TOKEN_FILE", ""), "file containing the connector client token")
	fs.StringVar(&s.connectorTokenEnv, "connector-token-env", env("CONNECTOR_TOKEN_ENV", ""), "connector client token variable name (default FORGE_ISSUES_CONNECTOR_TOKEN)")
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
	if s.connectorURL == "" {
		return settings{}, errors.New("connector URL required: set --connector-url or FORGE_ISSUES_CONNECTOR_URL")
	}
	if s.tokenFile != "" && s.tokenEnv != "" {
		return settings{}, errors.New("choose one service token source")
	}
	if s.tokenFile == "" && s.tokenEnv == "" {
		s.tokenEnv = "FORGE_ISSUES_TOKEN"
	}
	if s.connectorTokenFile != "" && s.connectorTokenEnv != "" {
		return settings{}, errors.New("choose one connector token source")
	}
	if s.connectorTokenFile == "" && s.connectorTokenEnv == "" {
		s.connectorTokenEnv = "FORGE_ISSUES_CONNECTOR_TOKEN"
	}
	return s, nil
}

// bearerToken checks that a token source is set and usable as a bearer
// token, without ever including its value in an error. Tokens are reread
// on each use, so rotation needs no restart.
func bearerToken(ctx context.Context, name, file, env string) (forgeauth.Credentials, error) {
	var creds forgeauth.Credentials = forgeauth.EnvToken(env)
	if file != "" {
		creds = forgeauth.FileToken(file)
	}
	token, err := creds.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s token required: configure token file or environment source", name)
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	if parsed, ok := forgeauth.AuthorizationToken(h, "Bearer"); !ok || parsed != token {
		return nil, fmt.Errorf("%s token must be a nonempty bearer token", name)
	}
	return creds, nil
}

// sources asks the connector for its connections on every request and reads
// each as a source named by its connection ID. The connector supplies
// authenticated sessions, so this service never handles forge credentials.
func sources(conn *connector.Client) api.Sources {
	return func(ctx context.Context) (*issues.Client, error) {
		conns, err := conn.Connections(ctx)
		if err != nil {
			return nil, err
		}
		sources := make([]issues.Source, len(conns))
		for i, c := range conns {
			sources[i] = issues.Source{ID: string(c.ID), Session: c.Session}
		}
		return issues.New(sources...)
	}
}

func run(ctx context.Context, s settings, ready func(grpcAddr, httpAddr net.Addr)) error {
	token, err := bearerToken(ctx, "service", s.tokenFile, s.tokenEnv)
	if err != nil {
		return err
	}
	connectorToken, err := bearerToken(ctx, "connector", s.connectorTokenFile, s.connectorTokenEnv)
	if err != nil {
		return err
	}
	conn, err := connector.New(connector.Config{URL: s.connectorURL, Credentials: connectorToken})
	if err != nil {
		return errors.New("connector URL must be an http or https URL")
	}
	service, err := api.New(sources(conn), token, s.timeout)
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
	pb.RegisterIssuesServer(grpcServer, service)
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
