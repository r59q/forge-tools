// Command forge-connector-proxy serves the authenticating forge API proxy.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	pb "github.com/r59q/forge-tools/forge-connector/library/brokerpb"
	"github.com/r59q/forge-tools/forge-connector/library/proxy"
	"github.com/r59q/forge-tools/forge-connector/proxy/service"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type settings struct {
	httpAddress             string
	grpcAddress             string
	brokerAddress           string
	tokenFile, tokenEnv     string
	brokerTLS, allowGitPush bool
	brokerCA, publicURL     string
	tokenTTL                time.Duration
	verifyTTL               time.Duration
	brokerTimeout           time.Duration
	maxGitPushBytes         int64
	downloadHosts           string
}

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	var s settings
	env := func(key, fallback string) string {
		if value := getenv("FORGE_PROXY_" + key); value != "" {
			return value
		}

		return fallback
	}
	fs := flag.NewFlagSet("forge-connector-proxy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&s.httpAddress, "http-listen", env("HTTP_LISTEN", "127.0.0.1:8081"), "proxy and /healthz listen address")
	fs.StringVar(&s.grpcAddress, "grpc-listen", env("GRPC_LISTEN", "127.0.0.1:9091"), "gRPC health listen address")
	fs.StringVar(&s.brokerAddress, "broker-addr", env("BROKER_ADDR", "127.0.0.1:9090"), "broker gRPC address")
	fs.StringVar(&s.tokenFile, "token-file", env("TOKEN_FILE", ""), "file containing the proxy's broker client token")
	fs.StringVar(&s.tokenEnv, "token-env", env("TOKEN_ENV", ""), "token variable name (default FORGE_PROXY_TOKEN)")
	fs.StringVar(&s.publicURL, "public-url", env("PUBLIC_URL", ""), "public origin behind a TLS-terminating proxy")
	fs.StringVar(&s.brokerCA, "broker-ca-file", env("BROKER_CA_FILE", ""), "PEM CA file for the broker (implies --broker-tls)")
	brokerTLS := env("BROKER_TLS", "false")
	fs.StringVar(&brokerTLS, "broker-tls", brokerTLS, "use TLS to reach the broker (true or false)")
	allowGitPush := env("ALLOW_GIT_PUSH", "false")
	fs.StringVar(&allowGitPush, "allow-git-push", allowGitPush, "serve git-receive-pack on the Git route (true or false)")
	maxGitPush := env("MAX_GIT_PUSH_BYTES", strconv.FormatInt(proxy.DefaultMaxGitPushBytes, 10))
	fs.StringVar(&maxGitPush, "max-git-push-bytes", maxGitPush, "largest Git push body in bytes")
	fs.StringVar(
		&s.downloadHosts,
		"download-hosts",
		env("DOWNLOAD_HOSTS", ""),
		"comma-separated trusted HTTPS download hosts; *. allows subdomains",
	)
	var tokenTTL, verifyTTL, brokerTimeout string
	fs.StringVar(&tokenTTL, "token-ttl", env("TOKEN_TTL", service.DefaultTokenTTL.String()), "how long forge tokens are cached")
	fs.StringVar(&verifyTTL, "verify-ttl", env("VERIFY_TTL", service.DefaultVerifyTTL.String()), "how long a client approval is cached")
	fs.StringVar(
		&brokerTimeout,
		"broker-timeout",
		env("BROKER_TIMEOUT", service.DefaultBrokerTimeout.String()),
		"deadline for each broker call",
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(help)
			fs.PrintDefaults()

			return settings{}, flag.ErrHelp
		}

		return settings{}, errors.New("invalid startup flags; use --help")
	}

	var err error
	if s.brokerTLS, err = strconv.ParseBool(brokerTLS); err != nil {
		return settings{}, errors.New("invalid broker TLS setting")
	}

	s.brokerTLS = s.brokerTLS || s.brokerCA != ""
	if s.allowGitPush, err = strconv.ParseBool(allowGitPush); err != nil {
		return settings{}, errors.New("invalid allow-git-push setting")
	}
	if s.maxGitPushBytes, err = strconv.ParseInt(maxGitPush, 10, 64); err != nil || s.maxGitPushBytes <= 0 {
		return settings{}, errors.New("invalid max-git-push-bytes")
	}

	if err = s.parseDurations(tokenTTL, verifyTTL, brokerTimeout); err != nil {
		return settings{}, err
	}

	if fs.NArg() != 0 || s.httpAddress == "" || s.grpcAddress == "" || s.brokerAddress == "" {
		return settings{}, errors.New("invalid startup settings")
	}
	if s.tokenFile != "" && s.tokenEnv != "" {
		return settings{}, errors.New("choose one token source")
	}
	if s.tokenFile == "" && s.tokenEnv == "" {
		s.tokenEnv = "FORGE_PROXY_TOKEN"
	}

	return s, nil
}

