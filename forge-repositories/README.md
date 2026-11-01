# forge-repositories

Makes the repositories, branches and tags of every forge connected through
[forge-connector](../forge-connector/) available through one shared model.
GitHub, GitLab and Forgejo are supported.

The connector stores credentials and handles authentication. This tool only
needs a connector client token: each connection the token may use becomes a
source. Listing the repositories of owner `mrbean` returns matches from every
source, and each result says which connection, forge kind and forge host it
came from.

```text
library/          Go module: shared model and forge adapters
service/          REST and gRPC service over forge-connector connections
```

It reads repository metadata, branches with the default marked, and tags,
each with its commit SHA. It can update a repository's description and
default branch. It never creates, forks or deletes repositories, branches or
tags.

The service gets its connections from the connector and passes each session
to the library. The library only works with sessions; it never sees tokens or
connection details.

Build and test each module from its own directory; see its README for setup.

## License

[MIT](../LICENSE)
