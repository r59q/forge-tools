// Command repositories lists repositories from several forge-connector
// proxy connections at once, using the forge-repositories library.
//
// Each -source is kind:connectionID. The client token in FORGE_TOKEN must be
// scoped to every listed connection:
//
//	FORGE_TOKEN=$CLIENT_TOKEN go run ./repositories -source github:$CONN
//	FORGE_TOKEN=$CLIENT_TOKEN go run ./repositories -source github:$CONN \
//	  -source gitlab:$GITLAB_CONN -owner mrbean
//	FORGE_TOKEN=$CLIENT_TOKEN go run ./repositories -source github:$CONN -refs owner/name
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-repositories/library/repositories"
)

// sourceFlags collects repeated -source kind:connectionID flags.
type sourceFlags []string

func (s *sourceFlags) String() string     { return strings.Join(*s, ",") }
func (s *sourceFlags) Set(v string) error { *s = append(*s, v); return nil }

// options configure run.
type options struct {
	// Proxy is the forge-connector proxy URL.
	Proxy string
	// Sources are kind:connectionID pairs.
	Sources []string
	// Owner filters the listing, across every source.
	Owner string
	// Refs, if set, prints the branches and tags of this repository path
	// instead of listing repositories.
	Refs string
}

func main() {
	var opts options
	var sources sourceFlags
	flag.StringVar(&opts.Proxy, "proxy", "http://127.0.0.1:8081", "forge-connector proxy URL")
	flag.Var(&sources, "source", "kind:connectionID, such as github:<id> (repeatable)")
	flag.StringVar(&opts.Owner, "owner", "", "list only this user, organization or group")
	flag.StringVar(&opts.Refs, "refs", "", "print branches and tags of this repository path")
	tokenEnv := flag.String("token-env", "FORGE_TOKEN", "variable holding the proxy client token")
	flag.Parse()
	opts.Sources = sources
	if err := run(context.Background(), opts, forgeauth.EnvToken(*tokenEnv), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options, creds forgeauth.Credentials, out io.Writer) error {
	if len(opts.Sources) == 0 {
		return errors.New("at least one -source kind:connectionID is required")
	}
	var reg forgeauth.Registry
	for _, p := range []forgeauth.Provider{github.Provider{}, gitlab.Provider{}, forgejo.Provider{}} {
		if err := reg.Register(p); err != nil {
			return err
		}
	}

	// Every source is a normal forgeauth session whose base URL is a proxy
	// connection. The caller states the kind; the library picks the API.
	var sources []repositories.Source
	for _, s := range opts.Sources {
		kind, id, ok := strings.Cut(s, ":")
		if !ok || id == "" {
			return fmt.Errorf("source %q: want kind:connectionID", s)
		}
		session, err := reg.Open(ctx, forgeauth.Connection{
			Kind:    forgeauth.Kind(kind),
			BaseURL: strings.TrimRight(opts.Proxy, "/") + "/c/" + id,
		}, creds)
		if err != nil {
			return err
		}
		sources = append(sources, repositories.Source{ID: id, Session: session})
	}
	c, err := repositories.New(sources...)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	defer w.Flush()
	if opts.Refs != "" {
		return printRefs(ctx, c, opts.Refs, w)
	}
	repos, err := c.List(ctx, repositories.ListOptions{Owner: opts.Owner})
	if err != nil {
		return err
	}
	for _, r := range repos {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Source.Kind, r.Path, r.Visibility, r.WebURL)
	}
	return nil
}

// printRefs prints the branches and tags of the repository at path. The
// path must exist on exactly one source.
func printRefs(ctx context.Context, c *repositories.Client, path string, w io.Writer) error {
	repo, err := c.Get(ctx, repositories.Ref{Path: path})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%s\t%s\t%s\n", repo.Source.Kind, repo.Path, repo.WebURL)
	branches, err := c.Branches(ctx, repo.Ref())
	if err != nil {
		return err
	}
	for _, b := range branches {
		mark := ""
		if b.Default {
			mark = "default"
		}
		fmt.Fprintf(w, "branch\t%s\t%s\t%s\n", b.Name, b.SHA, mark)
	}
	tags, err := c.Tags(ctx, repo.Ref())
	if err != nil {
		return err
	}
	for _, t := range tags {
		fmt.Fprintf(w, "tag\t%s\t%s\t\n", t.Name, t.SHA)
	}
	return nil
}
