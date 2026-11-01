# Examples

Run the forge-connector services with Docker Compose and use them with
`curl` or Go.

```text
docker-compose.yml   broker and proxy from ghcr.io
.env.example         secrets for docker-compose.yml
go/                  Go programs using the libraries and the broker API
management/          management UI alone or with a repositories service
```

## Run the services

For a read-only repository web interface, see the
[management setup example](management/). It can run without the broker or proxy.

[docker-compose.yml](docker-compose.yml) runs the published
[broker](../forge-connector/broker/) and [proxy](../forge-connector/proxy/)
images. Ports are bound to localhost. The steps below need `curl`, `jq`,
`openssl` and a forge token, such as a GitHub personal access token.

1. Create the secrets and start the broker:

   ```sh
   cp .env.example .env
   sed -i "s|^FORGE_BROKER_KEY=.*|FORGE_BROKER_KEY=$(openssl rand -base64 32)|" .env
   sed -i "s|^FORGE_BROKER_ADMIN_TOKEN=.*|FORGE_BROKER_ADMIN_TOKEN=$(openssl rand -hex 32)|" .env
   docker compose up -d

   . ./.env
   broker() { curl -fsS -H "Authorization: Bearer $FORGE_BROKER_ADMIN_TOKEN" "$@"; }
   ```

2. Set `GITHUB_TOKEN` to a GitHub personal access token. Create one under
   *Settings → Developer settings → Personal access tokens*; read access to
   your repositories is enough. Enter it at the prompt to keep it out of your
   shell history:

   ```sh
   read -rs GITHUB_TOKEN && export GITHUB_TOKEN
   ```

   Register the forge and store the connection. The broker checks the token
   with GitHub before saving it, and never returns it to the admin. A `400`
   means the token is empty or GitHub rejected it:

   ```sh
   : "${GITHUB_TOKEN:?set GITHUB_TOKEN first}"
   FORGE=$(broker localhost:8080/v1/forges \
     -d '{"kind":"github","baseUrl":"https://api.github.com","name":"GitHub"}' | jq -r .id)
   CONN=$(broker localhost:8080/v1/connections \
     -d "{\"forgeId\":\"$FORGE\",\"name\":\"me\",\"credential\":\"$GITHUB_TOKEN\"}" | jq -r .id)
   ```

3. Create a client for the proxy and start it. Client scopes cannot change
   later, so list every connection the proxy should serve. The token is
   returned once:

   ```sh
   PROXY_TOKEN=$(broker localhost:8080/v1/clients \
     -d "{\"name\":\"proxy\",\"connectionIds\":[\"$CONN\"],\"canIssueTokens\":true}" | jq -r .token)
   sed -i "s|^FORGE_PROXY_TOKEN=.*|FORGE_PROXY_TOKEN=$PROXY_TOKEN|" .env
   docker compose --profile proxy up -d
   curl --retry 5 --retry-all-errors --retry-delay 1 localhost:8081/healthz
   ```

4. Create a client for your app. It may use the connection through the proxy
   but cannot read the forge token:

   ```sh
   CLIENT_TOKEN=$(broker localhost:8080/v1/clients \
     -d "{\"name\":\"my-app\",\"connectionIds\":[\"$CONN\"]}" | jq -r .token)
   ```

## Call a forge through the proxy

Send normal forge API requests to `/c/{connectionID}/` with the client token
as `Authorization: Bearer <token>`. This works for every forge; the proxy
swaps in the real forge token and the forge's own auth header:

```sh
curl -H "Authorization: Bearer $CLIENT_TOKEN" localhost:8081/c/$CONN/user
curl -i -H "Authorization: Bearer $CLIENT_TOKEN" "localhost:8081/c/$CONN/user/repos?per_page=5"
```

`Link` headers in responses point back at the proxy.

## Manage the broker

```sh
broker localhost:8080/v1/connections                        # list
broker -X POST localhost:8080/v1/connections/$CONN/test -d '{}'  # recheck with the forge
read -rs NEW_GITHUB_TOKEN                                   # enter the new token
broker localhost:8080/v1/connections/$CONN/credential \
  -d "{\"credential\":\"$NEW_GITHUB_TOKEN\"}"               # rotate the forge token
broker localhost:8080/v1/clients                            # list clients
broker -X DELETE localhost:8080/v1/clients/<client-id>      # revoke a client
```

All admin calls are also available over gRPC on port 9090; see the
[API reference](../forge-connector/broker/README.md#api).

## Go

[go/](go/) is a Go module with three small programs. From `go/`:

```sh
# List your repositories directly from GitHub...
FORGE_TOKEN=$GITHUB_TOKEN go run ./repos
# ...or through the proxy. Only the URL and token change.
FORGE_TOKEN=$CLIENT_TOKEN go run ./repos -url http://127.0.0.1:8081/c/$CONN

# List repositories with the forge-repositories library through the proxy,
# then show one repository's branches and tags.
FORGE_TOKEN=$CLIENT_TOKEN go run ./repositories -source github:$CONN
FORGE_TOKEN=$CLIENT_TOKEN go run ./repositories -source github:$CONN -refs <owner>/<name>

# List forges, connections and clients over the broker's gRPC API.
go run ./broker-admin -addr 127.0.0.1:9090
```

- [repos](go/repos/main.go) opens a `forgeauth` session, calls `Whoami` and
  follows `Link` pagination with `forgehttp`. Use `-kind gitlab` or
  `-kind forgejo` with `-url` for other forges.
- [repositories](go/repositories/main.go) opens one session per
  `-source kind:connectionID` against the proxy and reads them all through one
  [forge-repositories](../forge-repositories/library/) client. Repeat
  `-source` to combine forges, for example a GitHub and a GitLab connection,
  and add `-owner <name>` to list one owner's repositories across all of them.
  The client token must be scoped to every listed connection. Its test runs
  the real proxy handler in front of fake GitHub and GitLab APIs.
- [broker-admin](go/broker-admin/main.go) calls the broker with the generated
  `brokerpb` client, sending the admin token as `authorization` metadata.

Test them with `go vet ./...` and `go test ./...`.

## Clean up

```sh
docker compose --profile proxy down -v   # -v also deletes the broker database
rm .env
```
