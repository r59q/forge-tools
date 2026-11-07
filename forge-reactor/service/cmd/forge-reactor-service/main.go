// Command forge-reactor-service accepts forge events from signed webhooks,
// Forgejo branch polling and authenticated publishers, and delivers them to
// durable subscriptions through RabbitMQ. It serves REST and gRPC.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/r59q/forge-tools/forge-connector/library/connector"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-reactor/library/poll"
	"github.com/r59q/forge-tools/forge-reactor/library/rabbitmq"
	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	"github.com/r59q/forge-tools/forge-reactor/library/sources"
	"github.com/r59q/forge-tools/forge-reactor/service/api"
	"github.com/r59q/forge-tools/forge-reactor/service/config"
	pb "github.com/r59q/forge-tools/forge-reactor/service/reactorpb"
	"github.com/r59q/forge-tools/forge-reactor/service/storage"
	"google.golang.org/grpc"
)

// settings are the shared service settings plus the reactor's own.
type settings struct {
	servicekit.Settings
	dataDirectory string
	configFile    string
	rabbitFile    string
	rabbitEnv     string
	queuePrefix   string
	publicURL     string
}

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	var s settings
	extra := func(fs *flag.FlagSet, env servicekit.Env) {
		fs.StringVar(&s.dataDirectory, "data-dir", env("DATA_DIR", "./reactor-data"), "durable reactor data directory")
		fs.StringVar(&s.configFile, "config", env("CONFIG", ""), "JSON file listing webhook sources and poll targets")
		fs.StringVar(&s.rabbitFile, "rabbitmq-url-file", env("RABBITMQ_URL_FILE", ""), "file containing the RabbitMQ URL")
		fs.StringVar(&s.rabbitEnv, "rabbitmq-url-env", env("RABBITMQ_URL_ENV", ""),
			"RabbitMQ URL variable name (default FORGE_REACTOR_RABBITMQ_URL)")
		fs.StringVar(&s.queuePrefix, "queue-prefix", env("QUEUE_PREFIX", rabbitmq.DefaultPrefix), "prefix of every subscription queue")
		fs.StringVar(&s.publicURL, "public-url", env("PUBLIC_URL", ""), "external base URL that forges reach, for webhook registration")
	}

	shared, err := servicekit.Parse(args, getenv, help, servicekit.Options{
		Name:       "forge-reactor-service",
		Prefix:     "FORGE_REACTOR",
		GRPCListen: "127.0.0.1:9103",
		HTTPListen: "127.0.0.1:8093",
		Timeout:    "30s",
	}, extra)
	if err != nil {
		return settings{}, err
	}
	s.Settings = shared

	switch {
	case s.dataDirectory == "" || s.queuePrefix == "":
		return settings{}, errors.New("invalid startup settings")
	case s.rabbitFile != "" && s.rabbitEnv != "":
		return settings{}, errors.New("choose one RabbitMQ URL source")
	case s.rabbitFile == "" && s.rabbitEnv == "":
		s.rabbitEnv = "FORGE_REACTOR_RABBITMQ_URL"
	}

	return s, nil
}

// brokerURL reads the RabbitMQ URL once. Errors never contain it.
func brokerURL(ctx context.Context, s settings) (string, error) {
	var source forgeauth.Credentials = forgeauth.EnvToken(s.rabbitEnv)
	if s.rabbitFile != "" {
		source = forgeauth.FileToken(s.rabbitFile)
	}
	url, err := source.Token(ctx)
	if err != nil || url == "" {
		return "", errors.New("cannot read RabbitMQ URL")
	}

	return url, nil
}

// sessions asks the connector for its connections on every call and opens
// the one named by ID, or returns nil without a connector URL. The
// connector supplies authenticated sessions, so this service never handles
// forge credentials.
func sessions(ctx context.Context, s settings) (sources.Sessions, error) {
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

	return func(ctx context.Context, id string) (*forgeauth.Session, error) {
		conns, err := conn.Connections(ctx)
		if err != nil {
			return nil, err
		}
		for _, c := range conns {
			if string(c.ID) == id {
				return c.Session, nil
			}
		}

		return nil, reactor.ErrNotFound
	}, nil
}

