# Full-stack example

Runs the whole stack with Docker Compose: the
[forge-connector](../../forge-connector/) broker and proxy, the
[repositories service](../../forge-repositories/service/), the
[issues service](../../forge-issues/service/), the
[checkouts service](../../forge-checkouts/service/), the
[actions service](../../forge-actions/service/), the
[explorer service](../../forge-explorer/service/), the
[pipeprint service](../../forge-pipeprint/service/), the
[reactor service](../../forge-reactor/service/) with RabbitMQ and the
[management interface](../../forge-tools-management/). You connect forges in
the browser.

It needs Docker Compose and `openssl`. Run all commands from this folder.

## Start

```sh
./init-env.sh
docker compose up --build -d
```

For development, [scripts/fullstack.sh](../../scripts/fullstack.sh) does the
same from anywhere: `start`, `stop`, or `clear` to also delete all data.
[scripts/integration-test.sh](../../scripts/integration-test.sh) tests the
running stack with a forge token in `FORGE_TEST_TOKEN`; see the script for
options.

`init-env.sh` creates `.env` with a random value for every secret. On an
existing `.env` it adds only the missing secrets, so run it again after
updating this example. It never changes existing values.

| Secret                               | Used for                                                         |
| ------------------------------------ | ---------------------------------------------------------------- |
| `FORGE_BROKER_KEY`                   | Encrypts stored forge tokens. Back it up; losing it loses them.  |
| `FORGE_BROKER_ADMIN_TOKEN`           | Lets management administer the broker.                           |
| `FORGE_MANAGEMENT_TOKEN`             | Your password for the management interface.                      |
| `FORGE_PROXY_TOKEN`                  | Lets the proxy fetch forge tokens from the broker.               |
| `FORGE_REPOSITORIES_CONNECTOR_TOKEN` | Lets the repositories service use connections through the proxy. |
| `FORGE_REPOSITORIES_TOKEN`           | Lets management call the repositories service.                   |
| `FORGE_ISSUES_CONNECTOR_TOKEN`       | Lets the issues service use connections through the proxy.       |
| `FORGE_ISSUES_TOKEN`                 | Lets management call the issues service.                         |
| `FORGE_CHECKOUTS_CONNECTOR_TOKEN`    | Lets checkouts fetch repositories through the proxy.             |
| `FORGE_CHECKOUTS_TOKEN`              | Authenticates checkout REST/gRPC callers.                        |
| `FORGE_ACTIONS_CONNECTOR_TOKEN`      | Lets actions read pipelines through the proxy.                   |
| `FORGE_EXPLORER_TOKEN`              | Authenticates local explorer REST/gRPC callers.                   |
| `FORGE_ACTIONS_TOKEN`                | Authenticates actions REST/gRPC callers and management.          |
| `FORGE_PIPEPRINT_TOKEN`              | Authenticates pipeprint REST/gRPC callers.                       |
| `FORGE_REACTOR_TOKEN`                | Authenticates reactor REST/gRPC callers and management.          |
| `FORGE_REACTOR_CONNECTOR_TOKEN`      | Lets the reactor poll branches and register webhooks.            |
| `FORGE_REACTOR_RABBITMQ_PASSWORD`    | RabbitMQ password of user `reactor`, for the reactor and you.    |
| `FORGE_REACTOR_WEBHOOK_SECRET`       | Signs webhooks for the example source in `reactor.json`.         |

