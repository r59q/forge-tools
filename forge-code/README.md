# forge-code

Makes the Git content of every forge connected through
[forge-connector](../forge-connector/) available through one shared model.
GitHub, GitLab and Forgejo are supported.

The connector stores credentials and handles authentication. This tool only
needs a connector client token: each connection the token may use becomes a
source. Reads use the forge REST APIs through the connector; writes push with
the Git protocol through the connector's Git route, which the proxy opens for
pushes only with `--allow-git-push`.

```text
library/          Go module: shared model and forge adapters
service/          REST and gRPC service over forge-connector connections
```

## Concepts

- **Source**: one forge connection.
- **Revision**: an object ID, `HEAD`, a full ref name or a bare branch or tag
  name, with optional `~N` and `^N` suffixes. It means the same on every
  forge.
- **Ref**: a branch or tag, with the commit it points to.
- **Tree** and **Entry**: a directory listing with Git modes, so symlinks,
  executables and submodules show.
- **Blob**: file content, streamed with a size cap.
- **Diff**: the changes between two commits, computed by the library.

A repository is just a path, such as `owner/name` or a nested GitLab path
like `group/sub/project`. forge-code does not depend on
[forge-repositories](../forge-repositories/).

## Operations

- Resolve revisions, list refs, read commits and history.
- List trees, stat paths, read files and blobs.
- Find merge bases and compare commits.
- Create commits and update refs with compare-and-swap. Commits are
  unsigned, and pushes run the forge's hooks and CI like any `git push`.

Every read is pinned to one commit, and results say which. Nothing is
cached between calls.

Build and test each module from its own directory; see its README for setup.

## License

[MIT](../LICENSE)
