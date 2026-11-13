// Command forge-symbols-service finds definitions and references in source
// files and navigates between them, over REST and gRPC. It reads only
// request content and needs no forge access.
package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-symbols/library/symbols"
	"github.com/r59q/forge-tools/forge-symbols/service/api"
	pb "github.com/r59q/forge-tools/forge-symbols/service/symbolspb"
	"google.golang.org/grpc"
)

// readTimeout covers servicekit's fixed HTTP read timeout, which is not part
// of the request timeout.
const readTimeout = 15 * time.Second

type settings = servicekit.Settings

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	return servicekit.Parse(args, getenv, help, servicekit.Options{
		Name:       "forge-symbols-service",
		Prefix:     "FORGE_SYMBOLS",
		GRPCListen: "127.0.0.1:9104",
		HTTPListen: "127.0.0.1:8094",
		Timeout:    "30s",
	}, nil)
}

func run(ctx context.Context, s settings, ready func(grpcAddr, httpAddr net.Addr)) (err error) {
	token, err := servicekit.BearerToken(ctx, "service", s.TokenFile, s.TokenEnv)
	if err != nil {
		return err
	}

	analyzer := symbols.New(symbols.Limits{})
	defer func() {
		if closeErr := analyzer.Close(context.WithoutCancel(ctx)); err == nil && closeErr != nil {
			err = errors.New("cannot close analyzer")
		}
	}()

	service, err := api.New(analyzer, token, s.Timeout)
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
		Register:     func(g *grpc.Server) { pb.RegisterSymbolsServiceServer(g, service) },
		GRPCOptions:  service.GRPCOptions(),
		Handler:      handler,
		WriteTimeout: s.Timeout + readTimeout,
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
