#!/bin/sh
# Creates .env with a random value for every secret the stack needs. If .env
# exists, it adds only the missing secrets and never changes existing ones.
set -eu
cd "$(dirname "$0")"
umask 077
created=false
[ -e .env ] || created=true
touch .env
# Start appended lines on a new line.
if [ -s .env ] && [ -n "$(tail -c 1 .env)" ]; then
  echo >> .env
fi
added=0
add() {
  if ! grep -q "^$1=" .env; then
    echo "$1=$2" >> .env
    added=$((added + 1))
  fi
}
add FORGE_BROKER_KEY "$(openssl rand -base64 32)"
for name in FORGE_BROKER_ADMIN_TOKEN FORGE_MANAGEMENT_TOKEN FORGE_PROXY_TOKEN \
  FORGE_REPOSITORIES_CONNECTOR_TOKEN FORGE_REPOSITORIES_TOKEN \
  FORGE_ISSUES_CONNECTOR_TOKEN FORGE_ISSUES_TOKEN \
  FORGE_CHECKOUTS_CONNECTOR_TOKEN FORGE_CHECKOUTS_TOKEN \
  FORGE_ACTIONS_CONNECTOR_TOKEN FORGE_ACTIONS_TOKEN \
  FORGE_EXPLORER_TOKEN FORGE_PIPEPRINT_TOKEN \
  FORGE_REACTOR_TOKEN FORGE_REACTOR_CONNECTOR_TOKEN FORGE_REACTOR_RABBITMQ_PASSWORD \
  FORGE_REACTOR_WEBHOOK_SECRET; do
  add "$name" "$(openssl rand -hex 32)"
done
if $created; then
  echo "Created .env. Sign in to management with FORGE_MANAGEMENT_TOKEN."
else
  echo "Added $added missing secrets to .env. Existing values are unchanged."
fi
