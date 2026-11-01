# Management setup example

This example builds local images for the read-only management interface and
an optional repositories service. It needs Docker Compose and `openssl`.
Run all commands from this folder.

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

1. Set `GITHUB_TOKEN` in `.env` to a GitHub token with read access to the
   repositories you want operators to see. The [repositories configuration
   reference](../../forge-repositories/service/README.md#configure) explains
   other forges, multiple sources and optional connector proxy connections.
   Change `repositories.json` for those setups and pass the corresponding
   token variables or mounted files to the repositories container.
2. Set these two lines in `.env`:

   ```dotenv
   MANAGEMENT_REPOSITORIES_ADDRESS=repositories:9090
   MANAGEMENT_REPOSITORIES_INSECURE=true
   ```

   Plaintext gRPC is used only inside this local Compose network. Neither
   service is exposed beyond localhost. Use TLS endpoints for remote access.
3. Start the backend and recreate management with the new settings:

   ```sh
   docker compose --profile repositories up --build -d
   ```

Refresh the page. Every row shows a full repository path, source ID, forge,
host and visibility. Operators share the upstream account's visibility;
management does not grant forge permissions. Repeated paths on different
sources remain separate rows. A successful empty listing shows **No
repositories found**. Stopping the backend demonstrates the failure state:

```sh
docker compose --profile repositories stop repositories
```

Refresh to see **Repository listing failed**; no stale or partial listing is
displayed. Restart with `docker compose --profile repositories up -d`.

## API and cleanup

For REST, use `GET http://127.0.0.1:8083/v1/repositories` with
`Authorization: Bearer <management operator token>`. gRPC is available at
`127.0.0.1:9093`; see the [service API reference](../../forge-tools-management/service/README.md#api).

```sh
docker compose --profile repositories down
rm .env
```

Close the browser session to clear its Basic authentication credentials.
