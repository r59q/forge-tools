# Management setup example

Runs the whole stack with Docker Compose: the
[forge-connector](../../forge-connector/) broker and proxy, the
[repositories service](../../forge-repositories/service/), the
[issues service](../../forge-issues/service/) and the
[management interface](../../forge-tools-management/). You connect forges in
the browser.

It needs Docker Compose and `openssl`. Run all commands from this folder.

## Start

```sh
./init-env.sh
docker compose up --build -d
```

`init-env.sh` creates `.env` with a random value for every secret. On an
existing `.env` it adds only the missing secrets, so run it again after
updating this example. It never changes existing values.

| Secret | Used for |
| --- | --- |
| `FORGE_BROKER_KEY` | Encrypts stored forge tokens. Back it up; losing it loses them. |
| `FORGE_BROKER_ADMIN_TOKEN` | Lets management administer the broker. |
| `FORGE_MANAGEMENT_TOKEN` | Your password for the management interface. |
| `FORGE_PROXY_TOKEN` | Lets the proxy fetch forge tokens from the broker. |
| `FORGE_REPOSITORIES_CONNECTOR_TOKEN` | Lets the repositories service use connections through the proxy. |
| `FORGE_REPOSITORIES_TOKEN` | Lets management call the repositories service. |
| `FORGE_ISSUES_CONNECTOR_TOKEN` | Lets the issues service use connections through the proxy. |
| `FORGE_ISSUES_TOKEN` | Lets management call the issues service. |

At startup, management registers the proxy, repositories and issues tokens
in the broker. To rotate any secret except `FORGE_BROKER_KEY`, change it in
`.env` and run `docker compose up -d` again.

## Connect a forge

1. Open `http://127.0.0.1:8083/` and sign in as `operator`, with
   `FORGE_MANAGEMENT_TOKEN` from `.env` as the password.
2. Go to **Connections** and add a forge account: choose the forge, enter an
   access token with read access to your repositories and issues, and
   optionally a host for self-hosted forges, such as `gitlab.example.com`.
   The token is checked with the forge, stored encrypted and never shown
   again.
3. Go to **Repositories**. It lists the repositories of every connection.
4. Go to **Issues**. It lists the open issues assigned to the connected
   accounts. Filter by state, or show the issues they created. On
   **Repositories**, **Issues** under a repository lists that repository's
   issues.

Add more accounts or forges the same way. Use **Test** to recheck a token,
**Replace token** to rotate it and **Remove** to delete a connection. Changes
apply right away, without restarts.

For a GitHub token, open *Settings → Developer settings → Personal access
tokens*. GitLab and Forgejo have personal access tokens in user settings.

## Notes

- Only management is reachable from the host, on `127.0.0.1`. The other
  services talk over the Compose network in plaintext; use TLS endpoints
  for remote access.
- All operators see what the connected accounts can see. Management grants
  no forge permissions.
- The same features are available over REST and gRPC, with
  `Authorization: Bearer <management token>`; see the
  [service API reference](../../forge-tools-management/service/README.md#api).

## Clean up

`down -v` also deletes the broker volume with the stored forge tokens:

```sh
docker compose down -v
rm .env
```

Close the browser session to clear its sign-in.
