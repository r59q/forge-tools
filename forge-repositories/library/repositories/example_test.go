package repositories_test

import (
	"context"
	"fmt"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-repositories/library/repositories"
)

func ExampleClient_List() {
	var reg forgeauth.Registry
	reg.Register(github.Provider{})
	reg.Register(gitlab.Provider{})

	// Each source is one forge-connector proxy connection. The caller states
	// its kind; the proxy client token comes from FORGE_TOKEN.
	ctx := context.Background()
	open := func(kind forgeauth.Kind, connection string) repositories.Source {
		s, err := reg.Open(ctx, forgeauth.Connection{
			Kind:    kind,
			BaseURL: "http://127.0.0.1:8081/c/" + connection,
		}, forgeauth.EnvToken("FORGE_TOKEN"))
		if err != nil {
			panic(err)
		}
		return repositories.Source{ID: connection, Session: s}
	}
	c, err := repositories.New(
		open(forgeauth.GitHub, "github-mrbean"),
		open(forgeauth.GitLab, "gitlab-mrbean"),
	)
	if err != nil {
		fmt.Println(err)
		return
	}

	repos, err := c.List(ctx, repositories.ListOptions{Owner: "mrbean"})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, r := range repos {
		fmt.Println(r.Source.Kind, r.Path, r.WebURL)
	}
}