// parseDurations sets the cache and timeout durations, which must not be
// negative.
func (s *settings) parseDurations(tokenTTL, verifyTTL, brokerTimeout string) error {
	for _, d := range []struct {
		name, value string
		dst         *time.Duration
	}{
		{"token-ttl", tokenTTL, &s.tokenTTL},
		{"verify-ttl", verifyTTL, &s.verifyTTL},
		{"broker-timeout", brokerTimeout, &s.brokerTimeout},
	} {
		var err error
		if *d.dst, err = time.ParseDuration(d.value); err != nil || *d.dst < 0 {
			return fmt.Errorf("invalid %s", d.name)
		}
	}

	return nil
}

// dial connects to the broker, over TLS when configured.
func (s settings) dial() (*grpc.ClientConn, error) {
	transport := insecure.NewCredentials()
	if s.brokerTLS {
		config, err := s.brokerTLSConfig()
		if err != nil {
			return nil, err
		}

		transport = credentials.NewTLS(config)
	}

	conn, err := grpc.NewClient(s.brokerAddress, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, errors.New("invalid broker address")
	}

	return conn, nil
}

// brokerTLSConfig trusts the system roots, or only the configured CA file.
func (s settings) brokerTLSConfig() (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if s.brokerCA == "" {
		return config, nil
	}

	pem, err := os.ReadFile(s.brokerCA)
	if err != nil {
		return nil, errors.New("cannot read broker CA file")
	}

	config.RootCAs = x509.NewCertPool()
	if !config.RootCAs.AppendCertsFromPEM(pem) {
		return nil, errors.New("broker CA file has no certificates")
	}

	return config, nil
}

// run serves the proxy until ctx ends.
func run(ctx context.Context, s settings, logger *slog.Logger) error {
	// Fail at startup, not on the first request, if the token is unusable.
	creds, err := servicekit.BearerToken(ctx, "proxy", s.tokenFile, s.tokenEnv)
	if err != nil {
		return err
	}

	conn, err := s.dial()
	if err != nil {
		return err
	}

	defer conn.Close()
	server, err := s.newServer(pb.NewBrokerServiceClient(conn), creds, logger)
	if err != nil {
		return err
	}

	httpListener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.httpAddress)
	if err != nil {
		return errors.New("cannot listen for HTTP")
	}

	defer func() { _ = httpListener.Close() }()
	grpcListener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.grpcAddress)
	if err != nil {
		return errors.New("cannot listen for gRPC")
	}

	defer func() { _ = grpcListener.Close() }()

	healthServer := health.NewServer()
	grpcServer := grpc.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	// No read or write timeout, unlike servicekit.NewHTTPServer: Git pushes
	// and forge responses stream and may be large.
	httpServer := &http.Server{
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	stopping := func() {
		server.SetStopping()
		healthServer.Shutdown()
	}

	return serve(ctx, grpcServer, grpcListener, httpServer, httpListener, stopping)
}

// newServer builds the proxy for the supported forges over the broker client.
func (s settings) newServer(client pb.BrokerServiceClient, creds forgeauth.Credentials, logger *slog.Logger) (*service.Server, error) {
	var registry forgeauth.Registry
	for _, provider := range []forgeauth.Provider{github.Provider{}, gitlab.Provider{}, forgejo.Provider{}} {
		if err := registry.Register(provider); err != nil {
			return nil, errors.New("cannot register forge provider")
		}
	}

	brk, err := service.NewBroker(service.BrokerConfig{
		Client:      client,
		Credentials: creds,
		TokenTTL:    s.tokenTTL,
		VerifyTTL:   s.verifyTTL,
		Timeout:     s.brokerTimeout,
		Logger:      logger,
	})
	if err != nil {
		return nil, errors.New("cannot initialize broker client")
	}

	server, err := service.New(service.Config{
		Broker:          brk,
		Registry:        &registry,
		PublicURL:       s.publicURL,
		Logger:          logger,
		AllowGitPush:    s.allowGitPush,
		MaxGitPushBytes: s.maxGitPushBytes,
		DownloadHosts:   splitDownloadHosts(s.downloadHosts),
	})
	if err != nil {
		return nil, errors.New("cannot initialize proxy")
	}

	return server, nil
}

// serve runs both servers until ctx ends or one stops. It then calls
// stopping and drains the servers.
func serve(
	ctx context.Context,
	grpcServer *grpc.Server,
	grpcListener net.Listener,
	httpServer *http.Server,
	httpListener net.Listener,
	stopping func(),
) error {
	failures := make(chan error, 2)
	go func() { failures <- grpcServer.Serve(grpcListener) }()
	go func() { failures <- httpServer.Serve(httpListener) }()

	var err error
	select {
	case <-ctx.Done():
	case failure := <-failures:
		err = errors.Join(errors.New("proxy listener stopped unexpectedly"), failure)
	}

	stopping()
	servicekit.Shutdown(ctx, grpcServer, httpServer)

	return err
}

func main() {
	servicekit.Main(func(ctx context.Context) error {
		s, err := parse(os.Args[1:], os.Getenv, os.Stdout)
		if err != nil {
			return err
		}

		return run(ctx, s, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	})
}

func splitDownloadHosts(value string) []string {
	if value == "" {
		return nil
	}

	return strings.Split(value, ",")
}
