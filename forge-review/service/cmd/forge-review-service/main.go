// Command forge-review-service serves durable reviews over REST and gRPC.
// Connected subjects optionally use sessions from forge-connector.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"time"

	"github.com/r59q/forge-tools/forge-connector/library/connector"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-review/library/review"
	"github.com/r59q/forge-tools/forge-review/service/api"
	pb "github.com/r59q/forge-tools/forge-review/service/reviewpb"
	"github.com/r59q/forge-tools/forge-review/service/storage"
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
		fs.StringVar(&dataDirectory, "data-dir", env("DATA_DIR", "./review-data"), "durable review data directory")
	}

	shared, err := servicekit.Parse(args, getenv, help, servicekit.Options{
		Name:       "forge-review-service",
		Prefix:     "FORGE_REVIEW",
		GRPCListen: "127.0.0.1:9097",
		HTTPListen: "127.0.0.1:8087",
		Timeout:    "30s",
	}, extra)
	if err != nil {
		return settings{}, err
	}

	if dataDirectory == "" {
		return settings{}, errors.New("invalid startup settings")
	}

	return settings{Settings: shared, dataDirectory: dataDirectory}, nil
}

// sources asks the connector for its connections on every request and reads
// each as a source named by its connection ID. The connector supplies
// authenticated sessions, so this service never handles forge credentials.
func sources(conn *connector.Client) api.Sources {
	return func(ctx context.Context) ([]review.Source, error) {
		conns, err := conn.Connections(ctx)
		if err != nil {
			return nil, err
		}

		sources := make([]review.Source, len(conns))
		for i, c := range conns {
			sources[i] = review.Source{ID: string(c.ID), Session: c.Session}
		}

		return sources, nil
	}
}

// connectorSources returns connector sources when a connector URL is set,
// and nil for a standalone service.
func connectorSources(ctx context.Context, s settings) (api.Sources, error) {
	// jscpd:ignore-start - optional connector setup, shared only with forge-checkouts.
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
		return errors.New("cannot open review data directory")
	}

	discover, err := connectorSources(ctx, s)
	if err != nil {
		return err
	}

	service, err := api.New(store, discover, token, s.Timeout)
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
		Register:     func(g *grpc.Server) { pb.RegisterReviewServiceServer(g, service) },
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
