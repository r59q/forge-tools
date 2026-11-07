# Test environment

Runs the published `ghcr.io/r59q/*:latest` images on the test server with
Docker Compose: every service except the explorer, plus RabbitMQ for the
reactor. nginx serves each REST API at `/<service>/` on port 6080, behind
the server's TLS proxy for `ENVIRONMENT_URL_TEST`.

## TLS proxy

nginx joins the Docker network `proxy` as `forgetools`; the deploy creates
the network if missing. Add Nginx Proxy Manager to it, in its compose file:

```yaml
services:
  app:
    networks: [default, proxy]

networks:
  proxy:
    external: true
```

Then forward the proxy host of `ENVIRONMENT_URL_TEST` to
`http://forgetools:6080`. To use an existing network instead, such as Nginx
Proxy Manager's own, set `FORGE_PROXY_NETWORK` in the server's `.env`.

## Deploy

[deploy-test.yml](../../.github/workflows/deploy-test.yml) deploys after a
service image is published from `master`, after changes to this folder, and
on manual runs. It joins the server's WireGuard network, then runs
[deploy.sh](deploy.sh) over SSH. The script:

1. copies `compose.yaml`, `nginx.conf` and `init-env.sh` to
   `~/services/forgetools` on the server;
2. adds missing secrets to `.env` there, pulls the images and starts them;
3. adds `github`, `gitlab` and `forgejo` connections from the test PATs, if
   missing;
4. writes `reactor.json` with webhook sources for `github` and `forgejo` and
   a poll of the Forgejo test repository's `main`, using
   [reactor.jq](reactor.jq);
5. checks that every service answers through `ENVIRONMENT_URL_TEST`.

| Secret                  | Used for                              |
| ----------------------- | ------------------------------------- |
| `WG_CONFIG`             | wg-quick configuration of the runner. |
| `WG_IP_ADDR`            | Server address in WireGuard.          |
| `SSH_USER_TEST`         | SSH user; it needs Docker access.     |
| `SSH_PRIVATE_KEY_TEST`  | SSH key of that user.                 |
| `ENVIRONMENT_URL_TEST`  | Public URL of the TLS proxy.          |
| `*_TEST_REPO_PAT`       | Tokens of the test connections.       |
| `FJ_TEST_REPO_DOMAIN`   | Forgejo host of the test connection.  |

The server needs Docker Compose 2.20 or newer and `openssl`. Run the script
from your own machine with `DEPLOY_SSH` and `DEPLOY_URL`; see its header.

## Use

Service tokens are generated in `~/services/forgetools/.env` on the server.
The workflow reads only the management token, masked in its logs. Send them
as `Authorization: Bearer <token>`:

| Path             | Token                      |
| ---------------- | -------------------------- |
| `/management/`   | `FORGE_MANAGEMENT_TOKEN`   |
| `/repositories/` | `FORGE_REPOSITORIES_TOKEN` |
| `/issues/`       | `FORGE_ISSUES_TOKEN`       |
| `/actions/`      | `FORGE_ACTIONS_TOKEN`      |
| `/checkouts/`    | `FORGE_CHECKOUTS_TOKEN`    |
| `/code/`         | `FORGE_CODE_TOKEN`         |
| `/changes/`      | `FORGE_CHANGES_TOKEN`      |
| `/review/`       | `FORGE_REVIEW_TOKEN`       |
| `/pipeprint/`    | `FORGE_PIPEPRINT_TOKEN`    |
| `/reactor/`      | `FORGE_REACTOR_TOKEN`      |

```sh
curl -H "Authorization: Bearer $FORGE_REACTOR_TOKEN" \
  "$ENVIRONMENT_URL_TEST/reactor/v1/subscriptions"
```

Only REST is exposed; the management web interface does not work under its
prefix, but its `/management/v1/` API does. gRPC, the broker, the proxy and
RabbitMQ stay on the server.

### Reactor

Point forge webhooks at `<ENVIRONMENT_URL_TEST>/reactor/webhooks/github` or
`/reactor/webhooks/forgejo`, signed with `FORGE_REACTOR_GITHUB_WEBHOOK_SECRET`
or `FORGE_REACTOR_FORGEJO_WEBHOOK_SECRET`. Consume queues through an SSH
tunnel to RabbitMQ, as user `reactor` with `FORGE_REACTOR_RABBITMQ_PASSWORD`:

```sh
ssh -L 5672:127.0.0.1:5672 -L 15672:127.0.0.1:15672 <user>@<WG_IP_ADDR>
```

## Settings

Set these in the server's `.env` if the defaults clash:

| Variable                    | Default           |
| --------------------------- | ----------------- |
| `FORGE_PROXY_NETWORK`       | `proxy`           |
| `FORGE_HTTP_PUBLISH`        | `127.0.0.1:6080`  |
| `FORGE_AMQP_PUBLISH`        | `127.0.0.1:5672`  |
| `FORGE_RABBITMQ_UI_PUBLISH` | `127.0.0.1:15672` |
| `FORGE_IMAGE_TAG`           | `latest`          |

## Reset

On the server, `docker compose down -v` in `~/services/forgetools` deletes all
data, including connections and stored tokens. Keep `.env` to keep the
service tokens; the next deploy recreates the rest.
