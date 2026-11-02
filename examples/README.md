# Examples

Run the forge-connector services with Docker Compose and use them with
`curl` or Go.

```text
docker-compose.yml   broker, proxy and forge-code service from ghcr.io
.env.example         secrets for docker-compose.yml
go/                  Go programs using the libraries and the broker API
management/          full stack, with connections set up in the management UI,
                     including the repositories and issues services
```

## Run the services

The quickest start is the [management setup example](management/): it runs
the whole stack, and you connect forges in a web interface.

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

3. Create a client for the proxy and start it. `allConnections` grants it
   every connection, including ones added later. The token is returned once:

   ```sh
   PROXY_TOKEN=$(broker localhost:8080/v1/clients \
     -d '{"name":"proxy","allConnections":true,"canIssueTokens":true}' | jq -r .token)
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

## Read and write code

The [forge-code service](../forge-code/service/) reads trees, files and
diffs, and pushes commits, on every connection its client may use. Writes
push through the proxy's Git route, so turn pushes on, create a client for
the service and start it. The forge token needs write access to contents for
writes. The first line adds keys missing from a `.env` made from an older
`.env.example`:

```sh
for k in FORGE_PROXY_ALLOW_GIT_PUSH FORGE_CODE_TOKEN FORGE_CODE_CONNECTOR_TOKEN; do grep -q "^$k=" .env || echo "$k=" >> .env; done
sed -i "s|^FORGE_PROXY_ALLOW_GIT_PUSH=.*|FORGE_PROXY_ALLOW_GIT_PUSH=true|" .env
CODE_CLIENT=$(broker localhost:8080/v1/clients \
  -d "{\"name\":\"forge-code\",\"connectionIds\":[\"$CONN\"]}" | jq -r .token)
sed -i "s|^FORGE_CODE_CONNECTOR_TOKEN=.*|FORGE_CODE_CONNECTOR_TOKEN=$CODE_CLIENT|" .env
sed -i "s|^FORGE_CODE_TOKEN=.*|FORGE_CODE_TOKEN=$(openssl rand -hex 32)|" .env
docker compose --profile proxy --profile code up -d
. ./.env
fc() { curl -fsS -H "Authorization: Bearer $FORGE_CODE_TOKEN" "$@"; }
```

Set `REPO` to one of your repositories, such as `octocat/hello`. Every call
names the connection as `source`:

```sh
fc "localhost:8085/v1/tree?source=$CONN&repository=$REPO&revision=main"
fc -o README.md "localhost:8085/v1/raw?source=$CONN&repository=$REPO&path=README.md"
fc "localhost:8085/v1/compare?source=$CONN&repository=$REPO&base=main&head=my-branch&patches=true"
```

Commit to a new branch. `old` is the ref's expected value; 40 zeros means it
must not exist yet. If the ref moved, the call fails with `409` and changes
nothing. The commit is unsigned and runs the forge's hooks and CI like any
push:

```sh
MAIN=$(fc "localhost:8085/v1/resolve?source=$CONN&repository=$REPO&revision=main" | jq -r .commit)
fc -X POST localhost:8085/v1/commits -d "{\"source\":\"$CONN\",\"repository\":\"$REPO\",
  \"parents\":[\"$MAIN\"],\"ref\":\"refs/heads/forge-code-demo\",
  \"old\":\"0000000000000000000000000000000000000000\",\"message\":\"Try forge-code\",
  \"author\":{\"name\":\"Mr Bean\",\"email\":\"bean@example.com\"},
  \"changes\":[{\"path\":\"hello.txt\",\"content\":\"$(printf 'hello\n' | base64)\"}]}"
```

Delete the branch again with `UpdateRefs`, using the commit `id` from the
response as `old`:

```sh
fc -X POST localhost:8085/v1/ref-updates -d "{\"source\":\"$CONN\",\"repository\":\"$REPO\",
  \"updates\":[{\"name\":\"refs/heads/forge-code-demo\",\"old\":\"<id>\",
  \"new\":\"0000000000000000000000000000000000000000\"}]}"
```

See the [service API reference](../forge-code/service/README.md#api) for
all routes and error codes.

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

# List repositories of every connection the client may use, then show one
# repository's branches and tags.
FORGE_TOKEN=$CLIENT_TOKEN go run ./repositories
FORGE_TOKEN=$CLIENT_TOKEN go run ./repositories -refs <owner>/<name>

# List forges, connections and clients over the broker's gRPC API.
go run ./broker-admin -addr 127.0.0.1:9090
```

- [repos](go/repos/main.go) opens a `forgeauth` session, calls `Whoami` and
  follows `Link` pagination with `forgehttp`. Use `-kind gitlab` or
  `-kind forgejo` with `-url` for other forges.
- [repositories](go/repositories/main.go) asks the connector for every
  connection the client token may use and reads them all through one
  [forge-repositories](../forge-repositories/library/) client. Scope the
  client to several connections, such as GitHub and GitLab, and add
  `-owner <name>` to list one owner's repositories across all of them, or
  `-source <connectionID>` to read one. Its test runs the real proxy handler
  in front of fake GitHub and GitLab APIs.
- [broker-admin](go/broker-admin/main.go) calls the broker with the generated
  `brokerpb` client, sending the admin token as `authorization` metadata.

Test them with `go vet ./...` and `go test ./...`.

## Clean up

```sh
docker compose --profile proxy --profile code down -v   # -v also deletes the broker database
rm .env
```
