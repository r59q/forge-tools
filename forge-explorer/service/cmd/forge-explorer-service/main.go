// Command forge-explorer-service serves explicitly configured live worktrees.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strings"

	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-explorer/library/explorer"
	"github.com/r59q/forge-tools/forge-explorer/service/api"
	pb "github.com/r59q/forge-tools/forge-explorer/service/explorerpb"
	"github.com/r59q/forge-tools/forge-explorer/service/web"
	"google.golang.org/grpc"
)

type settings struct {
	servicekit.Settings
	roots  string
	assets string
}

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	var s settings
	shared, err := servicekit.Parse(args, getenv, help, servicekit.Options{
		Name: "forge-explorer-service", Prefix: "FORGE_EXPLORER",
		GRPCListen: "127.0.0.1:9100", HTTPListen: "127.0.0.1:8090", Timeout: "10s",
	}, func(fs *flag.FlagSet, env servicekit.Env) {
		fs.StringVar(&s.roots, "roots", env("ROOTS", ""), "semicolon-separated id=/absolute/root mappings (required)")
		fs.StringVar(&s.assets, "assets", env("ASSETS", "../frontend/dist/app"), "prebuilt standalone web assets")
	})
	s.Settings = shared
	if err != nil {
		return settings{}, err
	}
	if s.roots == "" || s.assets == "" {
		return settings{}, errors.New("roots and assets are required")
	}
	return s, nil
}

func openRoots(spec string) ([]*explorer.Worktree, error) {
	parts := strings.Split(spec, ";")
	if len(parts) > 32 {
		return nil, errors.New("at most 32 roots may be configured")
	}
	roots := make([]*explorer.Worktree, 0, len(parts))
	for _, part := range parts {
		id, directory, ok := strings.Cut(part, "=")
		if !ok || !strings.HasPrefix(directory, "/") {
			closeRoots(roots)
			return nil, errors.New("roots must be id=/absolute/path mappings")
		}
		root, err := explorer.Open(id, id, directory)
		if err != nil {
			closeRoots(roots)
			return nil, errors.New("cannot open configured root")
		}
		roots = append(roots, root)
	}
	return roots, nil
}

func closeRoots(roots []*explorer.Worktree) {
	for _, root := range roots {
		_ = root.Close()
	}
}

func run(ctx context.Context, s settings) error {
	token, err := servicekit.BearerToken(ctx, "service", s.TokenFile, s.TokenEnv)
	if err != nil {
		return err
	}
	roots, err := openRoots(s.roots)
	if err != nil {
		return err
	}
	defer closeRoots(roots)

	server, err := api.New(roots, token, s.Timeout)
	if err != nil {
		return errors.New("invalid explorer configuration")
	}
	handler, err := server.REST(ctx)
	if err != nil {
		return err
	}
	return (servicekit.Server{
		GRPCAddress: s.GRPCAddress, HTTPAddress: s.HTTPAddress,
		Register: func(g *grpc.Server) { pb.RegisterExplorerServiceServer(g, server) },
		Handler:  web.Handler(handler, os.DirFS(s.assets)), WriteTimeout: s.Timeout,
	}).Run(ctx)
}

func main() {
	servicekit.Main(func(ctx context.Context) error {
		s, err := parse(os.Args[1:], os.Getenv, os.Stdout)
		if err != nil {
			return err
		}
		return run(ctx, s)
	})
}
