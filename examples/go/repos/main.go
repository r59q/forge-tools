// Command repos lists the authenticated user's repositories on a forge.
//
// The same code talks to a forge directly or through forge-connector-proxy;
// only the base URL and the token change:
//
//	FORGE_TOKEN=$GITHUB_TOKEN go run ./repos
//	FORGE_TOKEN=$CLIENT_TOKEN go run ./repos -url http://127.0.0.1:8081/c/<connection-id>
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgehttp"
)

// repoPaths is the first page of the user's repositories on each forge.
var repoPaths = map[forgeauth.Kind]string{
	forgeauth.GitHub:  "/user/repos?per_page=100",
	forgeauth.GitLab:  "/projects?membership=true&per_page=100",
	forgeauth.Forgejo: "/user/repos?limit=50",
}

func main() {
	kind := flag.String("kind", "github", "forge kind: github, gitlab or forgejo")
	baseURL := flag.String("url", "", "API base URL or proxy connection URL (default: api.github.com)")
	tokenEnv := flag.String("token-env", "FORGE_TOKEN", "variable holding the token")
	flag.Parse()
	conn := forgeauth.Connection{Kind: forgeauth.Kind(*kind), BaseURL: *baseURL}
	if err := run(context.Background(), conn, forgeauth.EnvToken(*tokenEnv), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, conn forgeauth.Connection, creds forgeauth.Credentials, out io.Writer) error {
	var reg forgeauth.Registry
	for _, p := range []forgeauth.Provider{github.Provider{}, gitlab.Provider{}, forgejo.Provider{}} {
		if err := reg.Register(p); err != nil {
			return err
		}
	}
	s, err := reg.Open(ctx, conn, creds)
	if err != nil {
		return err
	}
	me, err := reg.Whoami(ctx, s)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Signed in as %s\n", me.Login)

	// Follow Link headers; through the proxy they point back at the proxy.
	for next := repoPaths[s.Kind]; next != ""; {
		req, err := s.NewRequest(ctx, http.MethodGet, next, nil)
		if err != nil {
			return err
		}
		resp, err := s.Client.Do(req)
		if err != nil {
			return err
		}
		var repos []struct {
			FullName string `json:"full_name"`           // GitHub, Forgejo
			Path     string `json:"path_with_namespace"` // GitLab
		}
		err = forgehttp.CheckResponse(resp)
		if err == nil {
			err = json.NewDecoder(resp.Body).Decode(&repos)
		}
		resp.Body.Close()
		if err != nil {
			return err
		}
		for _, r := range repos {
			fmt.Fprintln(out, r.FullName+r.Path)
		}
		next, _ = forgehttp.NextPage(resp.Header)
	}
	return nil
}
