// Command forge-actions-service serves pipelines, jobs, logs and artifacts
// from every forge connected through forge-connector, over REST and gRPC.
package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"github.com/r59q/forge-tools/forge-actions/library/actions"
	pb "github.com/r59q/forge-tools/forge-actions/service/actionspb"
	"github.com/r59q/forge-tools/forge-actions/service/api"
	"github.com/r59q/forge-tools/forge-connector/library/connector"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc"
)

type settings = servicekit.Settings

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	return servicekit.Parse(args, getenv, help, servicekit.Options{
		Name:              "forge-actions-service",
		Prefix:            "FORGE_ACTIONS",
		GRPCListen:        "127.0.0.1:9099",
		HTTPListen:        "127.0.0.1:8089",
		Timeout:           "30s",
		ConnectorRequired: true,
	}, nil)
}

// sources asks the connector for its connections on every request and reads
// each as a source named by its connection ID. The connector supplies
// authenticated sessions, so this service never handles forge credentials.
func sources(conn *connector.Client) api.Sources {
	return func(ctx context.Context) (*actions.Client, error) {
		conns, err := conn.Connections(ctx)
		if err != nil {
			return nil, err
		}

		sources := make([]actions.Source, len(conns))
		for i, c := range conns {
			sources[i] = actions.Source{ID: string(c.ID), Session: c.Session}
		}

		return actions.New(sources...)
	}
}

func run(ctx context.Context, s settings, ready func(grpcAddr, httpAddr net.Addr)) error {
	token, err := servicekit.BearerToken(ctx, "service", s.TokenFile, s.TokenEnv)
	if err != nil {
		return err
	}

	connectorToken, err := servicekit.BearerToken(ctx, "connector", s.ConnectorTokenFile, s.ConnectorTokenEnv)
	if err != nil {
		return err
	}

	conn, err := connector.New(connector.Config{URL: s.ConnectorURL, Credentials: connectorToken})
	if err != nil {
		return errors.New("connector URL must be an http or https URL")
	}

	service, err := api.New(sources(conn), token, s.Timeout)
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
		Register:     func(g *grpc.Server) { pb.RegisterActionsServiceServer(g, service) },
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
