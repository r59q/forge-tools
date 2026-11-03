// Command forge-issues-service serves issues and comments from every forge
// connected through forge-connector, over REST and gRPC.
package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"github.com/r59q/forge-tools/forge-connector/library/connector"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-issues/library/issues"
	"github.com/r59q/forge-tools/forge-issues/service/api"
	pb "github.com/r59q/forge-tools/forge-issues/service/issuespb"
	"google.golang.org/grpc"
)

type settings = servicekit.Settings

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	return servicekit.Parse(args, getenv, help, servicekit.Options{
		Name:              "forge-issues-service",
		Prefix:            "FORGE_ISSUES",
		GRPCListen:        "127.0.0.1:9094",
		HTTPListen:        "127.0.0.1:8084",
		Timeout:           "30s",
		ConnectorRequired: true,
	}, nil)
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

// jscpd:ignore-start - connector setup; forge-core cannot import forge-connector.

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

	// jscpd:ignore-end
	return servicekit.Server{
		GRPCAddress:  s.GRPCAddress,
		HTTPAddress:  s.HTTPAddress,
		Register:     func(g *grpc.Server) { pb.RegisterIssuesServiceServer(g, service) },
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
