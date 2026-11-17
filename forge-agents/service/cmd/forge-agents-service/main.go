// Command forge-agents-service runs headless agent sessions over the
// Claude Code, Codex and Gemini CLIs, over REST and gRPC. Agents work in
// scratch directories, allowed paths, or forge-checkouts worktrees.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-agents/library/adapter"
	"github.com/r59q/forge-tools/forge-agents/library/adapter/claude"
	"github.com/r59q/forge-tools/forge-agents/library/adapter/codex"
	"github.com/r59q/forge-tools/forge-agents/library/adapter/gemini"
	"github.com/r59q/forge-tools/forge-agents/library/agent"
	"github.com/r59q/forge-tools/forge-agents/library/executor"
	"github.com/r59q/forge-tools/forge-agents/library/workspace"
	pb "github.com/r59q/forge-tools/forge-agents/service/agentspb"
	"github.com/r59q/forge-tools/forge-agents/service/api"
	"github.com/r59q/forge-tools/forge-agents/service/store"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc"
)

// readTimeout covers servicekit's fixed HTTP read timeout, which is not part
// of the request timeout.
const readTimeout = 15 * time.Second

// prefix starts every variable of this service. Variables with the forge
// prefix are kept from client processes.
const prefix = "FORGE_AGENTS"

// Directories in the data directory.
const (
	scratchDir = "scratch"
	runDir     = "run"
)

// adapters are the supported clients by name.
//
//nolint:gochecknoglobals // a fixed table of the supported clients.
var adapters = map[string]adapter.Adapter{
	"claude": claude.Adapter{},
	"codex":  codex.Adapter{},
	"gemini": gemini.Adapter{},
}

// settings are the shared service settings plus storage, client, workspace
// and limit settings.
type settings struct {
	servicekit.Settings
	dataDirectory string

	// clients maps each enabled client to its binary, a path or a name
	// looked up in PATH.
	clients map[string]string

	// roots are the directories path workspaces must be inside.
	roots []string

	checkouts checkoutSettings

	bounds        agent.Bounds
	streamTimeout time.Duration
	toolsListen   string
}

// flags holds the raw values of the settings that need parsing.
type flags struct {
	clients, roots                                      string
	paths                                               map[string]*string
	maxAgents, maxRunning, maxEvents, turnTimeout       string
	streamTimeout                                       string
	checkoutsInsecure, checkoutsData, checkoutsReported string
}

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	var s settings
	f := flags{paths: map[string]*string{}}
	extra := func(fs *flag.FlagSet, env servicekit.Env) {
		fs.StringVar(&s.dataDirectory, "data-dir", env("DATA_DIR", "./agents-data"), "durable agent data directory")
		fs.StringVar(&f.clients, "clients", env("CLIENTS", "claude,codex,gemini"), "comma-separated clients to offer")
		for _, name := range []string{"claude", "codex", "gemini"} {
			f.paths[name] = fs.String(name+"-path", env(strings.ToUpper(name)+"_PATH", name), name+" binary, a path or a name in PATH")
		}
		fs.StringVar(&f.roots, "path-roots", env("PATH_ROOTS", ""),
			"comma-separated absolute directories path workspaces must be inside; empty disables path workspaces")
		fs.StringVar(&f.maxAgents, "max-agents", env("MAX_AGENTS", strconv.Itoa(agent.DefaultMaxAgents)), "most agents")
		fs.StringVar(&f.maxRunning, "max-running", env("MAX_RUNNING", strconv.Itoa(agent.DefaultMaxRunning)), "most turns running at once")
		fs.StringVar(&f.maxEvents, "max-events", env("MAX_EVENTS", strconv.Itoa(agent.DefaultMaxEvents)), "most events in one transcript")
		fs.StringVar(&f.turnTimeout, "turn-timeout", env("TURN_TIMEOUT", agent.DefaultTurnTimeout.String()), "longest turn")
		fs.StringVar(&f.streamTimeout, "stream-timeout", env("STREAM_TIMEOUT", "10m"),
			"longest event stream; callers resume from the last event")
		fs.StringVar(&s.toolsListen, "tools-listen", env("TOOLS_LISTEN", "127.0.0.1:0"),
			"listen address of the MCP tool server clients ask questions through")
		checkoutFlags(fs, env, &s.checkouts, &f)
	}

	shared, err := servicekit.Parse(args, getenv, help, servicekit.Options{
		Name:       "forge-agents-service",
		Prefix:     prefix,
		GRPCListen: "127.0.0.1:9106",
		HTTPListen: "127.0.0.1:8096",
		Timeout:    "30s",
	}, extra)
	if err != nil {
		return settings{}, err
	}
	s.Settings = shared
	if err := s.finish(f); err != nil {
		return settings{}, err
	}
	return s, nil
}

// finish parses the raw values of f into s and checks s.
func (s *settings) finish(f flags) error {
	if s.dataDirectory == "" || s.toolsListen == "" {
		return errors.New("invalid startup settings")
	}
	var err error
	if s.clients, err = clientsOf(f.clients, f.paths); err != nil {
		return err
	}
	if s.roots, err = rootsOf(f.roots); err != nil {
		return err
	}
	if s.bounds, err = boundsOf(f); err != nil {
		return err
	}
	if s.streamTimeout, err = time.ParseDuration(f.streamTimeout); err != nil || s.streamTimeout <= 0 {
		return errors.New("invalid stream-timeout")
	}
	return s.checkouts.finish(f)
}

