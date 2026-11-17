package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"path/filepath"
	"strconv"

	"github.com/r59q/forge-tools/forge-agents/service/api"
	"github.com/r59q/forge-tools/forge-checkouts/service/checkoutspb"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// copies is the directory of checkout directories in a forge-checkouts data
// directory.
const copies = "copies"

// checkoutSettings reach forge-checkouts, which creates checkout
// workspaces. Without an address, checkout workspaces are unavailable.
type checkoutSettings struct {
	// address is the forge-checkouts gRPC address, reached over TLS unless
	// insecure.
	address  string
	insecure bool

	// tokenFile or tokenEnv holds the forge-checkouts service token.
	tokenFile, tokenEnv string

	// root holds the checkout directories.
	root api.Root
}

func checkoutFlags(fs *flag.FlagSet, env servicekit.Env, c *checkoutSettings, f *flags) {
	fs.StringVar(&c.address, "checkouts-addr", env("CHECKOUTS_ADDR", ""),
		"forge-checkouts gRPC address; empty disables checkout workspaces")
	fs.StringVar(&f.checkoutsInsecure, "checkouts-insecure", env("CHECKOUTS_INSECURE", "false"),
		"reach forge-checkouts without TLS (true or false)")
	fs.StringVar(&c.tokenFile, "checkouts-token-file", env("CHECKOUTS_TOKEN_FILE", ""),
		"file containing the forge-checkouts service token")
	fs.StringVar(&c.tokenEnv, "checkouts-token-env", env("CHECKOUTS_TOKEN_ENV", ""),
		"forge-checkouts token variable name (default "+prefix+"_CHECKOUTS_TOKEN)")
	fs.StringVar(&f.checkoutsData, "checkouts-data-dir", env("CHECKOUTS_DATA_DIR", ""),
		"forge-checkouts data directory as mounted here, writable (required with checkouts-addr)")
	fs.StringVar(&f.checkoutsReported, "checkouts-reported-data-dir", env("CHECKOUTS_REPORTED_DATA_DIR", ""),
		"absolute forge-checkouts data directory as forge-checkouts names it, when mounted elsewhere")
}

func (c *checkoutSettings) enabled() bool {
	return c.address != ""
}

// finish parses the raw checkout values of f into c and checks c.
func (c *checkoutSettings) finish(f flags) error {
	var err error
	if c.insecure, err = strconv.ParseBool(f.checkoutsInsecure); err != nil {
		return errors.New("invalid checkouts-insecure setting")
	}
	if c.tokenFile != "" && c.tokenEnv != "" {
		return errors.New("choose one checkouts token source")
	}
	if !c.enabled() {
		if c.tokenFile != "" || c.tokenEnv != "" || f.checkoutsData != "" || f.checkoutsReported != "" {
			return errors.New("checkouts settings need checkouts-addr")
		}
		return nil
	}
	if c.tokenFile == "" && c.tokenEnv == "" {
		c.tokenEnv = prefix + "_CHECKOUTS_TOKEN"
	}
	c.root, err = root(f.checkoutsData, f.checkoutsReported)
	return err
}

// root returns the checkout directories of a forge-checkouts data directory,
// and where forge-checkouts reports them when reported is set.
func root(data, reported string) (api.Root, error) {
	if data == "" {
		return api.Root{}, errors.New("checkouts-data-dir is required with checkouts-addr")
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

// connect checks the forge-checkouts token and dials forge-checkouts.
func (c *checkoutSettings) connect(ctx context.Context) (*api.Remote, *grpc.ClientConn, error) {
	token, err := servicekit.BearerToken(ctx, "checkouts", c.tokenFile, c.tokenEnv)
	if err != nil {
		return nil, nil, err
	}
	transport := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	if c.insecure {
		transport = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(c.address, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, nil, errors.New("invalid checkouts address")
	}
	return api.NewRemote(checkoutspb.NewCheckoutsServiceClient(conn), token, c.root), conn, nil
}
