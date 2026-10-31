package forgeauth_test

import (
	"context"
	"fmt"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/forgejo"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
)

func ExampleRegistry() {
	var reg forgeauth.Registry
	reg.Register(github.Provider{})
	reg.Register(gitlab.Provider{})
	reg.Register(forgejo.Provider{})

	ctx := context.Background()
	s, err := reg.Open(ctx, forgeauth.Connection{
		Kind:    forgeauth.GitLab,
		BaseURL: "https://gitlab.com/api/v4",
	}, forgeauth.EnvToken("GITLAB_TOKEN"))
	if err != nil {
		fmt.Println(err)
		return
	}
	me, err := reg.Whoami(ctx, s)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(me.Login)
}