At startup, management registers the proxy, repositories, issues, checkouts, actions and reactor
tokens in the broker. To rotate any secret except `FORGE_BROKER_KEY` and
`FORGE_REACTOR_RABBITMQ_PASSWORD`, change it in `.env` and run
`docker compose up -d` again. The RabbitMQ password is set only when its volume
is created; see [Forge events](#forge-events).

## Connect a forge

1. Open `http://127.0.0.1:8083/` and sign in as `operator`, with
   `FORGE_MANAGEMENT_TOKEN` from `.env` as the password.
2. Go to **Connections** and add a forge account: choose the forge, enter an
   access token with read access to your repositories, issues and pipeline data, and
   optionally a host for self-hosted forges, such as `gitlab.example.com`.
   The token is checked with the forge, stored encrypted and never shown
   again.
3. Go to **Repositories**. It lists the repositories of every connection.
4. Go to **Issues**. It lists the open issues assigned to the connected
   accounts. Filter by state, or show the issues they created. On
   **Repositories**, **Issues** under a repository lists that repository's
   issues.

5. On **Repositories**, choose **Pipelines** to browse executions, jobs and logs.

Add more accounts or forges the same way. Use **Test** to recheck a token,
**Replace token** to rotate it and **Remove** to delete a connection. Changes
apply right away, without restarts.

For a GitHub token, open *Settings → Developer settings → Personal access
tokens*. GitLab and Forgejo have personal access tokens in user settings.

To skip the manual steps, set `GITHUB_TOKEN` or `FORGEJO_TOKEN` before
`scripts/fullstack.sh start`. It adds connections named `github` and
`forgejo` if they are missing. Set `FORGEJO_HOSTNAME` for a Forgejo other than
`codeberg.org`. This needs `curl` and `jq`.

## Notes

- Management, checkouts, actions, explorer, pipeprint, the reactor and RabbitMQ are reachable from the host, on `127.0.0.1`. The other
  services talk over the Compose network in plaintext; use TLS endpoints
  for remote access.
- All operators see what the connected accounts can see. Management grants
  no forge permissions.
- The same features are available over REST and gRPC, with
  `Authorization: Bearer <management token>`; see the
  [service API reference](../../forge-tools-management/service/README.md#api).

## Working copies

The checkout API listens on `127.0.0.1:8088` (REST) and `127.0.0.1:9098`
(gRPC). Send `Authorization: Bearer <FORGE_CHECKOUTS_TOKEN>` and create with
`{"repository":{"source":"connection-id","path":"owner/repo"},"reference":"main"}`.
See its [API and storage configuration](../../forge-checkouts/service/README.md).

The `checkouts-data` volume preserves metadata, Git caches and checked-out code.
Returned paths such as `/data/copies/<id>` refer to the checkouts container.
An execution system must mount/access the same working files at the same paths.
There is no execution endpoint or isolation inside this service.

Existing `forge-workspaces` data can be reused by setting
`FORGE_CHECKOUTS_DATA_VOLUME` to the old volume name and renaming the token keys
in `.env`; see the [upgrade instructions](../../forge-checkouts/service/README.md#upgrade-from-forge-workspaces).

## Clean up

`down -v` deletes the broker tokens and all checkout files, including retained
checkouts and uncommitted work. Remove checkouts through their API first to
apply cleanup protections:

```sh
docker compose down -v
rm .env
```

Close the browser session to clear its sign-in.

## Pipelines

The actions API listens on `127.0.0.1:8089` (REST) and `127.0.0.1:9099`
(gRPC), authenticated with `FORGE_ACTIONS_TOKEN`. Management uses the internal
endpoint and adds Pipelines links to repository rows.

GitHub downloads redirect to object storage. The proxy's
`FORGE_PROXY_DOWNLOAD_HOSTS` defaults here to `*.blob.core.windows.net` and
`*.actions.githubusercontent.com`. Downloads to those HTTPS hosts carry no
forge or caller credentials. Set a comma-separated host list for other storage
providers; an explicitly empty value disables external downloads. Rerun
`init-env.sh` to add the two new actions tokens to an existing installation.

## Code exploration

Open `http://127.0.0.1:8090` and enter `FORGE_EXPLORER_TOKEN`. gRPC listens on
`127.0.0.1:9100`. The default root is the committed `explorer-content` sample.
Set `FORGE_EXPLORER_ROOT=/absolute/worktree` in `.env` and recreate the explorer
container to browse a real root, mounted read-only at `/content` as source `local`.
Ignored and untracked files are included; choose a root whose entire contents
callers may read. The explorer has no forge credentials and does not create or
remove checkouts. See its [configuration and limits](../../forge-explorer/README.md).

## Pipeline compiling

The pipeprint API listens on `127.0.0.1:8092` (REST) and `127.0.0.1:9102`
(gRPC), authenticated with `FORGE_PIPEPRINT_TOKEN`. It converts pipeline files
between GitHub, Forgejo, GitLab and [Pipeprint](../../forge-pipeprint/), and
returns job graphs. It reads only the request and holds no forge credentials.

```sh
jq -n --rawfile c .gitlab-ci.yml '{content: $c, to: "github"}' |
  curl -fsS -H "Authorization: Bearer $FORGE_PIPEPRINT_TOKEN" \
    http://127.0.0.1:8092/v1/pipeprint/convert -d @-
```

## Forge events

The reactor API listens on `127.0.0.1:8093` (REST) and `127.0.0.1:9103`
(gRPC), authenticated with `FORGE_REACTOR_TOKEN`. It turns forge webhooks and
branch polls into events, and delivers each to the RabbitMQ queue
`forge-reactor.<subscription>` of every matching subscription. Consumers
acknowledge there; the reactor holds no business logic. See the
[tool README](../../forge-reactor/README.md#events) for the event types.

[reactor.json](reactor.json) configures one GitHub webhook source, `example`,
signed with `FORGE_REACTOR_WEBHOOK_SECRET`, and no polls. To use your own,
set `FORGE_REACTOR_CONFIG_FILE` in `.env` to its path and recreate the reactor
container. Set each webhook `connection` and poll `connection` to a
connection ID from management; polls read Forgejo branches through the proxy.
Forges reach webhooks at `POST /webhooks/<source>`, so expose only that path.

Add more webhook sources on the **Webhooks** page in management. The reactor
generates each secret and shows it once. To have it create the webhook on the
forge, set `FORGE_REACTOR_PUBLIC_URL` in `.env` to the reactor's address as
forges reach it, such as `https://example.org/reactor`, and use a connection
whose token may manage repository webhooks. `reactor-data` keeps these
sources and their secrets. The **Events** page shows what the reactor
received.

```sh
jq -n '{id: "deploy", filter: {types: ["forge.branch.updated"]}}' |
  curl -fsS -H "Authorization: Bearer $FORGE_REACTOR_TOKEN" \
    http://127.0.0.1:8093/v1/subscriptions -d @-
```

Consume the queue with any AMQP 0-9-1 client at
`amqp://reactor:<FORGE_REACTOR_RABBITMQ_PASSWORD>@127.0.0.1:5672/`. The
RabbitMQ management interface is at `http://127.0.0.1:15672`, signed in as
`reactor`. The `rabbitmq-data` volume keeps queues and messages, and
`reactor-data` keeps subscriptions, delivery history, poll baselines and
managed webhook sources.

RabbitMQ sets the password only when `rabbitmq-data` is created. To rotate it,
change `.env`, then run:

```sh
docker compose exec rabbitmq rabbitmqctl change_password reactor "<new password>"
docker compose up -d
```
