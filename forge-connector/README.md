# forge-connector

Stores forge connections and credentials for reuse across requests. A single
owner manages connections and grants services access through scoped clients.

```text
library/          Go module with core features
  broker/         connections, sealed credentials and client authorization
```

The [library](library/) is available now. The standalone broker service,
authenticating proxy service, library proxy package and generated broker API
will follow in later issues. The broker will be deployable on its own.

Build and test from `library/`; see its README for setup and API behavior.
