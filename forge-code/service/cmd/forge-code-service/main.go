// Command forge-code-service serves repository content from every forge
// connected through forge-connector, over REST and gRPC.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/r59q/forge-tools/forge-code/library/code"
	"github.com/r59q/forge-tools/forge-code/service/api"
	pb "github.com/r59q/forge-tools/forge-code/service/codepb"
	"github.com/r59q/forge-tools/forge-connector/library/connector"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc"
)

// settings are the shared settings plus the stream and in-flight limits.
type settings struct {
	servicekit.Settings
	streamTimeout time.Duration
	maxInFlight   int
}

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	var streamTimeout, maxInFlight string
	extra := func(fs *flag.FlagSet, env servicekit.Env) {
		fs.StringVar(&streamTimeout, "stream-timeout", env("STREAM_TIMEOUT", "10m"), "maximum stream and raw download duration")
		fs.StringVar(&maxInFlight, "max-inflight-per-source", env("MAX_INFLIGHT_PER_SOURCE", "8"), "maximum concurrent requests per source")
	}

	shared, err := servicekit.Parse(args, getenv, help, servicekit.Options{
		Name:              "forge-code-service",
		Prefix:            "FORGE_CODE",
		GRPCListen:        "127.0.0.1:9095",
		HTTPListen:        "127.0.0.1:8085",
		Timeout:           "30s",
		ConnectorRequired: true,
	}, extra)
	if err != nil {
		return settings{}, err
	}

	return limits(shared, streamTimeout, maxInFlight)
}

// limits parses and checks the stream timeout and in-flight limit.
func limits(shared servicekit.Settings, streamTimeout, maxInFlight string) (settings, error) {
	s := settings{Settings: shared}

	var err error
	if s.streamTimeout, err = time.ParseDuration(streamTimeout); err != nil {
		return settings{}, errors.New("invalid stream timeout")
	}

	if s.maxInFlight, err = strconv.Atoi(maxInFlight); err != nil || s.maxInFlight <= 0 {
		return settings{}, errors.New("invalid in-flight limit")
	}

	if s.streamTimeout <= 0 {
		return settings{}, errors.New("invalid startup settings")
	}

	return s, nil
}

// sources asks the connector for its connections on every request and reads
// each as a source named by its connection ID, with its Git remote for
// pushes. The connector supplies authenticated sessions, so this service
// never handles forge credentials.
func sources(conn *connector.Client) api.Sources {
	return func(ctx context.Context) (*code.Client, error) {
		conns, err := conn.Connections(ctx)
		if err != nil {
			return nil, err
		}

		sources := make([]code.Source, len(conns))
		for i, c := range conns {
			sources[i] = code.Source{ID: string(c.ID), Session: c.Session, Git: c.Git}
		}

		return code.New(sources...)
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

	service, err := api.New(sources(conn), token, api.Config{Timeout: s.Timeout, StreamTimeout: s.streamTimeout, MaxInFlight: s.maxInFlight})
	if err != nil {
		return errors.New("cannot initialize API")
	}

	handler, err := service.REST(ctx)
	if err != nil {
		return errors.New("cannot initialize REST API")
	}

	// jscpd:ignore-end
	// Raw downloads and commit uploads extend their own read and write
	// deadlines.
	return servicekit.Server{
		GRPCAddress:  s.GRPCAddress,
		HTTPAddress:  s.HTTPAddress,
		Register:     func(g *grpc.Server) { pb.RegisterCodeServiceServer(g, service) },
		GRPCOptions:  service.GRPCOptions(),
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
