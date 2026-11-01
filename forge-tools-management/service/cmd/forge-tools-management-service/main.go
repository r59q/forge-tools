// Command forge-tools-management-service serves management over HTTP and
// gRPC, optionally connected to forge-connector, forge-repositories and
// forge-issues.
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
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	"github.com/r59q/forge-tools/forge-tools-management/service/api"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc"
)

type settings struct {
	grpcAddress, httpAddress                                         string
	tokenFile, tokenEnv                                              string
	timeout                                                          time.Duration
	repositoriesAddress, repositoriesTokenFile, repositoriesTokenEnv string
	repositoriesInsecure                                             bool
	issuesAddress, issuesTokenFile, issuesTokenEnv                   string
	issuesInsecure                                                   bool
	connectorAddress, connectorTokenFile, connectorTokenEnv          string
	connectorInsecure                                                bool
	clients                                                          []serviceClient
}

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	var s settings
	env := func(key, fallback string) string {
		if value := getenv("FORGE_MANAGEMENT_" + key); value != "" {
			return value
		}
		return fallback
	}
	fs := flag.NewFlagSet("forge-tools-management-service", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&s.grpcAddress, "grpc-listen", env("GRPC_LISTEN", "127.0.0.1:9093"), "gRPC listen address")
	fs.StringVar(&s.httpAddress, "http-listen", env("HTTP_LISTEN", "127.0.0.1:8083"), "REST listen address")
	fs.StringVar(&s.tokenFile, "token-file", env("TOKEN_FILE", ""), "file containing the service bearer token")
	fs.StringVar(&s.tokenEnv, "token-env", env("TOKEN_ENV", ""), "service token variable name (default FORGE_MANAGEMENT_TOKEN)")
	fs.StringVar(&s.repositoriesAddress, "repositories-address", env("REPOSITORIES_ADDRESS", ""), "optional forge-repositories gRPC host:port")
	fs.StringVar(&s.repositoriesTokenFile, "repositories-token-file", env("REPOSITORIES_TOKEN_FILE", ""), "repository service token file")
	fs.StringVar(&s.repositoriesTokenEnv, "repositories-token-env", env("REPOSITORIES_TOKEN_ENV", ""), "repository service token variable name")
	var insecure string
	fs.StringVar(&insecure, "repositories-insecure", env("REPOSITORIES_INSECURE", "false"), "use plaintext to a trusted local repository service: true or false")
	fs.StringVar(&s.issuesAddress, "issues-address", env("ISSUES_ADDRESS", ""), "optional forge-issues gRPC host:port")
	fs.StringVar(&s.issuesTokenFile, "issues-token-file", env("ISSUES_TOKEN_FILE", ""), "issue service token file")
	fs.StringVar(&s.issuesTokenEnv, "issues-token-env", env("ISSUES_TOKEN_ENV", ""), "issue service token variable name")
	var issuesInsecure string
	fs.StringVar(&issuesInsecure, "issues-insecure", env("ISSUES_INSECURE", "false"), "use plaintext to a trusted local issue service: true or false")
	fs.StringVar(&s.connectorAddress, "connector-address", env("CONNECTOR_ADDRESS", ""), "optional forge-connector broker gRPC host:port")
	fs.StringVar(&s.connectorTokenFile, "connector-admin-token-file", env("CONNECTOR_ADMIN_TOKEN_FILE", ""), "broker admin token file")
	fs.StringVar(&s.connectorTokenEnv, "connector-admin-token-env", env("CONNECTOR_ADMIN_TOKEN_ENV", ""), "broker admin token variable name")
	var connectorInsecure string
	fs.StringVar(&connectorInsecure, "connector-insecure", env("CONNECTOR_INSECURE", "false"), "use plaintext to a trusted local broker: true or false")
	proxy := serviceClient{name: "forge-connector-proxy", canIssueTokens: true}
	fs.StringVar(&proxy.file, "proxy-token-file", env("PROXY_TOKEN_FILE", ""), "proxy client token file, registered in the broker")
	fs.StringVar(&proxy.env, "proxy-token-env", env("PROXY_TOKEN_ENV", ""), "proxy client token variable name")
	repos := serviceClient{name: "forge-repositories"}
	fs.StringVar(&repos.file, "repositories-connector-token-file", env("REPOSITORIES_CONNECTOR_TOKEN_FILE", ""), "repositories service client token file, registered in the broker")
	fs.StringVar(&repos.env, "repositories-connector-token-env", env("REPOSITORIES_CONNECTOR_TOKEN_ENV", ""), "repositories service client token variable name")
	issuesClient := serviceClient{name: "forge-issues"}
	fs.StringVar(&issuesClient.file, "issues-connector-token-file", env("ISSUES_CONNECTOR_TOKEN_FILE", ""), "issues service client token file, registered in the broker")
	fs.StringVar(&issuesClient.env, "issues-connector-token-env", env("ISSUES_CONNECTOR_TOKEN_ENV", ""), "issues service client token variable name")
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
	if insecure != "true" && insecure != "false" {
		return settings{}, errors.New("invalid repositories-insecure setting")
	}
	if issuesInsecure != "true" && issuesInsecure != "false" {
		return settings{}, errors.New("invalid issues-insecure setting")
	}
	if connectorInsecure != "true" && connectorInsecure != "false" {
		return settings{}, errors.New("invalid connector-insecure setting")
	}
	s.repositoriesInsecure, s.issuesInsecure, s.connectorInsecure = insecure == "true", issuesInsecure == "true", connectorInsecure == "true"
	if s.tokenFile != "" && s.tokenEnv != "" {
		return settings{}, errors.New("choose one service token source")
	}
	if s.tokenFile == "" && s.tokenEnv == "" {
		s.tokenEnv = "FORGE_MANAGEMENT_TOKEN"
	}
	if s.repositoriesTokenFile != "" && s.repositoriesTokenEnv != "" {
		return settings{}, errors.New("choose one repository service token source")
	}
	if s.repositoriesAddress == "" && (s.repositoriesTokenFile != "" || s.repositoriesTokenEnv != "" || s.repositoriesInsecure) {
		return settings{}, errors.New("repository settings require repositories-address")
	}
	if s.repositoriesAddress != "" && s.repositoriesTokenFile == "" && s.repositoriesTokenEnv == "" {
		s.repositoriesTokenEnv = "FORGE_MANAGEMENT_REPOSITORIES_TOKEN"
	}
	if s.issuesTokenFile != "" && s.issuesTokenEnv != "" {
		return settings{}, errors.New("choose one issue service token source")
	}
	if s.issuesAddress == "" && (s.issuesTokenFile != "" || s.issuesTokenEnv != "" || s.issuesInsecure) {
		return settings{}, errors.New("issue settings require issues-address")
	}
	if s.issuesAddress != "" && s.issuesTokenFile == "" && s.issuesTokenEnv == "" {
		s.issuesTokenEnv = "FORGE_MANAGEMENT_ISSUES_TOKEN"
	}
	if s.connectorTokenFile != "" && s.connectorTokenEnv != "" {
		return settings{}, errors.New("choose one connector admin token source")
	}
	explicit := proxy.file != "" || proxy.env != "" || repos.file != "" || repos.env != "" || issuesClient.file != "" || issuesClient.env != ""
	if s.connectorAddress == "" && (s.connectorTokenFile != "" || s.connectorTokenEnv != "" || s.connectorInsecure || explicit) {
		return settings{}, errors.New("connector settings require connector-address")
	}
	if s.connectorAddress == "" {
		return s, nil
	}
	if s.connectorTokenFile == "" && s.connectorTokenEnv == "" {
		s.connectorTokenEnv = "FORGE_MANAGEMENT_CONNECTOR_ADMIN_TOKEN"
	}
	for _, c := range []struct {
		client     *serviceClient
		defaultEnv string
	}{{&proxy, "FORGE_MANAGEMENT_PROXY_TOKEN"}, {&repos, "FORGE_MANAGEMENT_REPOSITORIES_CONNECTOR_TOKEN"}, {&issuesClient, "FORGE_MANAGEMENT_ISSUES_CONNECTOR_TOKEN"}} {
		if c.client.file != "" && c.client.env != "" {
			return settings{}, fmt.Errorf("choose one %s client token source", c.client.name)
		}
		if c.client.file == "" && c.client.env == "" {
			c.client.env, c.client.optional = c.defaultEnv, true
		}
	}
	s.clients = []serviceClient{proxy, repos, issuesClient}
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
	reader, closeBackend, err := s.repositoryReader(ctx, token)
	if err != nil {
		return err
	}
	defer closeBackend()
	issueReader, closeIssues, err := s.issueReader(ctx, token)
	if err != nil {
		return err
	}
	defer closeIssues()
	config := management.Config{Repositories: reader, Issues: issueReader}
	connector, closeConnector, err := s.connector(ctx, token)
	if err != nil {
		return err
	}
	defer closeConnector()
	var clients []serviceClient
	if connector != nil {
		config.Connector = connector
		if clients, err = s.clientTokens(ctx, token, connector.token); err != nil {
			return err
		}
	}
	client := management.New(config)
	service, err := api.New(client, token, s.timeout)
	if err != nil {
		return errors.New("cannot initialize API")
	}
	handler, err := service.HTTP(ctx)
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
	pb.RegisterManagementServer(grpcServer, service)
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: s.timeout + 5*time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	failures := make(chan error, 2)
	go func() { failures <- grpcServer.Serve(grpcListener) }()
	go func() { failures <- httpServer.Serve(httpListener) }()
	// Register service clients while serving, so the UI is up even when the
	// broker is not yet.
	registerCtx, stopRegister := context.WithCancel(ctx)
	registered := make(chan struct{})
	go func() { register(registerCtx, client, clients, s.timeout); close(registered) }()
	defer func() { stopRegister(); <-registered }()
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
