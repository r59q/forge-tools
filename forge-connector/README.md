# forge-connector

Stores forge connections and credentials for reuse across requests. A single
owner manages connections and grants services access through scoped clients.

```text
library/          Go module with core features
  broker/         connections, sealed credentials and client authorization
  brokerpb/       committed gRPC and REST bindings
  proto/          documented API source
broker/           standalone gRPC and REST service with SQLite persistence
```

The [library](library/) and standalone [broker service](broker/) are available.
The authenticating proxy and library proxy package will follow in later issues.

Build and test each module from its own directory; see its README for setup.
