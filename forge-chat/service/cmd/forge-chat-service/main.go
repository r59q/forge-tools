// Command forge-chat-service serves the chat app, its metadata and forge-agents
// forwarding routes.
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"time"

	"github.com/r59q/forge-tools/forge-chat/service/api"
	pb "github.com/r59q/forge-tools/forge-chat/service/chatpb"
	"github.com/r59q/forge-tools/forge-chat/service/store"
	"github.com/r59q/forge-tools/forge-chat/service/web"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc"
)

type settings struct {
	servicekit.Settings
	assets, data, agentsTokenFile, agentsTokenEnv string
	agentsURL                                     *url.URL
	streamTimeout                                 time.Duration
}

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	var s settings
	var upstream, streamTimeout string
	extra := func(flags *flag.FlagSet, env servicekit.Env) {
		flags.StringVar(&s.assets, "assets", env("ASSETS", "../frontend/dist/web"), "directory of prebuilt app assets")
		flags.StringVar(&s.data, "data-dir", env("DATA_DIR", "./chat-data"), "directory for chat metadata")
		flags.StringVar(&upstream, "agents-url", env("AGENTS_URL", "http://127.0.0.1:8096"), "forge-agents REST base URL")
		flags.StringVar(&s.agentsTokenFile, "agents-token-file", env("AGENTS_TOKEN_FILE", ""), "file containing the upstream bearer token")
		flags.StringVar(&s.agentsTokenEnv, "agents-token-env", env("AGENTS_TOKEN_ENV", ""), "name of the upstream token variable")
		flags.StringVar(&streamTimeout, "stream-timeout", env("STREAM_TIMEOUT", "11m"), "maximum duration of a forwarded stream")
	}
	shared, err := servicekit.Parse(args, getenv, help, servicekit.Options{
		Name: "forge-chat-service", Prefix: "FORGE_CHAT", GRPCListen: "127.0.0.1:9107", HTTPListen: "127.0.0.1:8097", Timeout: "30s",
	}, extra)
	if err != nil {
		return settings{}, err
	}
	s.Settings = shared
	if s.agentsTokenEnv != "" && s.agentsTokenFile != "" {
		return settings{}, errors.New("choose one upstream token source")
	}
	if s.agentsTokenFile == "" && s.agentsTokenEnv == "" {
		s.agentsTokenEnv = "FORGE_CHAT_AGENTS_TOKEN"
	}
	s.streamTimeout, err = time.ParseDuration(streamTimeout)
	if err != nil || s.streamTimeout <= 0 || s.assets == "" || s.data == "" {
		return settings{}, errors.New("invalid chat settings")
	}
	s.agentsURL, err = agentsURL(upstream)
	return s, err
}

func agentsURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("invalid forge-agents URL")
	}
	return u, nil
}

func run(ctx context.Context, s settings) (err error) {
	token, err := servicekit.BearerToken(ctx, "chat service", s.TokenFile, s.TokenEnv)
	if err != nil {
		return err
	}
	upstream, err := servicekit.BearerToken(ctx, "forge-agents", s.agentsTokenFile, s.agentsTokenEnv)
	if err != nil {
		return err
	}
	assets, err := os.OpenRoot(s.assets)
	if err != nil {
		return errors.New("cannot open app assets")
	}
	defer func() { err = errors.Join(err, assets.Close()) }()
	data, err := store.Open(s.data)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, data.Close()) }()

	metadata := api.NewMetadata(data, token, s.Timeout)
	rest, err := metadata.REST(ctx)
	if err != nil {
		return err
	}
	proxy := web.NewAgentProxy(s.agentsURL, upstream, s.Timeout, s.streamTimeout)
	return (servicekit.Server{
		GRPCAddress:  s.GRPCAddress,
		HTTPAddress:  s.HTTPAddress,
		Register:     func(g *grpc.Server) { pb.RegisterChatServiceServer(g, metadata) },
		Handler:      web.Handler(assets.FS(), rest, proxy, token),
		WriteTimeout: s.Timeout + 10*time.Second,
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
