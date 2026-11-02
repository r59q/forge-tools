package code_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/r59q/forge-tools/forge-code/library/code"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth/github"
)

// This example wires one source by hand, as forge-connector's
// connector.Client.Connections does for every connection: the REST
// session goes to the proxy's /c/{id} route and the Git remote to its
// /g/{id} route, both with the client token. It then reads a file,
// compares two branches and commits with compare-and-swap.
func Example() {
	ctx := context.Background()
	token := forgeauth.EnvToken("CONNECTOR_TOKEN")
	session, err := github.Provider{}.Open(ctx, forgeauth.Connection{
		Kind:    forgeauth.GitHub,
		BaseURL: "http://localhost:8081/c/my-connection",
	}, token)
	if err != nil {
		fmt.Println(err)
		return
	}
	git, err := forgeauth.NewGitRemote("http://localhost:8081/g/my-connection", token,
		func(h http.Header, t string) { h.Set("Authorization", "Bearer "+t) }, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	c, err := code.New(code.Source{ID: "my-connection", Session: session, Git: git})
	if err != nil {
		fmt.Println(err)
		return
	}
	repo := code.RepositoryRef{Source: "my-connection", Repository: "mrbean/teddy"}

	f, err := c.ReadFile(ctx, repo, "main", "README.md")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	fmt.Println("README.md is", f.ID, "at", f.Commit)

	d, err := c.Compare(ctx, repo, "main", "fix", code.CompareOptions{Patches: true})
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, ch := range d.Changes {
		fmt.Println(ch.Status, ch.Path)
	}

	main, err := c.Resolve(ctx, repo, "refs/heads/main")
	if err != nil {
		fmt.Println(err)
		return
	}
	commit, err := c.CreateCommit(ctx, repo, code.NewCommit{
		Parents: []code.OID{main.Commit},
		Changes: []code.FileChange{{Path: "README.md", Content: []byte("hello\n")}},
		Author:  code.Signature{Name: "Mr Bean", Email: "bean@example.com"},
		Message: "Update README",
		Ref:     "refs/heads/main",
		Old:     main.Commit,
	})
	if errors.Is(err, code.ErrStale) {
		fmt.Println("main moved; read it again and retry")
		return
	}
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("pushed", commit.ID)
}
