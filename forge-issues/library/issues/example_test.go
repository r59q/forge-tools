package issues_test

import (
	"context"
	"fmt"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/gitlab"
	"github.com/r59q/forge-tools/forge-issues/library/issues"
)

func ExampleClient_List() {
	var reg forgeauth.Registry
	reg.Register(github.Provider{})
	reg.Register(gitlab.Provider{})

	// Each source is one forge-connector proxy connection. The caller states
	// its kind; the proxy client token comes from FORGE_TOKEN.
	ctx := context.Background()
	open := func(kind forgeauth.Kind, connection string) issues.Source {
		s, err := reg.Open(ctx, forgeauth.Connection{
			Kind:    kind,
			BaseURL: "http://127.0.0.1:8081/c/" + connection,
		}, forgeauth.EnvToken("FORGE_TOKEN"))
		if err != nil {
			panic(err)
		}
		return issues.Source{ID: connection, Session: s}
	}
	c, err := issues.New(
		open(forgeauth.GitHub, "github-mrbean"),
		open(forgeauth.GitLab, "gitlab-mrbean"),
	)
	if err != nil {
		fmt.Println(err)
		return
	}

	// Open issues assigned to mrbean on both forges, labelled bug.
	list, err := c.List(ctx, issues.ListOptions{Labels: []string{"bug"}})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, i := range list {
		fmt.Println(i.Source.Kind, i.Repository, i.Number, i.Title)
	}
}

func ExampleClient_Update() {
	var c *issues.Client // from issues.New
	ctx := context.Background()

	closed := issues.Closed
	ref := issues.Ref{Source: "github-mrbean", Repository: "mrbean/teddy", Number: 7}
	if _, err := c.AddComment(ctx, ref, "Found him."); err != nil {
		fmt.Println(err)
		return
	}
	// Close the issue and clear its assignees; the labels stay.
	iss, err := c.Update(ctx, ref, issues.Update{State: &closed, Assignees: &[]string{}})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(iss.State, iss.WebURL)
}
