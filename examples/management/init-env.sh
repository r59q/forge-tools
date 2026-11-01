#!/bin/sh
# Creates .env with a random value for every secret the stack needs.
set -eu
cd "$(dirname "$0")"
if [ -e .env ]; then
  echo ".env already exists; remove it first to start over." >&2
  exit 1
fi
umask 077
{
  echo "FORGE_BROKER_KEY=$(openssl rand -base64 32)"
  for name in FORGE_BROKER_ADMIN_TOKEN FORGE_MANAGEMENT_TOKEN FORGE_PROXY_TOKEN \
    FORGE_REPOSITORIES_CONNECTOR_TOKEN FORGE_REPOSITORIES_TOKEN; do
    echo "$name=$(openssl rand -hex 32)"
  done
} > .env
echo "Created .env. Sign in to management with FORGE_MANAGEMENT_TOKEN."
