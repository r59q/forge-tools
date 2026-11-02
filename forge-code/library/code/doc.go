// Package code reads and writes Git repository content on GitHub, GitLab
// and Forgejo through one forge-agnostic API.
//
// A Client combines several sources, such as forge-connector connections.
// Each source is a forgeauth session for the forge's REST API and,
// optionally, a forgeauth.GitRemote for pushes. Reads use the REST API;
// writes push with the Git protocol, so ref updates are compare-and-swap.
//
//	c, err := code.New(code.Source{ID: "github", Session: gh})
//	repo := code.RepositoryRef{Source: "github", Repository: "mrbean/teddy"}
//	r, err := c.ReadFile(ctx, repo, "main", "README.md")
//	defer r.Close()
//
// Revisions use one grammar on every forge: a full object ID, HEAD,
// refs/heads/X, refs/tags/X or a bare name, each optionally followed by
// ~N or ^N. Every read resolves its revision once and then reads only by
// object ID, and its result names the commit it read.
//
// A repository is only a path, such as "owner/name" or the nested GitLab
// path "group/sub/project". Nothing is cached between calls.
package code