// poller returns the configured poller, or nil without poll targets.
func poller(r *reactor.Reactor, open sources.Sessions, c config.Config, log io.Writer) (*poll.Poller, error) {
	if len(c.Targets) == 0 {
		return nil, nil
	}
	if open == nil {
		return nil, errors.New("poll targets need a connector URL: set --connector-url or FORGE_REACTOR_CONNECTOR_URL")
	}

	p := &poll.Poller{
		Reactor:  r,
		Sessions: poll.Sessions(open),
		Targets:  c.Targets,
		Interval: c.Interval,
		OnError: func(t poll.Target, branch string, _ error) {
			// The error may carry forge responses, so only names are logged.
			_, _ = fmt.Fprintf(log, "forge-reactor: poll %s branch %s failed; baseline kept\n", t.ID, branch)
		},
	}
	if err := p.Validate(); err != nil {
		return nil, errors.New("invalid poll target configuration")
	}

	return p, nil
}

// background runs the dispatcher and poller until ctx ends.
func background(ctx context.Context, r *reactor.Reactor, b reactor.Broker, p *poll.Poller) *sync.WaitGroup {
	var wg sync.WaitGroup
	wg.Go(func() { _ = r.Run(ctx, b) })
	if p != nil {
		wg.Go(func() { _ = p.Run(ctx) })
	}

	return &wg
}

func run(ctx context.Context, s settings, ready func(grpcAddr, httpAddr net.Addr)) error {
	token, err := servicekit.BearerToken(ctx, "service", s.TokenFile, s.TokenEnv)
	if err != nil {
		return err
	}
	url, err := brokerURL(ctx, s)
	if err != nil {
		return err
	}
	cfg, err := config.Load(s.configFile)
	if err != nil {
		return err
	}

	store, err := storage.Open(s.dataDirectory)
	if err != nil {
		return errors.New("cannot open reactor data directory")
	}
	defer store.Close()

	r, err := reactor.New(reactor.Config{Store: store.Store()})
	if err != nil {
		return errors.New("cannot load reactor state")
	}
	open, err := sessions(ctx, s)
	if err != nil {
		return err
	}
	src, err := webhookSources(ctx, s, store, open, cfg)
	if err != nil {
		return err
	}
	p, err := poller(r, open, cfg, os.Stderr)
	if err != nil {
		return err
	}
	broker, err := rabbitmq.New(rabbitmq.Config{URL: url, QueuePrefix: s.queuePrefix})
	if err != nil {
		return errors.New("invalid RabbitMQ URL")
	}
	defer broker.Close()

	return serve(ctx, s, token, r, broker, src, p, ready)
}

// webhookSources combines the configured sources with the managed ones in
// the data directory. Managed sources register webhooks with connector
// sessions at the public URL.
func webhookSources(ctx context.Context, s settings, store *storage.File, open sources.Sessions, cfg config.Config) (*api.Sources, error) {
	managed, err := sources.New(&sources.Manager{Store: store.Sources(), Sessions: open, PublicURL: s.publicURL})
	if err != nil {
		return nil, errors.New("public URL must be an http or https URL without query")
	}
	all, err := api.NewSources(ctx, cfg.Sources, managed)
	if err != nil {
		return nil, errors.New("invalid webhook source configuration; managed source IDs must not be configured")
	}

	return all, nil
}

// serve starts the dispatcher, poller and servers, and stops the background
// work once the servers have shut down.
func serve(ctx context.Context, s settings, token servicekit.Token, r *reactor.Reactor, b reactor.Broker, src *api.Sources, p *poll.Poller,
	ready func(grpcAddr, httpAddr net.Addr),
) error {
	hooks, err := api.NewWebhooks(r, src, s.Timeout)
	if err != nil {
		return errors.New("cannot initialize webhooks")
	}
	service, err := api.New(r, src, token, s.Timeout)
	if err != nil {
		return errors.New("cannot initialize API")
	}
	rest, err := service.REST(ctx)
	if err != nil {
		return errors.New("cannot initialize REST API")
	}

	ctx, cancel := context.WithCancel(ctx)
	wg := background(ctx, r, b, p)
	defer wg.Wait()
	defer cancel()

	return servicekit.Server{
		GRPCAddress:  s.GRPCAddress,
		HTTPAddress:  s.HTTPAddress,
		Register:     func(g *grpc.Server) { pb.RegisterReactorServiceServer(g, service) },
		Handler:      api.Routes(rest, hooks),
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
