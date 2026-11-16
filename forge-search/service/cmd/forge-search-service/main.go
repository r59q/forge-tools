// Command forge-search-service indexes and searches the code of
// forge-checkouts checkouts, and the issues of forge-issues, over REST and
// gRPC. It resolves checkouts through forge-checkouts and reads their
// directories from a shared data directory.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/r59q/forge-tools/forge-checkouts/service/checkoutspb"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-search/library/search"
	"github.com/r59q/forge-tools/forge-search/library/sqlite"
	"github.com/r59q/forge-tools/forge-search/service/api"
	pb "github.com/r59q/forge-tools/forge-search/service/searchpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// readTimeout covers servicekit's fixed HTTP read timeout, which is not part
// of the request timeout.
const readTimeout = 15 * time.Second

// copies is the directory of checkout directories in a forge-checkouts data
// directory.
const copies = "copies"

// journalDir is the directory, in the data directory, that lists the checkouts
// the service created and has not removed.
const journalDir = "checkouts"

// settings are the shared service settings plus storage, forge-checkouts and
// forge-issues settings.
type settings struct {
	servicekit.Settings
	dataDirectory string

	// checkoutsAddress is the forge-checkouts gRPC address, reached over TLS
	// unless checkoutsInsecure.
	checkoutsAddress  string
	checkoutsInsecure bool

	// checkoutsTokenFile or checkoutsTokenEnv holds the forge-checkouts
	// service token.
	checkoutsTokenFile, checkoutsTokenEnv string

	// root holds the checkout directories.
	root api.Root

	issues issueSettings

	indexTimeout time.Duration
	maxIndexes   int
}

func parse(args []string, getenv func(string) string, help io.Writer) (settings, error) {
	var s settings
	var checkoutsInsecure, checkoutsData, checkoutsReported, issuesInsecure, indexTimeout, maxIndexes string
	extra := func(fs *flag.FlagSet, env servicekit.Env) {
		fs.StringVar(&s.dataDirectory, "data-dir", env("DATA_DIR", "./search-data"), "durable index data directory")
		fs.StringVar(&s.checkoutsAddress, "checkouts-addr", env("CHECKOUTS_ADDR", "127.0.0.1:9098"), "forge-checkouts gRPC address")
		fs.StringVar(&checkoutsInsecure, "checkouts-insecure", env("CHECKOUTS_INSECURE", "false"),
			"reach forge-checkouts without TLS (true or false)")
		fs.StringVar(&s.checkoutsTokenFile, "checkouts-token-file", env("CHECKOUTS_TOKEN_FILE", ""),
			"file containing the forge-checkouts service token")
		fs.StringVar(&s.checkoutsTokenEnv, "checkouts-token-env", env("CHECKOUTS_TOKEN_ENV", ""),
			"forge-checkouts token variable name (default FORGE_SEARCH_CHECKOUTS_TOKEN)")
		fs.StringVar(&checkoutsData, "checkouts-data-dir", env("CHECKOUTS_DATA_DIR", ""),
			"forge-checkouts data directory as mounted here (required)")
		fs.StringVar(&checkoutsReported, "checkouts-reported-data-dir", env("CHECKOUTS_REPORTED_DATA_DIR", ""),
			"absolute forge-checkouts data directory as forge-checkouts names it, when mounted elsewhere")
		fs.StringVar(&indexTimeout, "index-timeout", env("INDEX_TIMEOUT", "10m"), "deadline for each checkout indexing")
		fs.StringVar(&maxIndexes, "max-indexes", env("MAX_INDEXES", strconv.Itoa(api.DefaultMaxIndexes)), "most indexes")
		issueFlags(fs, env, &s.issues, &issuesInsecure)
	}

	shared, err := servicekit.Parse(args, getenv, help, servicekit.Options{
		Name:       "forge-search-service",
		Prefix:     "FORGE_SEARCH",
		GRPCListen: "127.0.0.1:9105",
		HTTPListen: "127.0.0.1:8095",
		Timeout:    "30s",
	}, extra)
	if err != nil {
		return settings{}, err
	}
	s.Settings = shared

	if s.checkoutsInsecure, err = strconv.ParseBool(checkoutsInsecure); err != nil {
		return settings{}, errors.New("invalid checkouts-insecure setting")
	}
	if s.indexTimeout, err = time.ParseDuration(indexTimeout); err != nil || s.indexTimeout <= 0 {
		return settings{}, errors.New("invalid index-timeout")
	}
	if s.maxIndexes, err = strconv.Atoi(maxIndexes); err != nil || s.maxIndexes <= 0 {
		return settings{}, errors.New("invalid max-indexes")
	}
	if s.root, err = root(checkoutsData, checkoutsReported); err != nil {
		return settings{}, err
	}
	if err := s.issues.check(issuesInsecure); err != nil {
		return settings{}, err
	}

	if s.dataDirectory == "" || s.checkoutsAddress == "" {
		return settings{}, errors.New("invalid startup settings")
	}
	if s.checkoutsTokenFile != "" && s.checkoutsTokenEnv != "" {
		return settings{}, errors.New("choose one checkouts token source")
	}
	if s.checkoutsTokenFile == "" && s.checkoutsTokenEnv == "" {
		s.checkoutsTokenEnv = "FORGE_SEARCH_CHECKOUTS_TOKEN"
	}

	return s, nil
}

