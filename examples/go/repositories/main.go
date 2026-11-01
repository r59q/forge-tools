// Command repositories lists repositories from every forge connected through
// forge-connector, using the forge-repositories library.
//
// The connector supplies one ready session per connection the client token
// in FORGE_TOKEN may use; this program never handles forge credentials:
//
//	FORGE_TOKEN=$CLIENT_TOKEN go run ./repositories
//	FORGE_TOKEN=$CLIENT_TOKEN go run ./repositories -owner mrbean
//	FORGE_TOKEN=$CLIENT_TOKEN go run ./repositories -refs owner/name
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/r59q/forge-tools/forge-connector/library/connector"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-repositories/library/repositories"
)

// options configure run.
type options struct {
	// Proxy is the forge-connector proxy URL.
	Proxy string
	// Source, if set, reads only this connection ID.
	Source string
	// Owner filters the listing, across every connection.
	Owner string
	// Refs, if set, prints the branches and tags of this repository path
	// instead of listing repositories.
	Refs string
}

func main() {
	var opts options
	flag.StringVar(&opts.Proxy, "proxy", "http://127.0.0.1:8081", "forge-connector proxy URL")
	flag.StringVar(&opts.Source, "source", "", "read only this connection ID")
	flag.StringVar(&opts.Owner, "owner", "", "list only this user, organization or group")
	flag.StringVar(&opts.Refs, "refs", "", "print branches and tags of this repository path")
	tokenEnv := flag.String("token-env", "FORGE_TOKEN", "variable holding the connector client token")
	flag.Parse()
	if err := run(context.Background(), opts, forgeauth.EnvToken(*tokenEnv), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options, creds forgeauth.Credentials, out io.Writer) error {
	conn, err := connector.New(connector.Config{URL: opts.Proxy, Credentials: creds})
	if err != nil {
		return err
	}
	conns, err := conn.Connections(ctx)
	if err != nil {
		return err
	}
	// Each connection is a source; the library picks the API from its kind.
	sources := make([]repositories.Source, len(conns))
	for i, c := range conns {
		sources[i] = repositories.Source{ID: string(c.ID), Session: c.Session}
	}
	c, err := repositories.New(sources...)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	defer w.Flush()
	if opts.Refs != "" {
		return printRefs(ctx, c, repositories.Ref{Source: opts.Source, Path: opts.Refs}, w)
	}
	repos, err := c.List(ctx, repositories.ListOptions{Owner: opts.Owner, Source: opts.Source})
	if err != nil {
		return err
	}
	for _, r := range repos {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Source.Kind, r.Path, r.Visibility, r.WebURL)
	}
	return nil
}

// printRefs prints the branches and tags of the repository ref names.
// Without a source, the path must exist on exactly one connection.
func printRefs(ctx context.Context, c *repositories.Client, ref repositories.Ref, w io.Writer) error {
	repo, err := c.Get(ctx, ref)
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
