# forge-repositories

Reads repositories, branches and tags from GitHub, GitLab and Forgejo through
one shared model.

A client combines several sources, such as one person's GitHub and GitLab
accounts, and answers across all of them: listing the repositories of owner
`mrbean` returns matches from every source. Each result says which source,
forge kind and forge host it came from. Sources connect to a forge directly or
through a [forge-connector](../forge-connector/) proxy connection; the tool
does not depend on forge-connector itself.

```text
library/          Go module: shared model and forge adapters
```

It reads repository metadata, branches with the default marked, and tags,
each with its commit SHA. It can update a repository's description and
default branch. It never creates, forks or deletes repositories, branches or
tags.

Build and test each module from its own directory; see its README for setup.

## License

[MIT](../LICENSE)