// root returns the checkout directories of a forge-checkouts data directory,
// and where forge-checkouts reports them when reported is set.
func root(data, reported string) (api.Root, error) {
	if data == "" {
		return api.Root{}, errors.New("checkouts-data-dir is required")
	}
	local, err := filepath.Abs(data)
	if err != nil {
		return api.Root{}, errors.New("invalid checkouts-data-dir")
	}

	r := api.Root{Local: filepath.Join(local, copies)}
	if reported != "" {
		if !filepath.IsAbs(reported) {
			return api.Root{}, errors.New("checkouts-reported-data-dir must be absolute")
		}
		r.Reported = filepath.Join(reported, copies)
	}
	return r, nil
}

// dial connects to the service name names, over TLS unless plaintext.
func dial(name, address string, plaintext bool) (*grpc.ClientConn, error) {
	transport := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	if plaintext {
		transport = insecure.NewCredentials()
	}

	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, fmt.Errorf("invalid %s address", name)
	}
	return conn, nil
}

// openData creates the data directory, or checks that an existing one is
// private, and opens the index store in it.
func openData(ctx context.Context, dir string) (*sqlite.Store, error) {
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

	store, err := sqlite.Open(ctx, dir)
	if err != nil {
		return nil, errors.New("cannot open index storage")
	}
	return store, nil
}

func run(ctx context.Context, s settings, ready func(grpcAddr, httpAddr net.Addr)) (err error) {
	token, err := servicekit.BearerToken(ctx, "service", s.TokenFile, s.TokenEnv)
	if err != nil {
		return err
	}
	checkoutsToken, err := servicekit.BearerToken(ctx, "checkouts", s.checkoutsTokenFile, s.checkoutsTokenEnv)
	if err != nil {
		return err
	}

	conn, err := dial("checkouts", s.checkoutsAddress, s.checkoutsInsecure)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	issues, issuesConn, err := s.issues.connect(ctx)
	if err != nil {
		return err
	}
	if issuesConn != nil {
		defer func() { _ = issuesConn.Close() }()
	}

	store, err := openData(ctx, s.dataDirectory)
	if err != nil {
		return err
	}
	// Closing copies the write-ahead log into the database file, after ctx
	// has ended.
	defer func() { //nolint:contextcheck // runs after ctx ends
		if closeErr := store.Close(); err == nil && closeErr != nil {
			err = errors.New("cannot close index storage")
		}
	}()

	journal, err := api.OpenJournal(filepath.Join(s.dataDirectory, journalDir))
	if err != nil {
		return errors.New("cannot open checkout journal")
	}

	config := api.Config{
		Engine:       search.NewEngine(store, search.IndexLimits{}),
		Checkpoint:   store.Checkpoint,
		Checkouts:    api.NewRemote(checkoutspb.NewCheckoutsServiceClient(conn), checkoutsToken),
		Journal:      journal,
		Root:         s.root,
		Token:        token,
		Timeout:      s.Timeout,
		IndexTimeout: s.indexTimeout,
		MaxIndexes:   s.maxIndexes,
	}
	if issues != nil { // A nil *RemoteIssues is not a nil reader.
		config.Issues = issues
	}
	service, err := api.New(config)
	if err != nil {
		return errors.New("cannot initialize API")
	}

	// Remove the checkouts an earlier run left, as far as forge-checkouts
	// answers in time.
	service.Sweep(ctx)

	handler, err := service.REST(ctx)
	if err != nil {
		return errors.New("cannot initialize REST API")
	}

	return servicekit.Server{
		GRPCAddress:  s.GRPCAddress,
		HTTPAddress:  s.HTTPAddress,
		Register:     func(g *grpc.Server) { pb.RegisterSearchServiceServer(g, service) },
		GRPCOptions:  service.GRPCOptions(),
		Handler:      handler,
		WriteTimeout: max(s.Timeout, s.indexTimeout) + readTimeout,
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
