// Command forge-tools-management-service serves management over HTTP and
// gRPC, optionally connected to forge-connector, forge-repositories and
// forge-issues and forge-actions.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-tools-management/library/management"
	"github.com/r59q/forge-tools/forge-tools-management/service/api"
	pb "github.com/r59q/forge-tools/forge-tools-management/service/managementpb"
	"google.golang.org/grpc"
)

type settings struct {
	grpcAddress, httpAddress string
	tokenFile, tokenEnv      string
	timeout                  time.Duration
	repositoriesAddress      string
	repositoriesTokenFile    string
	repositoriesTokenEnv     string
	repositoriesInsecure     bool
	issuesAddress            string
	issuesTokenFile          string
	issuesTokenEnv           string
	issuesInsecure           bool
	actionsAddress           string
	actionsTokenFile         string
	actionsTokenEnv          string
	actionsInsecure          bool
	connectorAddress         string
	connectorTokenFile       string
	connectorTokenEnv        string
	connectorInsecure        bool
	clients                  []serviceClient
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
	transports := s.backendFlags(fs, env)
	clients := clientFlags(fs, env)
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
	if err := s.configureTransports(transports); err != nil {
		return settings{}, err
	}
	if err := s.configureBackends(); err != nil {
		return settings{}, err
	}
	if err := s.configureClients(clients); err != nil {
		return settings{}, err
	}

	return s, nil
}

func run(ctx context.Context, s settings, ready func(grpcAddr, httpAddr net.Addr)) error {
	token, err := servicekit.BearerToken(ctx, "service", s.tokenFile, s.tokenEnv)
	if err != nil {
		return err
	}

	config, closeBackends, err := s.backends(ctx, token)
	if err != nil {
		return err
	}

	defer closeBackends()
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

	// Register service clients once serving, so the UI is up even when the
	// broker is not yet.
	registerCtx, stopRegister := context.WithCancel(ctx)
	var registering sync.WaitGroup
	defer registering.Wait()
	defer stopRegister()

	return servicekit.Server{
		GRPCAddress:  s.grpcAddress,
		HTTPAddress:  s.httpAddress,
		Register:     func(g *grpc.Server) { pb.RegisterManagementServiceServer(g, service) },
		Handler:      handler,
		WriteTimeout: s.timeout + 5*time.Second,
		Ready: func(grpcAddr, httpAddr net.Addr) {
			registering.Go(func() { register(registerCtx, client, clients, s.timeout) })
			if ready != nil {
				ready(grpcAddr, httpAddr)
			}
		},
	}.Run(ctx)
}

func main() {
	servicekit.Main(func(ctx context.Context) error {
		s, err := parse(os.Args[1:], os.Getenv, os.Stdout)
		if err != nil {
			return err
		}

		return run(ctx, s, nil)
	})
}
