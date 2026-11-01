# Management setup example

This example builds local images for the read-only management interface and,
optionally, the repositories service with the
[forge-connector](../../forge-connector/) that it reads through. It needs Docker
Compose, `curl`, `jq` and `openssl`. Run all commands from this folder.

## Start with no tools

Create a private environment file with two distinct random service tokens:

```sh
umask 077
cp .env.example .env
sed -i "s/^FORGE_MANAGEMENT_TOKEN=.*/FORGE_MANAGEMENT_TOKEN=$(openssl rand -hex 32)/" .env
sed -i "s/^FORGE_REPOSITORIES_TOKEN=.*/FORGE_REPOSITORIES_TOKEN=$(openssl rand -hex 32)/" .env
docker compose up --build -d
```

Open `http://127.0.0.1:8083/`. Sign in at the browser prompt as `operator`,
using `FORGE_MANAGEMENT_TOKEN` from your local `.env` as the password. The
page shows **No repository capability configured**. No forge or other tool
is needed. Do not share the `.env` or use the repository service token in
the browser.

## Connect repositories

The connector stores forge credentials. The repositories service only holds a
connector client token and shows the repositories of every connection that
token may use.

1. Create the broker secrets and start the broker:

   ```sh
   sed -i "s|^FORGE_BROKER_KEY=.*|FORGE_BROKER_KEY=$(openssl rand -base64 32)|" .env
   sed -i "s|^FORGE_BROKER_ADMIN_TOKEN=.*|FORGE_BROKER_ADMIN_TOKEN=$(openssl rand -hex 32)|" .env
   docker compose --profile repositories up --build -d broker

   . ./.env
   broker() { curl -fsS --retry 5 --retry-all-errors --retry-delay 1 \
     -H "Authorization: Bearer $FORGE_BROKER_ADMIN_TOKEN" "$@"; }
   ```

2. Connect a forge. Enter a GitHub token with read access to your
   repositories at the prompt; the broker checks it with GitHub and stores it
   encrypted. Repeat for more accounts or forges, such as GitLab
   (`https://gitlab.com/api/v4`) or Forgejo (`https://codeberg.org/api/v1`):

   ```sh
   read -rs GITHUB_TOKEN
   FORGE=$(broker localhost:8080/v1/forges \
     -d '{"kind":"github","baseUrl":"https://api.github.com","name":"GitHub"}' | jq -r .id)
   CONN=$(broker localhost:8080/v1/connections \
     -d "{\"forgeId\":\"$FORGE\",\"name\":\"me\",\"credential\":\"$GITHUB_TOKEN\"}" | jq -r .id)
   unset GITHUB_TOKEN
   ```

3. Create two clients for that connection: one for the proxy, which may issue
   forge tokens, and one for the repositories service, which may not. List
   every connection in `connectionIds`; scopes cannot change later.

   ```sh
   client() { broker localhost:8080/v1/clients \
     -d "{\"name\":\"$1\",\"connectionIds\":[\"$CONN\"],\"canIssueTokens\":$2}" | jq -r .token; }
   sed -i "s|^FORGE_PROXY_TOKEN=.*|FORGE_PROXY_TOKEN=$(client proxy true)|" .env
   sed -i "s|^FORGE_REPOSITORIES_CONNECTOR_TOKEN=.*|FORGE_REPOSITORIES_CONNECTOR_TOKEN=$(client repositories false)|" .env
   ```

4. Point management at the repositories service by setting these two lines in
   `.env`:

   ```dotenv
   MANAGEMENT_REPOSITORIES_ADDRESS=repositories:9090
   MANAGEMENT_REPOSITORIES_INSECURE=true
   ```

   Plaintext is used only inside this local Compose network. Only the broker
   admin port and management are exposed, on localhost. Use TLS endpoints for
   remote access.

5. Start the rest and recreate management with the new settings:

   ```sh
   docker compose --profile repositories up --build -d
   ```

Refresh the page. Every row shows a full repository path, source ID (the
connection ID), forge, host and visibility. Operators share the connected
account's visibility; management does not grant forge permissions. Repeated
paths on different connections remain separate rows, and revoked connections
are skipped. A successful empty listing shows **No repositories found**.
Stopping the backend demonstrates the failure state:

```sh
docker compose --profile repositories stop repositories
```

Refresh to see **Repository listing failed**; no stale or partial listing is
displayed. Restart with `docker compose --profile repositories up -d`.

## API and cleanup

For REST, use `GET http://127.0.0.1:8083/v1/repositories` with
`Authorization: Bearer <management operator token>`. gRPC is available at
`127.0.0.1:9093`; see the [service API reference](../../forge-tools-management/service/README.md#api).

`down -v` also deletes the broker volume with the stored forge credentials:

```sh
docker compose --profile repositories down -v
rm .env
```

Close the browser session to clear its Basic authentication credentials.
