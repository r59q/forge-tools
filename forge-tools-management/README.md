# forge-tools-management

A read-only web interface for operators inspecting forge-tools. The first
version lists repositories, with their full paths, source IDs, forge kinds
and visibility. Identical paths on different sources remain separate.

- [library](library/) holds listing behavior and optional capability state.
- [service](service/) provides the web interface, REST and gRPC. It connects
  to an existing forge-repositories service, which reads the forges
  connected through forge-connector and owns pagination and upstream access
  control.
- [Setup example](../examples/management/) starts management alone, then
  optionally connects repositories through forge-connector.

No other tool is required at startup. Failed listings never appear as empty
or complete results. Configuration editing, repository mutations, branch/tag
views and other administration are outside this version.
