# Examples

## Docker Compose

[docker-compose.yml](docker-compose.yml) runs the published
[broker](../forge-connector/broker/) and [proxy](../forge-connector/proxy/)
images. Ports are bound to localhost.

1. Create the secrets and start the broker:

   ```sh
   cp .env.example .env
   sed -i "s|^FORGE_BROKER_KEY=.*|FORGE_BROKER_KEY=$(openssl rand -base64 32)|" .env
   sed -i "s|^FORGE_BROKER_ADMIN_TOKEN=.*|FORGE_BROKER_ADMIN_TOKEN=$(openssl rand -hex 32)|" .env
   docker compose up -d
   ```

2. Create a client for the proxy. It needs `canIssueTokens` and access to the
   connections it serves. The token is returned once:

   ```sh
   . ./.env
   curl -s -X POST localhost:8080/v1/clients \
     -H "Authorization: Bearer $FORGE_BROKER_ADMIN_TOKEN" \
     -d '{"name":"proxy","canIssueTokens":true}'
   ```

3. Put the returned token in `.env` as `FORGE_PROXY_TOKEN` and start the proxy:

   ```sh
   docker compose --profile proxy up -d
   ```

Then create forges and connections as described in the broker README, and
grant the proxy client access to them. Check the proxy with
`curl localhost:8081/healthz`.