// clientsOf returns the binaries of the clients a comma-separated list
// names.
func clientsOf(list string, paths map[string]*string) (map[string]string, error) {
	clients := map[string]string{}
	for name := range strings.SplitSeq(list, ",") {
		name = strings.TrimSpace(name)
		if _, ok := adapters[name]; !ok {
			return nil, errors.New("invalid clients: each must be claude, codex or gemini")
		}
		if *paths[name] == "" {
			return nil, fmt.Errorf("invalid %s-path", name)
		}
		clients[name] = *paths[name]
	}
	return clients, nil
}

// rootsOf returns the directories of a comma-separated list, each absolute
// and clean.
func rootsOf(list string) ([]string, error) {
	if list == "" {
		return nil, nil
	}
	var roots []string
	for root := range strings.SplitSeq(list, ",") {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return nil, errors.New("invalid path-roots: each must be an absolute, clean directory")
		}
		if !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}
	return roots, nil
}

func boundsOf(f flags) (agent.Bounds, error) {
	var b agent.Bounds
	var err error
	if b.MaxAgents, err = strconv.Atoi(f.maxAgents); err != nil || b.MaxAgents <= 0 {
		return b, errors.New("invalid max-agents")
	}
	if b.MaxRunning, err = strconv.Atoi(f.maxRunning); err != nil || b.MaxRunning <= 0 {
		return b, errors.New("invalid max-running")
	}
	if b.MaxEvents, err = strconv.ParseInt(f.maxEvents, 10, 64); err != nil || b.MaxEvents <= 0 {
		return b, errors.New("invalid max-events")
	}
	if b.TurnTimeout, err = time.ParseDuration(f.turnTimeout); err != nil || b.TurnTimeout <= 0 {
		return b, errors.New("invalid turn-timeout")
	}
	return b, nil
}

// resolve finds the binary of every client.
func resolve(clients map[string]string) (map[string]agent.Client, error) {
	resolved := map[string]agent.Client{}
	for name, path := range clients {
		found, err := exec.LookPath(path)
		if err != nil {
			return nil, fmt.Errorf("%s client binary not found", name)
		}
		if found, err = filepath.Abs(found); err != nil {
			return nil, fmt.Errorf("%s client binary not found", name)
		}
		resolved[name] = agent.Client{Adapter: adapters[name], Path: found}
	}
	return resolved, nil
}

// clientEnv returns the environment of client processes: this process's,
// without forge variables or the variables that hold service tokens.
func clientEnv(environ []string, tokens ...string) []string {
	return slices.DeleteFunc(slices.Clone(environ), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return strings.HasPrefix(name, "FORGE_") || slices.Contains(tokens, name)
	})
}

// openData creates the data directory, or checks that an existing one is
// private, creates its private subdirectories and opens the agent store.
func openData(ctx context.Context, dir string) (*store.SQLite, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.New("cannot create data directory")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("cannot open data directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("data directory must not be accessible to group or others (mode %v)", info.Mode().Perm())
	}
	for _, sub := range []string{scratchDir, runDir} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, errors.New("cannot create data directory")
		}
	}

	s, err := store.Open(ctx, dir)
	if err != nil {
		return nil, errors.New("cannot open agent storage")
	}
	return s, nil
}

func run(ctx context.Context, s settings, ready func(grpcAddr, httpAddr net.Addr)) (err error) {
	token, err := servicekit.BearerToken(ctx, "service", s.TokenFile, s.TokenEnv)
	if err != nil {
		return err
	}
	clients, err := resolve(s.clients)
	if err != nil {
		return err
	}
	workspaces := workspace.Local{Roots: s.roots}
	if s.checkouts.enabled() {
		checkouts, conn, err := s.checkouts.connect(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close() }()
		workspaces.Checkouts = checkouts
	}

	data, err := openData(ctx, s.dataDirectory)
	if err != nil {
		return err
	}
	// Closing copies the write-ahead log into the database file, after ctx
	// has ended and every turn has stopped.
	defer func() { //nolint:contextcheck // runs after ctx ends
		if closeErr := data.Close(); err == nil && closeErr != nil {
			err = errors.New("cannot close agent storage")
		}
	}()
	absolute, err := filepath.Abs(s.dataDirectory)
	if err != nil {
		return errors.New("cannot open data directory")
	}
	workspaces.ScratchDir = filepath.Join(absolute, scratchDir)

	manager, err := agent.Open(ctx, agent.Config{
		Store:       data,
		Clients:     clients,
		Executor:    executor.Local{},
		Workspaces:  workspaces,
		Env:         clientEnv(os.Environ(), s.TokenEnv, s.checkouts.tokenEnv),
		RunDir:      filepath.Join(absolute, runDir),
		Bounds:      s.bounds,
		ToolsListen: s.toolsListen,
	})
	if err != nil {
		return errors.New("cannot start agent manager")
	}
	// Ends running turns before storage closes.
	defer func() { _ = manager.Close() }() //nolint:contextcheck // runs after ctx ends

	return serve(ctx, s, manager, token, ready)
}

func serve(ctx context.Context, s settings, m *agent.Manager, token servicekit.Token, ready func(grpcAddr, httpAddr net.Addr)) error {
	service, err := api.New(api.Config{Manager: m, Token: token, Timeout: s.Timeout, StreamTimeout: s.streamTimeout})
	if err != nil {
		return errors.New("cannot initialize API")
	}
	handler, err := service.REST(ctx)
	if err != nil {
		return errors.New("cannot initialize REST API")
	}
	return servicekit.Server{
		GRPCAddress: s.GRPCAddress,
		HTTPAddress: s.HTTPAddress,
		Register:    func(g *grpc.Server) { pb.RegisterAgentsServiceServer(g, service) },
		GRPCOptions: service.GRPCOptions(),
		Handler:     handler,
		// The event stream extends its own write deadline.
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
