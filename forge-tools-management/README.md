# forge-tools-management

A web interface for setting up and inspecting forge-tools. Operators connect
forge accounts in the browser, and see the repositories and issues of every
connection.

- **Connections** adds, tests, rotates and removes forge connections in
  [forge-connector](../forge-connector/). Tokens are checked with the forge,
  stored encrypted by the connector and never shown again.
- **Repositories** lists repositories through
  [forge-repositories](../forge-repositories/), with full paths, source IDs,
  forge kinds and visibility. Identical paths on different sources remain
  separate.
- **Issues** lists issues through [forge-issues](../forge-issues/): those
  assigned to or created by the connected accounts, or one repository's
  issues, filtered by state.

- **Pipelines** browses repository executions, jobs, steps and logs through
  [forge-actions](../forge-actions/). The capability is optional.
- **Webhooks** adds, rotates and removes webhook sources in
  [forge-reactor](../forge-reactor/). The reactor generates each secret, shown
  once, and can create the webhook on the forge. Sources from the reactor's
  configuration file are listed read-only. The capability is optional.

Projects:

- [library](library/) holds connection and webhook management, repository
  and issue listing, and service client registration.
- [service](service/) provides the web interface, REST and gRPC. At startup
  it registers the proxy, repositories and issues service tokens in the
  connector, so every token comes from one configuration file.
- [Setup example](../examples/fullstack/) runs the whole stack from a
  generated `.env`.

Each capability is optional; management starts with none. Failed listings
never appear as empty or complete results.
