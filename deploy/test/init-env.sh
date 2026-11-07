#!/bin/sh
# Runs on the server. Adds a random value to .env for every missing secret,
# and never changes existing ones. Creates an empty reactor.json and the proxy
# network if missing.
set -eu
cd "$(dirname "$0")"
umask 077
touch .env

add() {
  if ! grep -q "^$1=" .env; then
    echo "$1=$2" >> .env
  fi
}

add FORGE_BROKER_KEY "$(openssl rand -base64 32)"
for name in FORGE_BROKER_ADMIN_TOKEN FORGE_MANAGEMENT_TOKEN FORGE_PROXY_TOKEN \
  FORGE_REPOSITORIES_TOKEN FORGE_REPOSITORIES_CONNECTOR_TOKEN \
  FORGE_ISSUES_TOKEN FORGE_ISSUES_CONNECTOR_TOKEN \
  FORGE_ACTIONS_TOKEN FORGE_ACTIONS_CONNECTOR_TOKEN \
  FORGE_CHECKOUTS_TOKEN FORGE_CHECKOUTS_CONNECTOR_TOKEN \
  FORGE_CODE_TOKEN FORGE_CODE_CONNECTOR_TOKEN \
  FORGE_CHANGES_TOKEN FORGE_CHANGES_CONNECTOR_TOKEN \
  FORGE_REVIEW_TOKEN FORGE_REVIEW_CONNECTOR_TOKEN \
  FORGE_PIPEPRINT_TOKEN \
  FORGE_REACTOR_TOKEN FORGE_REACTOR_CONNECTOR_TOKEN FORGE_REACTOR_RABBITMQ_PASSWORD \
  FORGE_REACTOR_GITHUB_WEBHOOK_SECRET FORGE_REACTOR_FORGEJO_WEBHOOK_SECRET; do
  add "$name" "$(openssl rand -hex 32)"
done

# Compose would mount a missing file as a directory.
if [ ! -e reactor.json ]; then
  (umask 022 && echo '{"webhooks": [], "polls": []}' > reactor.json)
fi

# The network nginx shares with the TLS proxy.
network=$(sed -n 's/^FORGE_PROXY_NETWORK=//p' .env)
network=${network:-proxy}
if ! docker network inspect "$network" >/dev/null 2>&1; then
  docker network create "$network" >/dev/null
fi
