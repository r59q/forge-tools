// Command forge-checkouts-service serves durable checkouts over REST and gRPC.
// Connected repositories optionally use sessions from forge-connector.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"time"

	"github.com/r59q/forge-tools/forge-checkouts/library/checkouts"
	"github.com/r59q/forge-tools/forge-checkouts/service/api"
	pb "github.com/r59q/forge-tools/forge-checkouts/service/checkoutspb"
	"github.com/r59q/forge-tools/forge-checkouts/service/storage"
	"github.com/r59q/forge-tools/forge-connector/library/connector"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc"
)

// settings are the shared service settings plus the data directory.
type settings struct {
	servicekit.Settings
	dataDirectory string
}

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	var dataDirectory string
	extra := func(fs *flag.FlagSet, env servicekit.Env) {
		fs.StringVar(&dataDirectory, "data-dir", env("DATA_DIR", "./checkout-data"), "durable checkout data directory")
	}

	shared, err := servicekit.Parse(args, getenv, help, servicekit.Options{
		Name:       "forge-checkouts-service",
		Prefix:     "FORGE_CHECKOUTS",
		GRPCListen: "127.0.0.1:9098",
		HTTPListen: "127.0.0.1:8088",
		Timeout:    "5m",
	}, extra)
	if err != nil {
		return settings{}, err
	}

	if dataDirectory == "" {
		return settings{}, errors.New("invalid startup settings")
	}

	return settings{Settings: shared, dataDirectory: dataDirectory}, nil
}

// sources asks the connector for its connections on every creation and reads
// each as a source named by its connection ID. The connector supplies
// authenticated sessions, so this service never handles forge credentials.
func sources(conn *connector.Client) checkouts.Resolve {
	return func(ctx context.Context, id string) (*forgeauth.GitRemote, error) {
		conns, err := conn.Connections(ctx)
		if err != nil {
			return nil, err
		}

		for _, c := range conns {
			if string(c.ID) == id {
				return c.Git, nil
			}
		}

		return nil, checkouts.ErrNotFound
	}
}

// discovery returns the connector's sources, or nil without a connector.
func discovery(ctx context.Context, s settings) (checkouts.Resolve, error) {
	// jscpd:ignore-start - optional connector setup, shared only with forge-review.
	if s.ConnectorURL == "" {
		return nil, nil
	}

	token, err := servicekit.BearerToken(ctx, "connector", s.ConnectorTokenFile, s.ConnectorTokenEnv)
	if err != nil {
		return nil, err
	}

	conn, err := connector.New(connector.Config{URL: s.ConnectorURL, Credentials: token})
	if err != nil {
		return nil, errors.New("connector URL must be an http or https URL")
	}

	return sources(conn), nil
	// jscpd:ignore-end
}

func run(ctx context.Context, s settings, ready func(grpcAddr, httpAddr net.Addr)) error {
	token, err := servicekit.BearerToken(ctx, "service", s.TokenFile, s.TokenEnv)
	if err != nil {
		return err
	}

	store, err := storage.Open(ctx, s.dataDirectory)
	if err != nil {
		return errors.New("cannot open checkout data directory")
	}

	discover, err := discovery(ctx, s)
	if err != nil {
		return err
	}

	manager, err := checkouts.New(ctx, s.dataDirectory, store, discover)
	if err != nil {
		return errors.New("cannot open checkout manager")
	}

	defer manager.Close()

	service, err := api.New(manager, token, s.Timeout)
	if err != nil {
		return errors.New("cannot initialize API")
	}

	handler, err := service.REST(ctx)
	if err != nil {
		return errors.New("cannot initialize REST API")
	}

	return servicekit.Server{
		GRPCAddress:  s.GRPCAddress,
		HTTPAddress:  s.HTTPAddress,
		GRPCOptions:  []grpc.ServerOption{grpc.MaxRecvMsgSize(20 << 20)},
		Register:     func(g *grpc.Server) { pb.RegisterCheckoutsServiceServer(g, service) },
		Handler:      handler,
		WriteTimeout: s.Timeout + 5*time.Second,
		Ready:        ready,
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
