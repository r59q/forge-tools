# forge-issues

Makes the issues of every forge connected through
[forge-connector](../forge-connector/) available through one shared model.
GitHub, GitLab and Forgejo are supported.

The connector stores credentials and handles authentication. This tool only
needs a connector client token: each connection the token may use becomes a
source. Each issue says which connection, forge kind and forge host it came
from.

```text
library/          Go module: shared model and forge adapters
service/          REST and gRPC service over forge-connector connections
```

## Concepts

- **Source**: one forge connection.
- **Issue**: title, body, state (open or closed), author, assignees, labels,
  milestone, comment count and timestamps. Pull and merge requests are never
  returned.
- **Comment**: a user comment on an issue. GitLab system notes are left out.
- **Ref**: a source, a repository path and an issue number. The number is the
  one shown on the forge (GitLab's `iid`).

A repository is just a path, such as `owner/name` or a nested GitLab path
like `group/sub/project`. forge-issues does not depend on
[forge-repositories](../forge-repositories/).

## Operations

- List a repository's issues, or the issues assigned to or created by the
  account, filtered by state and labels. Without a source, every source is
  asked and the results are merged.
- Get an issue and list its comments.
- Create an issue, update its title, body, state, labels and assignees, and
  add comments. Writes always name a source.

It never deletes issues or comments.

The service gets its connections from the connector and passes each session
to the library. The library only works with sessions; it never sees tokens or
connection details.

[forge-tools-management](../forge-tools-management/) lists issues in its web
interface, and the [management example](../examples/management/) runs this
service with the rest of the stack.

Build and test each module from its own directory; see its README for setup.

## License

[MIT](../LICENSE)
