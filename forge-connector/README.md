# forge-connector

Connects tools to forges. It stores forge connections and credentials, and
handles authentication, so tools never hold forge tokens. A single owner
manages connections and grants tools access through scoped clients.

The connector knows nothing about tool domains such as repositories or
issues. A tool asks it for connections and gets ready sessions; the tool
chooses the forge API paths, the connector authenticates the requests.

```text
library/          Go module with core features
  broker/         connections, sealed credentials and client authorization
  brokerpb/       committed gRPC and REST bindings
  connector/      client that gives tools sessions for their connections
  proto/          documented API source
  proxy/          authenticating HTTP handler for forge APIs
broker/           standalone gRPC and REST service with SQLite persistence
proxy/            standalone HTTP service that serves the proxy handler
```

The [library](library/), the [broker service](broker/) and the
[proxy service](proxy/) are available.

- The broker stores connections and sealed credentials, and issues scoped client
  tokens. It is administered by one owner.
- The proxy serves the library handler over HTTP. Clients send requests to
  `/c/{connectionID}/...` with a client token, and list their connections at
  `GET /connections`. The proxy verifies the client and
  fetches the forge token from the broker over gRPC, then forwards the request,
  so callers never hold the forge credential.

Run the broker first, create a connection and a client for the proxy with
`CanIssueTokens`, then start the proxy with that client's token.

Build and test each module from its own directory; see its README for setup.
