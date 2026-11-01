# forge-tools-management

A web interface for setting up and inspecting forge-tools. Operators connect
forge accounts in the browser, and see the repositories of every connection.

- **Connections** adds, tests, rotates and removes forge connections in
  [forge-connector](../forge-connector/). Tokens are checked with the forge,
  stored encrypted by the connector and never shown again.
- **Repositories** lists repositories through
  [forge-repositories](../forge-repositories/), with full paths, source IDs,
  forge kinds and visibility. Identical paths on different sources remain
  separate.

Projects:

- [library](library/) holds connection management, repository listing and
  service client registration.
- [service](service/) provides the web interface, REST and gRPC. At startup
  it registers the proxy and repositories service tokens in the connector, so
  every token comes from one configuration file.
- [Setup example](../examples/management/) runs the whole stack from a
  generated `.env`.

Each capability is optional; management starts with none. Failed listings
never appear as empty or complete results.
