package main

import (
	"context"
	"errors"
	"flag"
	"strconv"

	"github.com/r59q/forge-tools/forge-core/servicekit"
	"github.com/r59q/forge-tools/forge-issues/service/issuespb"
	"github.com/r59q/forge-tools/forge-search/service/api"
	"google.golang.org/grpc"
)

// issueSettings are the forge-issues settings. Without an address the issue
// RPCs fail with UNIMPLEMENTED and no issues token is read.
type issueSettings struct {
	// address is the forge-issues gRPC address, reached over TLS unless
	// insecure.
	address  string
	insecure bool

	// tokenFile or tokenEnv holds the forge-issues service token.
	tokenFile, tokenEnv string
}

// issueFlags adds the forge-issues flags; insecure receives the raw
// insecure setting.
func issueFlags(fs *flag.FlagSet, env servicekit.Env, s *issueSettings, insecure *string) {
	fs.StringVar(&s.address, "issues-addr", env("ISSUES_ADDR", ""),
		"forge-issues gRPC address (empty disables issue search)")
	fs.StringVar(insecure, "issues-insecure", env("ISSUES_INSECURE", "false"),
		"reach forge-issues without TLS (true or false)")
	fs.StringVar(&s.tokenFile, "issues-token-file", env("ISSUES_TOKEN_FILE", ""),
		"file containing the forge-issues service token")
	fs.StringVar(&s.tokenEnv, "issues-token-env", env("ISSUES_TOKEN_ENV", ""),
		"forge-issues token variable name (default FORGE_SEARCH_ISSUES_TOKEN)")
}

// check parses the insecure setting and checks the token source.
func (s *issueSettings) check(insecure string) error {
	var err error
	if s.insecure, err = strconv.ParseBool(insecure); err != nil {
		return errors.New("invalid issues-insecure setting")
	}
	if s.tokenFile != "" && s.tokenEnv != "" {
		return errors.New("choose one issues token source")
	}
	if s.tokenFile == "" && s.tokenEnv == "" {
		s.tokenEnv = "FORGE_SEARCH_ISSUES_TOKEN"
	}
	return nil
}

// connect reads the issues token and dials forge-issues. Without an address
// it returns nils. The caller closes the connection.
func (s issueSettings) connect(ctx context.Context) (*api.RemoteIssues, *grpc.ClientConn, error) {
	if s.address == "" {
		return nil, nil, nil
	}

	token, err := servicekit.BearerToken(ctx, "issues", s.tokenFile, s.tokenEnv)
	if err != nil {
		return nil, nil, err
	}
	conn, err := dial("issues", s.address, s.insecure)
	if err != nil {
		return nil, nil, err
	}
	return api.NewRemoteIssues(issuespb.NewIssuesServiceClient(conn), token), conn, nil
}
